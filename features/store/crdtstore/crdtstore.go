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

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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

// doc returns the LoroDoc for ownerID, lazily creating it and rebuilding
// it from the existing `todos` records (so a restart restores state
// from the same PocketBase table every other strategy uses). Caller
// must hold s.mu if multi-op.
func (s *CRDTStore) doc(ownerID string) (*loro.LoroDoc, error) {
	if d, ok := s.docs[ownerID]; ok {
		return d, nil
	}
	d := loro.NewLoroDoc()
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	records, err := s.app.FindRecordsByFilter(
		todosCollectionName, "owner = {:o}", "-created", 200, 0,
		map[string]any{"o": ownerID},
	)
	// PB v0.39.6's FindRecordsByFilter returns sql.ErrNoRows when the
	// filter matches no records (instead of an empty slice + nil).
	// Treat that as "no existing todos" so the first access for a
	// fresh owner is not an error.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("crdtstore: load todos for %s: %w", ownerID, err)
	}
	for _, r := range records {
		// Key the Loro item map by the todo/idem_key id, NOT the
		// PocketBase row id (r.Id). Every mutating path (Create,
		// Update, Delete, ClearCompleted, persistRecords) keys the
		// items map by the todo id, so the reload must match, or
		// Get/Lookup-by-todo-id and the delete-stale check would
		// see PocketBase record ids instead of todo ids.
		child, iErr := items.InsertMapContainer(r.GetString("idem_key"), loro.NewLoroMap())
		if iErr != nil {
			return nil, fmt.Errorf("crdtstore: rehydrate todo %s: %w", r.Id, iErr)
		}
		if wErr := writeItem(child, todoFromRecord(r)); wErr != nil {
			return nil, wErr
		}
	}
	s.docs[ownerID] = d
	return d, nil
}

// persistRecords projects the resolved doc state for ownerID into the
// `todos` PocketBase collection: it upserts every todo currently in
// the doc and deletes any `todos` record for this owner that is no
// longer in the doc (so Delete/ClearCompleted stay consistent). The
// Loro doc is the CRDT merge workspace; these records are the durable,
// queryable projection shared with PBStore. Called after every mutating
// op. Also bumps the version counter so Watch subscribers and the
// optional publisher are notified.
func (s *CRDTStore) persistRecords(ownerID string, d *loro.LoroDoc) error {
	s.bumpVersion(ownerID)
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	want := make(map[string]todo.Todo)
	for id, vc := range items.All() {
		if vc == nil || !vc.IsContainer() {
			continue
		}
		t := todoFromLoro(id, *vc.AsLoroMap())
		t.ID = id
		want[id] = t
	}
	for _, t := range want {
		if err := s.upsertTodoRecord(ownerID, t); err != nil {
			return err
		}
	}
	// Delete `todos` records for this owner that the doc no longer
	// contains (handles Delete / ClearCompleted).
	have, err := s.app.FindRecordsByFilter(todosCollectionName, "owner = {:o}", "", 200, 0, map[string]any{"o": ownerID})
	// Same sql.ErrNoRows-treat-as-empty normalisation as doc() above.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("crdtstore: list existing todos: %w", err)
	}
	for _, rec := range have {
		// want is keyed by the todo/CRDT id, which is persisted as the
		// record's idem_key. rec.Id is the PocketBase row id (a different
		// namespace) and must NOT be used as the lookup key, or every
		// freshly-upserted record would be seen as "stale" and deleted.
		if _, ok := want[rec.GetString("idem_key")]; ok {
			continue
		}
		if dErr := s.app.Delete(rec); dErr != nil {
			return fmt.Errorf("crdtstore: delete stale todo %s: %w", rec.Id, dErr)
		}
	}
	return nil
}

// upsertTodoRecord writes a single todo as a `todos` record, creating
// it when the id is new or updating the existing one.
func (s *CRDTStore) upsertTodoRecord(ownerID string, t todo.Todo) error {
	col, err := s.app.FindCollectionByNameOrId(todosCollectionName)
	// PB v0.39.6's FindCollectionByNameOrId returns sql.ErrNoRows when
	// the collection does not exist (mirrors the FindRecordsByFilter
	// behaviour). Treat that as "collection missing" rather than an
	// error: callers should have wired EnsureSchema or the seed
	// beforehand; if not, surface a clear diagnostic rather than a
	// driver-flavoured error.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("crdtstore: find %q: %w", todosCollectionName, err)
	}
	if col == nil {
		return fmt.Errorf("crdtstore: collection %q not found (call EnsureSchema or SeedDefaults first)", todosCollectionName)
	}
	var rec *core.Record
	// Look up by (idem_key, owner) instead of by record id. The Loro map
	// key is the client-generated id (possibly 5-char alnum or a UUID),
	// but PB record ids are auto-generated 15-char alnum. idem_key is
	// always the Loro key (i.e. the client-generated id), stored on
	// first Create — so subsequent upserts find the same row via the
	// (idem_key, owner) unique index instead of creating duplicates.
	existing, fErr := s.app.FindFirstRecordByFilter(
		todosCollectionName, "idem_key = {:k} && owner = {:o}",
		map[string]any{"k": t.ID, "o": ownerID},
	)
	if fErr == nil && existing != nil {
		rec = existing
		// Preserve idem_key (it already equals t.ID, but we do not
		// rely on that — preserve rather than Set so the contract is
		// unambiguous).
	} else {
		rec = core.NewRecord(col)
		rec.Set("idem_key", t.ID)
	}
	rec.Set("owner", ownerID)
	rec.Set(fieldTitle, t.Title)
	rec.Set(fieldCompleted, t.Completed)
	if !t.CreatedAt.IsZero() {
		rec.Set("created", t.CreatedAt)
	}
	if !t.UpdatedAt.IsZero() {
		rec.Set("updated", t.UpdatedAt)
	}
	if err := s.app.Save(rec); err != nil {
		return fmt.Errorf("crdtstore: save todo %q: %w", t.ID, err)
	}
	return nil
}

// todoFromRecord decodes a normal `todos` PocketBase record into a todo.
func todoFromRecord(r *core.Record) todo.Todo {
	return todo.Todo{
		ID:        r.Id,
		Title:     r.GetString(fieldTitle),
		Completed: r.GetBool(fieldCompleted),
		CreatedAt: r.GetDateTime("created").Time(),
		UpdatedAt: r.GetDateTime("updated").Time(),
	}
}

// Create inserts a new todo into the owner's doc and projects it as a
// normal `todos` record. The client must supply a PB-compatible id
// (alphanumeric, <=15 chars, matching PocketBase's record-id pattern)
// — it becomes both the Loro map key and the `todos` record id.
// idemKey (the client's request key) is persisted on the record so the
// unique (idem_key, owner) index dedupes offline replays; the stable
// client-generated id additionally makes Create idempotent at the
// Loro-map level (a replayed request reuses the same id and is
// rejected as a duplicate).
func (s *CRDTStore) Create(_ context.Context, e todo.Todo, ownerID, _ string) (todo.Todo, error) {
	if ownerID == "" {
		return todo.Todo{}, errors.New("crdtstore: empty ownerID")
	}
	if e.ID == "" {
		return todo.Todo{}, errors.New("crdtstore: empty todo ID (client must generate UUID)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return todo.Todo{}, err
	}
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	if vc := items.Lookup(e.ID); vc != nil && vc.IsContainer() {
		// Idempotent Create: the same client-generated id already
		// exists (offline replay re-sends the same id). Return the
		// existing entity instead of erroring, so a retried request
		// converges to the original write.
		existing := todoFromLoro(e.ID, *vc.AsLoroMap())
		return existing, nil
	}
	child, err := items.InsertMapContainer(e.ID, loro.NewLoroMap())
	if err != nil {
		return todo.Todo{}, fmt.Errorf("crdtstore: insert map: %w", err)
	}
	if err := writeItem(child, e); err != nil {
		return todo.Todo{}, fmt.Errorf("crdtstore: write item: %w", err)
	}
	if err := s.persistRecords(ownerID, d); err != nil {
		return todo.Todo{}, err
	}
	//nolint:contextcheck
	s.publishOpFromDoc(context.Background(), ownerID, "create-"+e.ID, d)
	// Return the entity read back from the doc so the caller sees the
	// server-assigned timestamps (CreatedAt/UpdatedAt).
	out, ok := findItem(d, e.ID)
	if !ok {
		return todo.Todo{}, errors.New("crdtstore: created todo not found in doc")
	}
	return out, nil
}

// Get returns the todo owned by ownerID with the given id.
func (s *CRDTStore) Get(_ context.Context, ownerID, id string) (todo.Todo, error) {
	if ownerID == "" || id == "" {
		return todo.Todo{}, store.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return todo.Todo{}, err
	}
	t, ok := findItem(d, id)
	if !ok {
		return todo.Todo{}, store.ErrNotFound
	}
	return t, nil
}

// listFilter values for CRDTStore.List. Defined as constants so
// golangci-lint's goconst check stays happy (the strings appear in
// the ClearCompleted helper, the Update filter, and the List switch).
const (
	fieldTitle       = "title"
	fieldCompleted   = "completed"
	listFilterActive = "active"
)

// List returns all todos owned by ownerID. filter is "active",
// "completed", or "" for all. Full-doc scan (no SQL index).
func (s *CRDTStore) List(_ context.Context, ownerID, filter string) ([]todo.Todo, error) {
	if ownerID == "" {
		return []todo.Todo{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return nil, err
	}
	all := readAll(d)
	out := make([]todo.Todo, 0, len(all))
	for _, t := range all {
		switch filter {
		case listFilterActive:
			if !t.Completed {
				out = append(out, t)
			}
		case fieldCompleted:
			if t.Completed {
				out = append(out, t)
			}
		default:
			out = append(out, t)
		}
	}
	return out, nil
}

// Update applies patch to the todo owned by ownerID. Supported patch
// keys: "title", "completed". UpdatedAt is set server-side.
func (s *CRDTStore) Update(_ context.Context, ownerID, id string, patch map[string]any) (todo.Todo, error) {
	if ownerID == "" || id == "" {
		return todo.Todo{}, store.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return todo.Todo{}, err
	}
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	vc := items.Lookup(id)
	if vc == nil || !vc.IsContainer() {
		return todo.Todo{}, store.ErrNotFound
	}
	m := *vc.AsLoroMap()
	for k, v := range patch {
		if err := m.InsertAny(k, v); err != nil {
			return todo.Todo{}, fmt.Errorf("crdtstore: patch %s: %w", k, err)
		}
	}
	if err := m.InsertAny("updated", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return todo.Todo{}, err
	}
	if err := s.persistRecords(ownerID, d); err != nil {
		return todo.Todo{}, err
	}
	//nolint:contextcheck
	s.publishOpFromDoc(context.Background(), ownerID, "update-"+id, d)
	t, ok := findItem(d, id)
	if !ok {
		return todo.Todo{}, store.ErrNotFound
	}
	return t, nil
}

// Delete removes the todo owned by ownerID. Idempotent: second delete
// returns ErrNotFound (caller may ignore).
func (s *CRDTStore) Delete(_ context.Context, ownerID, id string) error {
	if ownerID == "" || id == "" {
		return store.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return err
	}
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	if v := items.Lookup(id); v == nil {
		return store.ErrNotFound
	}
	if err := items.Delete(id); err != nil {
		return fmt.Errorf("crdtstore: delete: %w", err)
	}
	if err := s.persistRecords(ownerID, d); err != nil {
		return err
	}
	//nolint:contextcheck
	s.publishOpFromDoc(context.Background(), ownerID, "delete-"+id, d)
	return nil
}

// ClearCompleted removes every completed todo owned by ownerID.
// Returns the count deleted.
func (s *CRDTStore) ClearCompleted(_ context.Context, ownerID string) (int, error) {
	if ownerID == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return 0, err
	}
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	// Collect IDs to delete (don't mutate during iteration).
	var toDelete []string
	for id, vc := range items.All() {
		if vc == nil || !vc.IsContainer() {
			continue
		}
		m := *vc.AsLoroMap()
		if done, _ := m.GetBool(fieldCompleted); done {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		if err := items.Delete(id); err != nil {
			return len(toDelete), fmt.Errorf("crdtstore: delete %s: %w", id, err)
		}
	}
	if len(toDelete) > 0 {
		if err := s.persistRecords(ownerID, d); err != nil {
			return len(toDelete), err
		}
		//nolint:contextcheck
		s.publishOpFromDoc(context.Background(), ownerID, "clear-completed", d)
	}
	return len(toDelete), nil
}

// Count returns the total number of todos owned by ownerID. O(n) scan
// (LoroMap.All returns a Go 1.23 range-over-func iterator; no
// built-in size accessor).
func (s *CRDTStore) Count(_ context.Context, ownerID string) (int, error) {
	if ownerID == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.doc(ownerID)
	if err != nil {
		return 0, err
	}
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	n := 0
	for range items.All() {
		n++
	}
	return n, nil
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

// writeItem writes a todo.Todo's fields into a fresh LoroMap child
// of the items map. The caller is responsible for creating the child
// via InsertMapContainer and passing it in.
func writeItem(m *loro.LoroMap, t todo.Todo) error {
	if err := m.InsertAny("id", t.ID); err != nil {
		return err
	}
	if err := m.InsertAny(fieldTitle, t.Title); err != nil {
		return err
	}
	if err := m.InsertAny(fieldCompleted, t.Completed); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if t.CreatedAt.IsZero() {
		if err := m.InsertAny("created", now); err != nil {
			return err
		}
	} else {
		if err := m.InsertAny("created", t.CreatedAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if t.UpdatedAt.IsZero() {
		if err := m.InsertAny("updated", now); err != nil {
			return err
		}
	} else {
		if err := m.InsertAny("updated", t.UpdatedAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

// findItem returns the todo with the given id and whether it was found.
func findItem(d *loro.LoroDoc, id string) (todo.Todo, bool) {
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	vc := items.Lookup(id)
	if vc == nil || !vc.IsContainer() {
		return todo.Todo{}, false
	}
	m := *vc.AsLoroMap()
	return todoFromLoro(id, m), true
}

// readAll returns every todo in the owner's doc as a slice.
func readAll(d *loro.LoroDoc) []todo.Todo {
	items := d.GetMap(loro.AsContainerId(itemsContainerName))
	out := make([]todo.Todo, 0)
	for id, vc := range items.All() {
		if vc == nil || !vc.IsContainer() {
			continue
		}
		m := *vc.AsLoroMap()
		out = append(out, todoFromLoro(id, m))
	}
	return out
}

// todoFromLoro decodes one item LoroMap into a todo.Todo. Missing
// timestamps parse to the zero time (callers can detect via IsZero).
func todoFromLoro(id string, m *loro.LoroMap) todo.Todo {
	title, _ := m.GetString(fieldTitle)
	completed, _ := m.GetBool(fieldCompleted)
	createdStr, hasCreated := m.GetString("created")
	updatedStr, hasUpdated := m.GetString("updated")
	created, _ := time.Parse(time.RFC3339, createdStr)
	updated, _ := time.Parse(time.RFC3339, updatedStr)
	if !hasCreated {
		created = time.Time{}
	}
	if !hasUpdated {
		updated = time.Time{}
	}
	return todo.Todo{
		ID:        id,
		Title:     title,
		Completed: completed,
		CreatedAt: created,
		UpdatedAt: updated,
	}
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
