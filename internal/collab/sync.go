// SCOPE:layer=infra,removal=plugin — Loro CRDT + DocStore + sync workers + presence
package collab

import (
	"context"
	"strings"

	natsio "github.com/nats-io/nats.go"
)

// SyncWorker is the NATS subscriber side. REQUIRES NATS.
// If you keep only WebSyncWorker (SSE-only), the whiteboard works without NATS.
// SyncWorker subscribes to collaborative doc updates on the NATS subject
// app.sync.> and applies them to the shared DocStore (the same one the
// WebSyncWorker uses), then persists the resolved snapshot via the
// Persister. This is the server side of cross-instance sync: browser ops
// published by WebSyncWorker to NATS are received here and applied to the
// shared doc, converging every instance.
//
// Topic layout: "app.sync.<docID>" carries a Loro update for that doc.
type SyncWorker struct {
	nc        *natsio.Conn
	persister Persister
	docs      *DocStore // shared with WebSyncWorker
}

// NewSyncWorker builds a worker bound to a NATS connection and a
// Persister. docs is the shared DocStore (pass the same one used by
// WebSyncWorker).
func NewSyncWorker(nc *natsio.Conn, p Persister, docs *DocStore) *SyncWorker {
	if docs == nil {
		docs = NewDocStore()
	}
	return &SyncWorker{
		nc:        nc,
		persister: p,
		docs:      docs,
	}
}

func (w *SyncWorker) doc(docID string) *Doc {
	return w.docs.GetOrCreate(docID)
}

// Run subscribes to app.sync.> and blocks processing updates until the
// context is cancelled (subscription drained) or the connection closes.
// Callers typically run it in a goroutine and cancel on shutdown.
func (w *SyncWorker) Run(ctx context.Context) error {
	sub, err := w.nc.Subscribe("app.sync.>", func(msg *natsio.Msg) {
		docID := docIDFromSubject(msg.Subject)
		if docID == "" {
			return
		}
		d := w.doc(docID)
		if err := d.ApplyUpdate(msg.Data); err != nil {
			// A malformed update is skipped — CRDT convergence must not
			// crash the worker on one bad message.
			return
		}
		// Persist the resolved snapshot as the source of truth.
		_ = w.persister.SaveSnapshot(docID, d.EncodeSnapshot())
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

// docIDFromSubject extracts the doc id from "app.sync.<docID>".
func docIDFromSubject(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}
