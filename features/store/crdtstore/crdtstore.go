// SCOPE:layer=infra,removal=plugin — Loro CRDT-backed EntityStore strategy for todos
package crdtstore

//
// Single source of truth = PocketBase. Every todo is eventually a
// normal record in the `todos` collection (the SAME collection PBStore
// uses, with the SAME fields: id, title, completed, created, updated,
// owner, idem_key). The admin UI, SQL queries, and PocketBase realtime
// all work against those records exactly as they do for PBStore.
//
// The Loro document is an in-memory CRDT merge workspace, one per
// owner. It holds the authoritative *merged* state and is what
// List/Get/Count read from. On every mutation the resolved todos are
// projected (upserted/deleted) into the `todos` collection, and on
// first access for an owner the Loro doc is rebuilt from the existing
// `todos` records so a restart restores state from the same table
// everything else uses.
//
// Why keep the Loro doc at all? It gives automatic, conflict-free
// convergence of concurrent offline edits from multiple devices for
// the same owner — the CRDT merge semantics PBStore (last-writer-wins
// per field) does not provide. The PB records are the durable,
// queryable projection; the doc is the merge engine.
//
// Trade-off vs PBStore:
//
//   - ✅ Auto-merge of concurrent edits (CRDT magic).
//   - ✅ Offline-first by construction: ops replay converges.
//   - ❌ No SQL queries: List/filter is a full-doc scan over the LoroMap.
//   - ❌ Migration from PBStore is a no-op copy (the `todos` records
//     are already compatible).
//
// Realtime: mutations write normal `todos` records, so PocketBase
// realtime already delivers per-owner updates to subscribed clients —
// the same path PBStore uses. An optional JetStream op-transport
// (SetTransport) additionally ships Loro ops across instances for
// cross-instance convergence; it is OPTIONAL and OFF by default. The
// SSE Hub publisher (SetPublisher) emits a doc-version tick so any
// consumer can trigger a resync; also optional. Choose the strategy
// with one env var: ENTITY_STORE=pb (default) or ENTITY_STORE=crdt.
//
// File layout (split for the 500-line budget):
//
//   - crdtstore.go      — type, wiring (publisher/transport), schema,
//     versioning/watch, remote-op ingest, lifecycle.
//   - crdtstore_doc.go  — Loro doc rehydration + PocketBase projection
//     (doc, persistRecords, upsert, record/item codecs).
//   - crdtstore_crud.go — EntityStore[todo.Todo] CRUD operations.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/aholstenson/loro-go"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
)

// itemsContainerName is the LoroMap root that holds every todo for a
// given owner's doc. Each entry is itself a LoroMap with the todo
// fields; the entry key is the todo ID.
const itemsContainerName = "items"

// todosCollectionName is the normal PocketBase collection the resolved
// todos are projected into. It is the SAME collection PBStore uses
// (same id/title/completed/created/updated/owner/idem_key fields), so
// the admin UI, SQL queries, and PocketBase realtime all see the same
// data regardless of which strategy is active. Exported so EnsureSchema
// and tests can reference it without duplicating the literal.
const todosCollectionName = "todos"

// CRDTStore is the CRDT-backed implementation of EntityStore[todo.Todo].
// One in-memory LoroDoc per owner is the CRDT merge workspace; the
// resolved todos are projected as normal records into the `todos`
// PocketBase collection (shared with PBStore).
type CRDTStore struct {
	app core.App

	mu   sync.Mutex
	docs map[string]*loro.LoroDoc // ownerID -> doc (lazy on first access)

	// transport is the cross-instance JetStream op publisher
	// (optional). nil = single-process mode (publish is a no-op).
	transport *CRDTTransport

	// versionMu protects versions + watchers + publisher. Bumped
	// by bumpVersion after every persistRecords (both local and
	// remote). The version counter is what Watch() subscribers
	// receive via buffered chan; publisher (if set) is called
	// synchronously to fan out the doc-version-bumped event to
	// whatever sits downstream (typically the SSE Hub).
	versionMu     sync.Mutex
	versions      map[string]uint64    // ownerID -> version (0 = unseen)
	watchers      []*watchSubscription // signal-driven listeners
	publisher     DocPublisher         // optional cross-store event sink
	publisherName string               // diagnostics label for publisher
}

// DocPublisher is the cross-store event sink invoked from
// bumpVersion after every persistRecords. The router wires this to
// the SSE Hub so each connected client of a given owner sees the
// new doc version and re-fetches the fragment. Implementations
// MUST NOT block — the publisher callback is called under
// versionMu, so a slow callback blocks every future bumpVersion.
type DocPublisher interface {
	PublishDocEvent(ownerID string, version uint64)
}

// SetPublisher wires a downstream event sink (typically the SSE
// Hub via router.WireCRDTStorePublisher). Re-setting the publisher
// replaces the previous one (idempotent for production where it's
// set once at boot). Passing nil removes the publisher.
func (s *CRDTStore) SetPublisher(p DocPublisher) {
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	s.publisher = p
	s.publisherName = publisherName(p)
}

// PublisherName returns the diagnostics label of the currently
// configured publisher, or "" if none.
func (s *CRDTStore) PublisherName() string {
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	return s.publisherName
}

func publisherName(p DocPublisher) string {
	if p == nil {
		return ""
	}
	if named, ok := p.(interface{ Name() string }); ok {
		return named.Name()
	}
	return fmt.Sprintf("%T", p)
}

// versionEvent is the payload pushed to Watch() subscribers whenever
// an owner's doc version bumps. Owner is included so a single Watch
// goroutine can fan out to multiple owners if needed.
type versionEvent struct {
	owner   string
	version uint64
}

// watchSubscription is one Watch() consumer. The ch is buffered; if
// it fills, bumpVersion skips the slot (Phase 3 graceful degradation
// for slow consumers).
type watchSubscription struct {
	ch chan versionEvent
}

// New constructs a CRDTStore. The snapshot collection must exist
// before first use; call EnsureSchema() at startup.
func New(app core.App) *CRDTStore {
	return &CRDTStore{
		app:  app,
		docs: make(map[string]*loro.LoroDoc),
	}
}

// SetTransport wires the optional cross-instance JetStream op publisher.
// Pass nil to disable cross-instance sync (single-process mode,
// default). Call before any request handler runs. The caller is
// responsible for starting the consumer (Subscribe) and for running
// the goroutine that pumps the doc's encoded updates into the
// transport.
func (s *CRDTStore) SetTransport(t *CRDTTransport) { s.transport = t }

// publishOpFromDoc encodes d as a Loro Update and ships it to peers.
// Caller is responsible for holding (or not holding) s.mu as needed:
// the publish step itself doesn't touch s.mu. Use this when the doc
// is already in hand to avoid re-locking (Create holds s.mu for the
// whole insert + saveSnapshot + publish sequence).
func (s *CRDTStore) publishOpFromDoc(ctx context.Context, ownerID, opID string, d *loro.LoroDoc) {
	if s.transport == nil {
		return
	}
	if d == nil {
		return
	}
	snap, err := d.Export(loro.UpdatesMode(loro.NewVersionVector()))
	if err != nil {
		slog.Warn("crdtstore: export update failed", "owner", ownerID, "op", opID, "error", err)
		return
	}
	if err := s.transport.Publish(ctx, Op{
		ID:      opID,
		OwnerID: ownerID,
		Updates: snap,
	}); err != nil {
		slog.Warn("crdtstore: transport publish failed", "owner", ownerID, "op", opID, "error", err)
	}
}

// EnsureSchema makes sure the `todos` collection exists with the fields
// CRDTStore writes. In production the collection is created by
// db/seed.go (which also adds the idem_key unique index); here we only
// create it if it is somehow missing (e.g. isolated tests). Idempotent.
func (s *CRDTStore) EnsureSchema() error {
	if _, err := s.app.FindCollectionByNameOrId(todosCollectionName); err == nil {
		return nil
	}
	col := core.NewBaseCollection(todosCollectionName)
	// Field set mirrors db/seed.go's ensureTodosCollection so the
	// collection CRDTStore creates (isolated tests / first-boot
	// fallback) is byte-compatible with the production one.
	col.Fields.Add(
		&core.TextField{Name: fieldTitle, Required: true},
		&core.BoolField{Name: fieldCompleted},
		&core.DateField{Name: "created"},
		&core.DateField{Name: "updated"},
		&core.RelationField{Name: "owner", MaxSelect: 1, CollectionId: "_pb_users_auth_"},
		&core.TextField{Name: "idem_key", Max: 64},
	)
	// Unique (idem_key, owner) so offline replays dedupe, matching the
	// index db/seed.go adds in production. idem_key may be empty for
	// CRDTStore writes (the stable client id handles dedup there).
	col.AddIndex("idx_todos_idem_owner", true, "idem_key", "owner")
	if err := s.app.Save(col); err != nil {
		return fmt.Errorf("crdtstore: create %q collection: %w", todosCollectionName, err)
	}
	return nil
}

// ApplyRemoteOp applies a Loro update received from a peer via the
// JetStream transport. Concurrent-safe. The local doc merges the
// incoming op automatically (Loro CRDT magic); we just save a
// snapshot afterwards so a future peer reconnect can catch up.
//
// Per the transport's loop filter, this method is only called for
// ops emitted by OTHER processes — the in-process publisher is
// filtered by the Subscribe handler.
func (s *CRDTStore) ApplyRemoteOp(_ context.Context, ownerID string, op Op) error {
	if ownerID == "" {
		return errors.New("crdtstore ApplyRemoteOp: empty ownerID")
	}
	if len(op.Updates) == 0 {
		return nil // no-op: empty update bytes
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return err
	}
	if _, err := d.Import(op.Updates); err != nil {
		return fmt.Errorf("crdtstore ApplyRemoteOp: import: %w", err)
	}
	if err := s.persistRecords(ownerID, d); err != nil {
		return fmt.Errorf("crdtstore ApplyRemoteOp: persist: %w", err)
	}
	// Emit a "doc version bumped" event so the UI can re-fetch.
	// The publisher (if wired to the SSE Hub) fans it out; PB realtime
	// also delivers the underlying `todos` record change directly.
	s.bumpVersion(ownerID)
	slog.Debug("crdtstore: applied remote op", "owner", ownerID, "op", op.ID, "publisher", op.PublisherID)
	return nil
}

// bumpVersion increments the in-memory version counter for an owner
// and fans out the new version to subscribers via Watch AND to the
// optional publisher (when one is wired). The counter is the
// ground-truth for catch-up reads on reconnect; the channel +
// publisher are the live notification paths.
func (s *CRDTStore) bumpVersion(ownerID string) {
	s.versionMu.Lock()
	if s.versions == nil {
		s.versions = make(map[string]uint64)
	}
	s.versions[ownerID]++
	v := s.versions[ownerID]
	// Non-blocking fan-out to subscribers. Each Watch consumer
	// buffers its own channel; if the buffer is full, the slot is
	// skipped (the next bump fills a fresh slot, so the latest
	// version always lands for a non-pathologically slow consumer).
	for _, w := range s.watchers {
		select {
		case w.ch <- versionEvent{owner: ownerID, version: v}:
		default:
		}
	}
	// Optional downstream publisher (SSE Hub). Called under the
	// versionMu lock — implementations MUST NOT block. A blocked
	// publisher stalls every future bumpVersion for this store.
	if s.publisher != nil {
		s.publisher.PublishDocEvent(ownerID, v)
	}
	s.versionMu.Unlock()
}

// Version returns the current version counter for an owner (or 0).
// Tests + the SSE broadcaster (wired via SetPublisher) use this to
// detect a "doc version bumped" event.
func (s *CRDTStore) Version(ownerID string) uint64 {
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	return s.versions[ownerID]
}

// Watch returns a channel that receives a uint64 every time the
// owner's doc version bumps. The channel is buffered (size 8); if
// the buffer fills, events are dropped (the next bump fills a fresh
// slot, so the latest version always lands). The watcher is removed
// when cancel is called (SSE hub disconnect). Replay-first: the
// current version is sent immediately so a reconnected client
// receives the catch-up value before any new events.
func (s *CRDTStore) Watch(ownerID string) (<-chan uint64, func()) {
	const watchOutBuf = 8
	const watchInternalBuf = 16
	out := make(chan uint64, watchOutBuf)
	internal := make(chan versionEvent, watchInternalBuf)
	s.versionMu.Lock()
	s.watchers = append(s.watchers, &watchSubscription{ch: internal})
	s.versionMu.Unlock()
	go func() {
		defer close(out)
		// Send initial snapshot value (0 = no events yet).
		out <- s.Version(ownerID)
		for ev := range internal {
			if ev.owner != ownerID {
				continue
			}
			select {
			case out <- ev.version:
			default:
			}
		}
	}()
	cancel := func() {
		s.versionMu.Lock()
		defer s.versionMu.Unlock()
		for i, w := range s.watchers {
			if w.ch == internal {
				s.watchers = append(s.watchers[:i], s.watchers[i+1:]...)
				close(internal)
				return
			}
		}
	}
	return out, cancel
}

// Close releases all per-owner in-memory state (docs, versions,
// watchers, publisher). Safe to call multiple times. The PocketBase
// app and the JetStream transport are owned by their callers.
func (s *CRDTStore) Close() error {
	s.mu.Lock()
	s.docs = make(map[string]*loro.LoroDoc)
	s.mu.Unlock()
	s.versionMu.Lock()
	s.versions = make(map[string]uint64)
	s.watchers = nil
	s.publisher = nil
	s.publisherName = ""
	s.versionMu.Unlock()
	return nil
}

// compile-time guard: CRDTStore must satisfy EntityStore[todo.Todo].
// Adding a method here without implementing it would now be a compile
// error instead of a runtime panic.
var _ store.EntityStore[todo.Todo] = (*CRDTStore)(nil)
