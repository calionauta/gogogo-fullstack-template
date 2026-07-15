package crdtstore

// SCOPE:pluggable - REMOVE if you don't need CRDT-backed collaborative storage.
//
// CRDTStore is the second EntityStore strategy: it persists each owner's
// todos in a single Loro CRDT document and snapshots the resolved state
// to PocketBase (collection todos_crdt_snapshot) for durability. The
// strategy wins for the multi-user / multi-device use case because Loro
// CRDT ops merge automatically — two devices editing the same todo
// offline converge without data loss, no LWW.
//
// Trade-off vs PBStore:
//
//   - ✅ Auto-merge of concurrent edits (CRDT magic).
//   - ✅ Offline-first by construction: ops replay converges.
//   - ✅ JetStream MsgId dedup replaces our `idem_key` field when
//     cross-instance sync is enabled (Phase 2 — not in MVP).
//   - ❌ No SQL queries: List/filter is a full-doc scan over the LoroMap.
//   - ❌ PB realtime becomes doc-version-bumped events, not per-record.
//   - ❌ PB admin UI can't edit records directly (CRDT state is opaque).
//   - ❌ Migration from PBStore requires a one-shot converter.
//
// MVP scope (v0.20.0): per-owner in-memory LoroDoc + PB snapshot
// persistence + a single-process lock. Cross-instance JetStream op
// transport is a follow-up (see docs/decisions.md v0.20.0 ADR Phase 2).
//
// Why the in-house Loro wrapper duplicates a little of
// internal/collab/collab.go: the collab.Doc is whiteboard-specific
// (ApplyShapeOp hardcoded). CRDTStore operates on a LoroMap per
// owner with todo-shaped values and needs a different commit
// discipline. If a second generic CRDT consumer appears, extract a
// shared `internal/collab/genericdoc.go`; for now, ~30 LOC of
// duplication beats a leaky abstraction.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// snapshotCollectionName is the PB collection that stores the resolved
// CRDT state per owner. Exported so EnsureSchema (and tests) can
// reference it without duplicating the literal.
const snapshotCollectionName = "todos_crdt_snapshot"

// CRDTStore is the CRDT-backed implementation of EntityStore[todo.Todo].
// One in-memory LoroDoc per owner; persisted as a snapshot blob in
// the todos_crdt_snapshot PocketBase collection.
type CRDTStore struct {
	app core.App

	mu   sync.Mutex
	docs map[string]*loro.LoroDoc // ownerID -> doc (lazy on first access)

	// transport is the cross-instance JetStream op publisher
	// (Phase 2). nil = single-process mode (publish is a no-op).
	transport *CRDTTransport

	// versionMu protects versions + watchers + publisher. Bumped
	// by bumpVersion after every saveSnapshot (both local and
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
// bumpVersion after every saveSnapshot. The router wires this to
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

// SetTransport wires the cross-instance JetStream op publisher
// (Phase 2). Pass nil to disable cross-instance sync (single-process
// mode, default). Call before any request handler runs. The caller
// is responsible for starting the consumer (Subscribe) and for
// running the goroutine that pumps the doc's encoded updates into
// the transport.
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

// EnsureSchema creates the todos_crdt_snapshot collection and the
// owner-unique index if missing. Idempotent.
func (s *CRDTStore) EnsureSchema() error {
	col, err := s.app.FindCollectionByNameOrId(snapshotCollectionName)
	if err != nil {
		col = core.NewBaseCollection(snapshotCollectionName)
		col.Fields.Add(
			&core.TextField{Name: "owner", Max: 100, Required: true},
			&core.TextField{Name: "snapshot"},
			&core.NumberField{Name: "version"},
		)
	}
	if col.Fields.GetByName("owner") == nil {
		return errors.New("crdtstore: owner field missing after create")
	}
	hasOwnerIndex := false
	for _, sql := range col.Indexes {
		if contains(sql, "crdt_snapshot_owner_idx") {
			hasOwnerIndex = true
			break
		}
	}
	if !hasOwnerIndex {
		col.AddIndex("crdt_snapshot_owner_idx", true, "owner", "")
	}
	if err := s.app.Save(col); err != nil {
		return fmt.Errorf("crdtstore: save snapshot collection: %w", err)
	}
	slog.Info("crdtstore: ensured todos_crdt_snapshot collection")
	return nil
}

// doc returns the LoroDoc for ownerID, lazily creating it (and
// loading any persisted snapshot). Caller must hold s.mu if multi-op.
func (s *CRDTStore) doc(ownerID string) (*loro.LoroDoc, error) {
	if d, ok := s.docs[ownerID]; ok {
		return d, nil
	}
	d := loro.NewLoroDoc()
	snap, ok, err := s.loadSnapshot(ownerID)
	if err != nil {
		return nil, fmt.Errorf("crdtstore: load snapshot for %s: %w", ownerID, err)
	}
	if ok {
		if _, iErr := d.Import(snap); iErr != nil {
			return nil, fmt.Errorf("crdtstore: import snapshot for %s: %w", ownerID, iErr)
		}
	}
	s.docs[ownerID] = d
	return d, nil
}

// saveSnapshot persists the current resolved doc state for ownerID.
// Called after every mutating op so a crash never loses more than the
// in-flight op. Also bumps the version counter for any caller (local
// or remote). The Phase 3 SSE broadcaster subscribes to this counter.
func (s *CRDTStore) saveSnapshot(ownerID string, d *loro.LoroDoc) error {
	s.bumpVersion(ownerID)
	snap, err := d.Export(loro.SnapshotMode())
	if err != nil {
		return fmt.Errorf("crdtstore: export snapshot: %w", err)
	}
	col, cErr := s.app.FindCollectionByNameOrId(snapshotCollectionName)
	if cErr != nil {
		return fmt.Errorf("crdtstore: find snapshot collection: %w", cErr)
	}
	rec, fErr := s.app.FindFirstRecordByFilter(snapshotCollectionName, "owner = {:o}", map[string]any{"o": ownerID})
	if fErr != nil || rec == nil {
		rec = core.NewRecord(col)
		rec.Set("owner", ownerID)
		rec.Set("version", 1)
	} else {
		rec.Set("version", rec.GetInt("version")+1)
	}
	rec.Set("snapshot", string(snap))
	if sErr := s.app.Save(rec); sErr != nil {
		return fmt.Errorf("crdtstore: save snapshot: %w", sErr)
	}
	return nil
}

// loadSnapshot returns the persisted snapshot bytes for ownerID and
// whether one was found. A "no rows" result is reported as (nil,
// false, nil) — not an error — so the caller can lazily create a
// fresh Loro doc on first access without surfacing a PB-internal
// "no rows in result set" message to the user.
func (s *CRDTStore) loadSnapshot(ownerID string) ([]byte, bool, error) {
	rec, err := s.app.FindFirstRecordByFilter(snapshotCollectionName, "owner = {:o}", map[string]any{"o": ownerID})
	if err != nil {
		// PocketBase returns a "no rows in result set" error from
		// FindFirstRecordByFilter when the filter matches nothing.
		// Treat that as a cache miss, not a real failure.
		if err.Error() == "sql: no rows in result set" {
			return nil, false, nil
		}
		return nil, false, err
	}
	if rec == nil {
		return nil, false, nil
	}
	return []byte(rec.GetString("snapshot")), true, nil
}

// Create inserts a new todo into the owner's doc and persists the
// snapshot. idemKey is currently unused (the CRDT op IDs are
// implicitly idempotent within a doc); Phase 2 will use it for
// JetStream MsgId dedup across instances.
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
	if vc := items.Lookup(e.ID); vc != nil {
		// id already exists; surface a conflict. Phase 2 will use
		// idemKey to merge concurrent creates of the same id.
		return todo.Todo{}, store.ErrNotFound
	}
	child, err := items.InsertMapContainer(e.ID, loro.NewLoroMap())
	if err != nil {
		return todo.Todo{}, fmt.Errorf("crdtstore: insert map: %w", err)
	}
	if err := writeItem(child, e); err != nil {
		return todo.Todo{}, fmt.Errorf("crdtstore: write item: %w", err)
	}
	if err := s.saveSnapshot(ownerID, d); err != nil {
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
	listFilterActive    = "active"
	listFilterCompleted = "completed"
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
		case listFilterCompleted:
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
	if err := s.saveSnapshot(ownerID, d); err != nil {
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
	if err := s.saveSnapshot(ownerID, d); err != nil {
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
		if done, _ := m.GetBool("completed"); done {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		if err := items.Delete(id); err != nil {
			return len(toDelete), fmt.Errorf("crdtstore: delete %s: %w", id, err)
		}
	}
	if len(toDelete) > 0 {
		if err := s.saveSnapshot(ownerID, d); err != nil {
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
	if err := s.saveSnapshot(ownerID, d); err != nil {
		return fmt.Errorf("crdtstore ApplyRemoteOp: save snapshot: %w", err)
	}
	// Emit a "doc version bumped" event so the UI can re-fetch
	// (Phase 3: SSE-based realtime for CRDTStore). For now we just
	// bump a version counter that the handler can poll.
	s.bumpVersion(ownerID)
	slog.Debug("crdtstore: applied remote op", "owner", ownerID, "op", op.ID, "publisher", op.PublisherID)
	return nil
}

// bumpVersion increments the in-memory version counter for an owner
// and (Phase 3) fans out the new version to subscribers via Watch
// AND to the optional publisher. The counter is the ground-truth
// for catch-up reads on reconnect; the channel + publisher are
// the live notification paths.
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
// Tests + (Phase 3) the SSE broadcast use this to detect a "doc
// version bumped" event.
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
	if err := m.InsertAny("title", t.Title); err != nil {
		return err
	}
	if err := m.InsertAny("completed", t.Completed); err != nil {
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
	title, _ := m.GetString("title")
	completed, _ := m.GetBool("completed")
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

// contains is a substring helper. Could use strings.Contains but this
// keeps the file dependency-free at the cost of one tiny helper.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// Compile-time guard: CRDTStore must satisfy EntityStore[todo.Todo].
// Adding a method here without implementing it would now be a compile
// error instead of a runtime panic.
var _ store.EntityStore[todo.Todo] = (*CRDTStore)(nil)
