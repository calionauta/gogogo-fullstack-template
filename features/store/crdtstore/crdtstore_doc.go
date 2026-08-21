// SCOPE:layer=infra,removal=plugin — Loro doc rehydration + PocketBase record projection for CRDTStore
package crdtstore

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aholstenson/loro-go"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/features/todo"
)

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
