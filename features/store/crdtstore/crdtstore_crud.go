// SCOPE:layer=infra,removal=plugin — EntityStore[todo.Todo] CRUD operations for CRDTStore
package crdtstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aholstenson/loro-go"

	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
)

// listFilter values for CRDTStore.List. Defined as constants so
// golangci-lint's goconst check stays happy (the strings appear in
// the ClearCompleted helper, the Update filter, and the List switch).
const (
	fieldTitle       = "title"
	fieldCompleted   = "completed"
	listFilterActive = "active"
)

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
