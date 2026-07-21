// SCOPE:layer=infra,removal=core — REMOVE if you don't need PocketBase-backed storage
//
// PBStore is the default EntityStore implementation: it persists each
// entity as a PocketBase record in the corresponding collection. Idempotent
// offline replay is handled by the OnRecordCreateRequest hook installed in
// db/RegisterIdempotencyHook + the (idem_key, owner) unique index added by
// db/enableTodosIdempotency. Without those (see db/idempotency_seed.go),
// concurrent or replayed creates will land duplicates.
//
// Trade-off: PocketBase generates record IDs server-side. Replays of a
// queued POST land a NEW record unless dedup is in place. PBStore relies
// on the hook+index dedup layer; without it, every queued create becomes
// a duplicate after the Service Worker replays.
//
// Why this exists: PBStore is one of two EntityStore implementations
// behind features/store/store.go. The other is CRDTStore (future),
// for the collaborative multi-user case where Loro CRDT + JetStream
// give better merge semantics. PBStore is the simple, single-user,
// SQL-queryable default. Switch via config.EntityStore; the HTTP handlers
// don't know the difference.
package pbstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
)

// PBStore is the PocketBase-backed implementation of EntityStore[*todo.Todo].
type PBStore struct {
	app      core.App
	collName string
}

// New constructs a PBStore backed by the named collection. The
// collection must have: id (PB-default), title (text), completed
// (bool), owner (relation to users), and idem_key (text, optional).
// Created by db/seed.go (ensureTodosCollection + enableTodosIdempotency).
func New(app core.App, collectionName string) *PBStore {
	return &PBStore{app: app, collName: collectionName}
}

// Create persists a new todo. ownerID scopes it to the user; idemKey
// enables offline-replay dedup via the OnRecordCreateRequest hook.
func (s *PBStore) Create(_ context.Context, e todo.Todo, ownerID, idemKey string) (todo.Todo, error) {
	col, err := s.app.FindCollectionByNameOrId(s.collName)
	if err != nil {
		return todo.Todo{}, fmt.Errorf("pbstore: find collection %q: %w", s.collName, err)
	}
	rec := core.NewRecord(col)
	rec.Set("title", e.Title)
	rec.Set("completed", e.Completed)
	if ownerID != "" {
		rec.Set("owner", ownerID)
	}
	// Persist idemKey so the OnRecordCreateRequest idempotency hook can
	// dedupe replayed offline writes via the (idem_key, owner) index.
	if idemKey != "" {
		rec.Set("idem_key", idemKey)
	}
	if err := s.app.Save(rec); err != nil {
		return todo.Todo{}, fmt.Errorf("pbstore: save: %w", err)
	}
	return fromRecord(rec), nil
}

// Get returns the todo owned by ownerID. Cross-owner reads return
// ErrNotFound rather than Forbidden (don't leak existence).
func (s *PBStore) Get(_ context.Context, ownerID, id string) (todo.Todo, error) {
	rec, err := s.app.FindRecordById(s.collName, id)
	if err != nil {
		return todo.Todo{}, store.ErrNotFound
	}
	if ownerID != "" && rec.GetString("owner") != "" && rec.GetString("owner") != ownerID {
		return todo.Todo{}, store.ErrNotFound
	}
	return fromRecord(rec), nil
}

// List returns todos owned by ownerID, filtered by the supplied
// filter value ("" for all, "active" for !completed, "completed" for
// completed). Sorted newest-first.
func (s *PBStore) List(_ context.Context, ownerID, filter string) ([]todo.Todo, error) {
	var filterExpr string
	switch filter {
	case "active":
		filterExpr = "completed=false"
	case "completed":
		filterExpr = "completed=true"
	}
	if ownerID != "" {
		ownerFilter := fmt.Sprintf("owner = %q", ownerID)
		if filterExpr == "" {
			filterExpr = ownerFilter
		} else {
			filterExpr = filterExpr + " && " + ownerFilter
		}
	}
	records, err := s.app.FindRecordsByFilter(s.collName, filterExpr, "-created", 0, 0)
	if err != nil {
		return nil, fmt.Errorf("pbstore: list (filter=%q): %w", filter, err)
	}
	res := make([]todo.Todo, len(records))
	for i, r := range records {
		res[i] = fromRecord(r)
	}
	return res, nil
}

// Update applies a field-level patch to the todo owned by ownerID.
// Supports the keys: "title", "completed". Unknown keys are ignored
// silently (the strategy doesn't know the entity schema; the caller
// decides which fields to patch).
func (s *PBStore) Update(_ context.Context, ownerID, id string, patch map[string]any) (todo.Todo, error) {
	rec, err := s.app.FindRecordById(s.collName, id)
	if err != nil {
		return todo.Todo{}, store.ErrNotFound
	}
	if ownerID != "" && rec.GetString("owner") != "" && rec.GetString("owner") != ownerID {
		return todo.Todo{}, store.ErrNotFound
	}
	for k, v := range patch {
		rec.Set(k, v)
	}
	if err := s.app.Save(rec); err != nil {
		return todo.Todo{}, fmt.Errorf("pbstore: update: %w", err)
	}
	return fromRecord(rec), nil
}

// Delete removes the todo. Idempotent on retry (second delete on a
// missing id returns ErrNotFound, which the caller may ignore).
func (s *PBStore) Delete(_ context.Context, ownerID, id string) error {
	rec, err := s.app.FindRecordById(s.collName, id)
	if err != nil {
		return store.ErrNotFound
	}
	if ownerID != "" && rec.GetString("owner") != "" && rec.GetString("owner") != ownerID {
		return store.ErrNotFound
	}
	if err := s.app.Delete(rec); err != nil {
		return fmt.Errorf("pbstore: delete: %w", err)
	}
	return nil
}

// ClearCompleted deletes every completed todo owned by ownerID.
// Returns the number deleted. Uses a single bulk query + iterate;
// for very large completed sets this could be batched, but the demo's
// expected todo count (<200 per user) doesn't need it.
func (s *PBStore) ClearCompleted(_ context.Context, ownerID string) (int, error) {
	filterExpr := "completed=true"
	if ownerID != "" {
		filterExpr = filterExpr + " && " + fmt.Sprintf("owner = %q", ownerID)
	}
	records, err := s.app.FindRecordsByFilter(s.collName, filterExpr, "", 0, 0)
	if err != nil {
		return 0, fmt.Errorf("pbstore: clear find: %w", err)
	}
	count := 0
	for _, r := range records {
		if delErr := s.app.Delete(r); delErr != nil {
			return count, fmt.Errorf("pbstore: clear delete %s: %w", r.Id, delErr)
		}
		count++
	}
	return count, nil
}

// Count returns the total number of todos owned by ownerID. Cheap
// (count query) so it's safe on hot paths.
func (s *PBStore) Count(_ context.Context, ownerID string) (int, error) {
	var filterExpr string
	if ownerID != "" {
		filterExpr = fmt.Sprintf("owner = %q", ownerID)
	}
	records, err := s.app.FindRecordsByFilter(s.collName, filterExpr, "", 0, 0)
	if err != nil {
		return 0, fmt.Errorf("pbstore: count: %w", err)
	}
	return len(records), nil
}

// fromRecord is the private adapter PB ↔ domain Todo. The Strategy
// interface is the public contract; this is internal plumbing so the
// PB schema (column names, relation IDs, Date types) doesn't leak
// into the todo package.
func fromRecord(r *core.Record) todo.Todo {
	return todo.Todo{
		ID:        r.Id,
		Title:     r.GetString("title"),
		Completed: r.GetBool("completed"),
		CreatedAt: r.GetDateTime("created").Time(),
		UpdatedAt: r.GetDateTime("updated").Time(),
	}
}

// Compile-time guard: PBStore must satisfy EntityStore[*todo.Todo]. If
// the interface drifts, the build fails here rather than at the call
// site in router.Init.
var _ store.EntityStore[todo.Todo] = (*PBStore)(nil)

// errors alias kept for clarity at call sites (avoid importing both
// the standard library errors and store just to read "not found").
var _ = errors.Is
