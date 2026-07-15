// SCOPE:pluggable - E2E test for CRDTStore.
//
// Boots a temp PocketBase app (like db/idempotency_hook_test.go),
// runs EnsureSchema, then exercises the 7 EntityStore methods
// against the CRDTStore. Asserts:
//   - the snapshot collection is created
//   - Create returns the persisted entity; Get retrieves it
//   - Update flips `completed` and bumps `updated`
//   - Delete returns ErrNotFound on a second call
//   - List with "active" / "completed" filters correctly
//   - ClearCompleted removes only completed items
//   - Count matches List length
//   - snapshot round-trips: write → load from a fresh CRDTStore
//     → state matches (this is the offline-replay scenario)
package crdtstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"

	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
)

// newTestApp is a local copy of the same pattern db/seed_test.go uses
// (boots a fresh PocketBase app on a temp dir with the same ncruces
// driver as production). Duplicated here rather than exported from db
// to keep the test fixture local; if a third package needs it,
// promote to internal/testutil.
func newTestApp(t *testing.T, tmpDir string) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: tmpDir,
		DBConnect: func(dbPath string) (*dbx.DB, error) {
			pragmas := "?_pragma=busy_timeout(10000)" +
				"&_pragma=journal_mode(WAL)" +
				"&_pragma=foreign_keys(ON)"
			return dbx.Open("sqlite3", dbPath+pragmas)
		},
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return app
}

func newCRDTStore(t *testing.T) (*CRDTStore, *pocketbase.PocketBase, func()) {
	t.Helper()
	tmpDir, mkErr := os.MkdirTemp("", "crdtstore-*")
	if mkErr != nil {
		t.Fatal(mkErr)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	app := newTestApp(t, tmpDir)
	s := New(app)
	if schemaErr := s.EnsureSchema(); schemaErr != nil {
		cleanup()
		t.Fatalf("EnsureSchema: %v", schemaErr)
	}
	return s, app, cleanup
}

func TestCRDTStore_EnsureSchemaCreatesCollection(t *testing.T) {
	s, _, cleanup := newCRDTStore(t)
	defer cleanup()

	col, err := s.app.FindCollectionByNameOrId(snapshotCollectionName)
	if err != nil {
		t.Fatalf("snapshot collection missing after EnsureSchema: %v", err)
	}
	if col.Fields.GetByName("owner") == nil {
		t.Error("owner field missing")
	}
	if col.Fields.GetByName("snapshot") == nil {
		t.Error("snapshot field missing")
	}
	if col.Fields.GetByName("version") == nil {
		t.Error("version field missing")
	}
}

func TestCRDTStore_CreateGetListUpdateDelete(t *testing.T) {
	s, _, cleanup := newCRDTStore(t)
	defer cleanup()
	ctx := context.Background()
	owner := "user-crud-1"

	// Create: client-generated ID (UUID); store fills timestamps.
	id := "11111111-2222-3333-4444-555555555555"
	in := todo.Todo{ID: id, Title: "first", Completed: false}
	out, err := s.Create(ctx, in, owner, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ID != id || out.Title != "first" || out.Completed {
		t.Errorf("Create returned %+v, want id+title+!completed", out)
	}
	if out.CreatedAt.IsZero() {
		t.Error("Create did not set CreatedAt")
	}
	if out.UpdatedAt.IsZero() {
		t.Error("Create did not set UpdatedAt")
	}

	// Get: round-trip.
	got, err := s.Get(ctx, owner, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "first" {
		t.Errorf("Get title = %q, want %q", got.Title, "first")
	}

	// Get cross-owner: ErrNotFound.
	if _, getErr := s.Get(ctx, "other-owner", id); !errors.Is(getErr, store.ErrNotFound) {
		t.Errorf("cross-owner Get err = %v, want ErrNotFound", getErr)
	}

	// Update: toggle completed. The new UpdatedAt must be strictly
	// later than the original — we don't compare wall clock strings
	// directly (RFC3339 sub-second precision can collapse under
	// snapshot round-trips); we just assert the timestamp advanced.
	updated, err := s.Update(ctx, owner, id, map[string]any{"completed": true, "title": "first-edited"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !updated.Completed || updated.Title != "first-edited" {
		t.Errorf("Update returned %+v", updated)
	}
	if !updated.UpdatedAt.After(out.UpdatedAt) {
		t.Errorf("UpdatedAt did not advance: %v vs %v", updated.UpdatedAt, out.UpdatedAt)
	}

	// List: all, active, completed.
	all, err := s.List(ctx, owner, "")
	if err != nil || len(all) != 1 {
		t.Errorf("List all: err=%v len=%d, want 1", err, len(all))
	}
	active, _ := s.List(ctx, owner, "active")
	completed, _ := s.List(ctx, owner, "completed")
	if len(active) != 0 {
		t.Errorf("active filter returned %d, want 0", len(active))
	}
	if len(completed) != 1 {
		t.Errorf("completed filter returned %d, want 1", len(completed))
	}

	// Count matches List length.
	if c, _ := s.Count(ctx, owner); c != 1 {
		t.Errorf("Count = %d, want 1", c)
	}

	// Delete: removes.
	if err := s.Delete(ctx, owner, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, owner, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after Delete, Get err = %v, want ErrNotFound", err)
	}
	// Second delete: ErrNotFound (idempotent path).
	if err := s.Delete(ctx, owner, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
}

func TestCRDTStore_ClearCompleted(t *testing.T) {
	s, _, cleanup := newCRDTStore(t)
	defer cleanup()
	ctx := context.Background()
	owner := "user-clear-1"

	// Create 3: 2 completed, 1 active.
	for i, title := range []string{"a", "b", "c"} {
		id := []string{"id-a", "id-b", "id-c"}[i]
		_, err := s.Create(ctx, todo.Todo{ID: id, Title: title}, owner, "")
		if err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	// Mark a and b completed.
	if _, err := s.Update(ctx, owner, "id-a", map[string]any{"completed": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, owner, "id-b", map[string]any{"completed": true}); err != nil {
		t.Fatal(err)
	}

	n, err := s.ClearCompleted(ctx, owner)
	if err != nil {
		t.Fatalf("ClearCompleted: %v", err)
	}
	if n != 2 {
		t.Errorf("ClearCompleted returned %d, want 2", n)
	}
	all, _ := s.List(ctx, owner, "")
	if len(all) != 1 || all[0].ID != "id-c" {
		t.Errorf("after Clear, list = %+v, want only id-c", all)
	}
}

func TestCRDTStore_SnapshotRoundTrip(t *testing.T) {
	// The CRDTStore persists a Loro snapshot to PB. A fresh CRDTStore
	// reading the same data dir should load the snapshot and see the
	// same entities. This is the offline-replay / restart scenario.
	tmpDir, err := os.MkdirTemp("", "crdtstore-roundtrip-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	app1 := newTestApp(t, tmpDir)
	s1 := New(app1)
	if schemaErr := s1.EnsureSchema(); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	ctx := context.Background()
	owner := "user-roundtrip-1"
	for i, title := range []string{"alpha", "beta", "gamma"} {
		id := []string{"id-alpha", "id-beta", "id-gamma"}[i]
		if _, createErr := s1.Create(ctx, todo.Todo{ID: id, Title: title}, owner, ""); createErr != nil {
			t.Fatal(createErr)
		}
	}
	if _, updateErr := s1.Update(ctx, owner, "id-beta", map[string]any{"completed": true}); updateErr != nil {
		t.Fatal(updateErr)
	}

	// Fresh CRDTStore on the same data dir.
	app2 := newTestApp(t, tmpDir)
	defer func() { _ = app2.ResetBootstrapState() }()
	s2 := New(app2)
	all, err := s2.List(ctx, owner, "")
	if err != nil {
		t.Fatalf("s2.List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("s2.List returned %d items, want 3", len(all))
	}
	gotBeta, _ := s2.Get(ctx, owner, "id-beta")
	if !gotBeta.Completed {
		t.Errorf("s2: id-beta Completed = false, want true (snapshot not restored)")
	}
}

func TestCRDTStore_EmptyOwnerReturnsEmpty(t *testing.T) {
	s, _, cleanup := newCRDTStore(t)
	defer cleanup()
	ctx := context.Background()
	if all, _ := s.List(ctx, "never-touched-owner", ""); len(all) != 0 {
		t.Errorf("List on fresh owner returned %d, want 0", len(all))
	}
	if c, _ := s.Count(ctx, "never-touched-owner"); c != 0 {
		t.Errorf("Count on fresh owner = %d, want 0", c)
	}
}

func TestCRDTStore_WatchSignals(t *testing.T) {
	s, _, _ := newCRDTStore(t)
	ctx := context.Background()
	ownerID := "watch-owner"
	if _, err := s.Create(ctx, todo.Todo{ID: "watch-1", Title: "first"}, ownerID, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ch, cancel := s.Watch(ownerID)
	defer cancel()
	// Watch sends current value immediately.
	select {
	case v := <-ch:
		if v < 1 {
			t.Errorf("initial event v=%d, want >= 1", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive initial event")
	}
	// Next event triggered by a new mutation.
	if _, err := s.Create(ctx, todo.Todo{ID: "watch-2", Title: "second"}, ownerID, ""); err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	select {
	case v := <-ch:
		if v < 2 {
			t.Errorf("second event v=%d, want >= 2", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive second event")
	}
}
