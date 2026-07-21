// SCOPE:layer=infra,removal=plugin — Enables cross-instance
// Leaf Node replicates, server consumer writes to server PocketBase).
//
// Architecture:
//
//	Desktop (NATS Leaf Node)         Server
//	┌─────────────────────┐         ┌──────────────────────┐
//	│  Todo handler        │         │  CrudConsumer         │
//	│  ─ Publishes to NATS │ ────►   │  ─ Reads app.crud.*   │
//	│  ─ Writes to local PB│  Leaf   │  ─ Writes to server PB│
//	│  ─ Returns SSE       │  Node   │  ─ PB realtime → web  │
//	└─────────────────────┘  sync    └──────────────────────┘
//
// The handler ALWAYS writes directly to PocketBase (fast path, no
// latency). The NATS publication is ADDITIONAL (async, best-effort)
// so other instances converge. The desktop's local JetStream persists
// messages when offline and replays them when the Leaf Node reconnects.
//
// ID MANAGEMENT: The create operation carries the PocketBase-generated
// ID so the server consumer creates the record with the SAME ID. This
// ensures toggle/delete operations (which reference that ID) work
// correctly when replicated to the server. PocketBase IDs are 15-char
// [a-z0-9], so collision risk is negligible.
package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	natsio "github.com/nats-io/nats.go"
	"github.com/pocketbase/pocketbase/core"
)

// CRUD stream and subject constants.
const (
	crudStreamName    = "APP_CRUD"
	crudSubjectPrefix = "app.crud.todo."
	crudDurableFmt    = "%s-crud"
)

// CrudOpType is the kind of CRUD operation published to NATS.
type CrudOpType string

const (
	CrudOpCreate         CrudOpType = "create"
	CrudOpToggle         CrudOpType = "toggle"
	CrudOpDelete         CrudOpType = "delete"
	CrudOpClearCompleted CrudOpType = "clear_completed"
)

// CrudPayload is the wire format for a CRUD operation published to NATS.
type CrudPayload struct {
	Op     CrudOpType  `json:"op"`
	UserID string      `json:"userId"`
	Data   *CrudOpData `json:"data,omitempty"`
}

// CrudOpData carries the per-operation data. ID is REQUIRED for
// toggle/delete (must match the PocketBase record ID on the target
// instance). For create, the handler includes the ID the local
// PocketBase generated so the remote consumer reuses the same ID.
type CrudOpData struct {
	ID        string `json:"id,omitempty"`
	Title     string `json:"title,omitempty"`
	Completed bool   `json:"completed,omitempty"`
}

// CrudPublisher publishes todo CRUD operations to JetStream.
// Each handler (create/toggle/delete) calls Publish AFTER writing to
// PocketBase, so the SSE response is fast (PB write is the source of
// truth). The NATS publication is best-effort convergence for other
// instances (or the server, for desktop edges).
//
// Safe to call with a nil receiver (NATS unavailable) — it's a no-op.
type CrudPublisher struct {
	js   natsio.JetStreamContext
	mu   sync.Mutex
	done bool
}

// NewCrudPublisher ensures the APP_CRUD stream exists and returns a
// publisher. Returns nil when js is nil (NATS disabled), so callers
// can call Publish on nil safely (no-op).
func NewCrudPublisher(js natsio.JetStreamContext) *CrudPublisher {
	if js == nil {
		return nil
	}
	if _, err := js.AddStream(&natsio.StreamConfig{
		Name:     crudStreamName,
		Subjects: []string{crudSubjectPrefix + ">"},
		Storage:  natsio.FileStorage,
	}); err != nil {
		if err.Error() != "stream already exists" {
			slog.Warn("crudproxy: add stream failed", "error", err)
			return nil
		}
	}
	return &CrudPublisher{js: js}
}

// Publish sends a CRUD operation to JetStream. Non-blocking from the
// caller's perspective: logs on error but returns immediately. The
// handler has already written to PocketBase, so this is additive sync.
func (p *CrudPublisher) Publish(op CrudOpType, userID string, data *CrudOpData) {
	if p == nil {
		return
	}
	payload, err := json.Marshal(CrudPayload{Op: op, UserID: userID, Data: data})
	if err != nil {
		slog.Warn("crudproxy: marshal payload", "op", op, "error", err)
		return
	}
	subject := crudSubjectPrefix + string(op)
	if _, pubErr := p.js.Publish(subject, payload); pubErr != nil {
		slog.Warn("crudproxy: publish failed", "op", op, "subject", subject, "error", pubErr)
	}
}

// Close drains the publisher. Idempotent.
func (p *CrudPublisher) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	p.done = true
}

// CrudConsumer subscribes to app.crud.todo.> and writes received CRUD
// operations to the local PocketBase instance. It is the SERVER-SIDE
// counterpart to CrudPublisher: desktop edges publish operations to
// their local NATS; the Leaf Node replicates to the server; this
// consumer picks them up and persists to the server's PocketBase.
//
// appName is used to derive the JetStream durable consumer name so
// each project deployment gets its own consumer group, preventing
// stale replays when renaming the project.
//
// Run in a goroutine; cancel ctx to stop.
type CrudConsumer struct {
	app     core.App
	js      natsio.JetStreamContext
	appName string
}

// NewCrudConsumer builds a consumer bound to a PocketBase app.
// appName is used as the durable consumer name prefix.
func NewCrudConsumer(app core.App, js natsio.JetStreamContext, appName string) *CrudConsumer {
	return &CrudConsumer{app: app, js: js, appName: appName}
}

// Run subscribes to app.crud.todo.> and blocks until ctx is cancelled.
func (c *CrudConsumer) Run(ctx context.Context) error {
	if c.js == nil {
		return nil
	}
	durableName := fmt.Sprintf(crudDurableFmt, c.appName)
	sub, err := c.js.Subscribe(crudSubjectPrefix+">", func(msg *natsio.Msg) {
		var p CrudPayload
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			slog.Warn("crudconsumer: unmarshal", "error", err)
			return
		}
		if p.UserID == "" {
			return
		}
		switch p.Op {
		case CrudOpCreate:
			c.handleCreate(p)
		case CrudOpToggle:
			c.handleToggle(p)
		case CrudOpDelete:
			c.handleDelete(p)
		case CrudOpClearCompleted:
			c.handleClearCompleted(p)
		default:
			slog.Warn("crudconsumer: unknown op", "op", p.Op)
		}
		_ = msg.Ack()
	}, natsio.Durable(durableName), natsio.ManualAck())
	if err != nil {
		return err
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

// --- handler methods ---

func (c *CrudConsumer) handleCreate(p CrudPayload) {
	if p.Data == nil || p.Data.Title == "" {
		return
	}
	col, err := c.app.FindCollectionByNameOrId("todos")
	if err != nil {
		slog.Warn("crudconsumer: find todos collection", "error", err)
		return
	}
	rec := core.NewRecord(col)
	// Reuse the same ID so toggle/delete from the edge find the right record.
	if p.Data.ID != "" {
		rec.Id = p.Data.ID
	}
	rec.Set("title", p.Data.Title)
	rec.Set("completed", p.Data.Completed)
	rec.Set("owner", p.UserID)
	if saveErr := c.app.Save(rec); saveErr != nil {
		slog.Warn("crudconsumer: save todo", "id", rec.Id, "error", saveErr)
	}
}

func (c *CrudConsumer) handleToggle(p CrudPayload) {
	if p.Data == nil || p.Data.ID == "" {
		return
	}
	rec, err := c.app.FindRecordById("todos", p.Data.ID)
	if err != nil {
		slog.Warn("crudconsumer: find for toggle", "id", p.Data.ID, "error", err)
		return
	}
	owner := rec.GetString("owner")
	if owner != "" && owner != p.UserID {
		return
	}
	rec.Set("completed", p.Data.Completed)
	if saveErr := c.app.Save(rec); saveErr != nil {
		slog.Warn("crudconsumer: toggle todo", "id", p.Data.ID, "error", saveErr)
	}
}

func (c *CrudConsumer) handleDelete(p CrudPayload) {
	if p.Data == nil || p.Data.ID == "" {
		return
	}
	rec, err := c.app.FindRecordById("todos", p.Data.ID)
	if err != nil {
		slog.Warn("crudconsumer: find for delete", "id", p.Data.ID, "error", err)
		return
	}
	owner := rec.GetString("owner")
	if owner != "" && owner != p.UserID {
		return
	}
	if delErr := c.app.Delete(rec); delErr != nil {
		slog.Warn("crudconsumer: delete todo", "id", p.Data.ID, "error", delErr)
	}
}

func (c *CrudConsumer) handleClearCompleted(p CrudPayload) {
	filter := fmt.Sprintf("completed=true && owner = %q", p.UserID)
	records, err := c.app.FindRecordsByFilter("todos", filter, "", 0, 0)
	if err != nil {
		slog.Warn("crudconsumer: find completed todos", "error", err)
		return
	}
	for _, rec := range records {
		if delErr := c.app.Delete(rec); delErr != nil {
			slog.Warn("crudconsumer: delete completed todo", "id", rec.Id, "error", delErr)
		}
	}
}
