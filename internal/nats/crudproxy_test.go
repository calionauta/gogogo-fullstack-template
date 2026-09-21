package nats_test

import (
	"os"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/internal/nats"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// TestCrudConsumerCreate validates the end-to-end NATS CRUD proxy path:
// 1. Start embedded NATS with JetStream
// 2. Create CrudPublisher + CrudConsumer (server side)
// 3. Start the consumer in a goroutine
// 4. Publish a "create" operation via the publisher
// 5. Assert the record was created in PocketBase
//
// This guards the cross-instance sync path: desktop edges publish
// CRUD ops to their local NATS, Leaf Node replicates to the server,
// and the consumer writes them to the server's PocketBase.
func TestCrudConsumerCreate(t *testing.T) {
	func() { _ = nats.StartEmbedded(t.TempDir()) }()
	defer nats.Stop()
	js := nats.JetStream()
	if js == nil {
		t.Fatal("JetStream not available after StartEmbedded")
	}

	// Bootstrap a minimal PocketBase with the todos collection.
	tmpDir, err := os.MkdirTemp("", "crud-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       tmpDir,
		DefaultEncryptionEnv: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if bootErr := app.Bootstrap(); bootErr != nil {
		t.Fatalf("Bootstrap: %v", bootErr)
	}
	defer func() { _ = app.ClearBootstrap() }()

	// Create the todos collection (same schema as production).
	col := core.NewBaseCollection("todos")
	col.Fields.Add(
		&core.TextField{Name: "title"},
		&core.BoolField{Name: "completed"},
		&core.TextField{Name: "owner"},
	)
	if saveErr := app.Save(col); saveErr != nil {
		t.Fatalf("create todos collection: %v", saveErr)
	}

	// Create publisher + consumer.
	pub := nats.NewCrudPublisher(js)
	if pub == nil {
		t.Fatal("NewCrudPublisher returned nil")
	}
	consumer := nats.NewCrudConsumer(app, js, "gogogo-fullstack-template")
	ctx := t.Context()
	go func() {
		if runErr := consumer.Run(ctx); runErr != nil {
			t.Logf("consumer Run exited: %v", runErr)
		}
	}()

	// Give consumer time to subscribe to the stream.
	time.Sleep(200 * time.Millisecond)

	// Publish a "create" operation.
	pub.Publish(nats.CrudOpCreate, "user-test", &nats.CrudOpData{
		ID: "test123abcd1234", Title: "NATS CRUD test", Completed: false,
	})

	// Wait for consumer to process.
	time.Sleep(500 * time.Millisecond)

	// Assert the record exists in PocketBase.
	rec, err := app.FindRecordById("todos", "test123abcd1234")
	if err != nil {
		t.Fatalf("FindRecordById: %v — record was not created by consumer", err)
	}
	if rec.GetString("title") != "NATS CRUD test" {
		t.Fatalf("title = %q, want %q", rec.GetString("title"), "NATS CRUD test")
	}
	if rec.GetBool("completed") {
		t.Fatal("completed should be false")
	}
	if rec.GetString("owner") != "user-test" {
		t.Fatalf("owner = %q, want %q", rec.GetString("owner"), "user-test")
	}
}

// TestCrudConsumerToggle verifies that publishing a toggle operation
// via NATS updates an existing PocketBase record.
func TestCrudConsumerToggle(t *testing.T) {
	func() { _ = nats.StartEmbedded(t.TempDir()) }()
	defer nats.Stop()
	js := nats.JetStream()

	tmpDir, err := os.MkdirTemp("", "crud-toggle-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       tmpDir,
		DefaultEncryptionEnv: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if bootErr := app.Bootstrap(); bootErr != nil {
		t.Fatalf("Bootstrap: %v", bootErr)
	}
	defer func() { _ = app.ClearBootstrap() }()

	col := core.NewBaseCollection("todos")
	col.Fields.Add(&core.TextField{Name: "title"}, &core.BoolField{Name: "completed"}, &core.TextField{Name: "owner"})
	_ = app.Save(col)

	// Seed a record.
	rec := core.NewRecord(col)
	rec.Id = "toggle123abc123"
	rec.Set("title", "Toggle me")
	rec.Set("completed", false)
	rec.Set("owner", "user-test")
	if seedErr := app.Save(rec); seedErr != nil {
		t.Fatalf("seed record: %v", seedErr)
	}

	pub := nats.NewCrudPublisher(js)
	consumer := nats.NewCrudConsumer(app, js, "gogogo-fullstack-template")
	ctx := t.Context()
	go func() { _ = consumer.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	// Publish toggle.
	pub.Publish(nats.CrudOpToggle, "user-test", &nats.CrudOpData{
		ID: "toggle123abc123", Completed: true,
	})
	time.Sleep(500 * time.Millisecond)

	updated, err := app.FindRecordById("todos", "toggle123abc123")
	if err != nil {
		t.Fatalf("FindRecordById: %v", err)
	}
	if !updated.GetBool("completed") {
		t.Fatal("todo was not toggled by consumer")
	}
}

// TestCrudConsumerDelete verifies delete operations via NATS.
func TestCrudConsumerDelete(t *testing.T) {
	func() { _ = nats.StartEmbedded(t.TempDir()) }()
	defer nats.Stop()
	js := nats.JetStream()

	tmpDir, err := os.MkdirTemp("", "crud-delete-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       tmpDir,
		DefaultEncryptionEnv: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if bootErr := app.Bootstrap(); bootErr != nil {
		t.Fatalf("Bootstrap: %v", bootErr)
	}
	defer func() { _ = app.ClearBootstrap() }()

	col := core.NewBaseCollection("todos")
	col.Fields.Add(&core.TextField{Name: "title"}, &core.BoolField{Name: "completed"}, &core.TextField{Name: "owner"})
	_ = app.Save(col)

	rec := core.NewRecord(col)
	rec.Id = "delete123abc123"
	rec.Set("title", "Delete me")
	rec.Set("completed", false)
	rec.Set("owner", "user-test")
	_ = app.Save(rec)

	pub := nats.NewCrudPublisher(js)
	consumer := nats.NewCrudConsumer(app, js, "gogogo-fullstack-template")
	ctx := t.Context()
	go func() { _ = consumer.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	pub.Publish(nats.CrudOpDelete, "user-test", &nats.CrudOpData{ID: "delete123abc123"})
	time.Sleep(500 * time.Millisecond)

	if _, err := app.FindRecordById("todos", "delete123abc123"); err == nil {
		t.Fatal("record was not deleted by consumer")
	}
}

// TestCrudConsumerClearCompleted verifies clear_completed via NATS.
func TestCrudConsumerClearCompleted(t *testing.T) {
	func() { _ = nats.StartEmbedded(t.TempDir()) }()
	defer nats.Stop()
	js := nats.JetStream()

	tmpDir, err := os.MkdirTemp("", "crud-clear-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       tmpDir,
		DefaultEncryptionEnv: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if bootErr := app.Bootstrap(); bootErr != nil {
		t.Fatalf("Bootstrap: %v", bootErr)
	}
	defer func() { _ = app.ClearBootstrap() }()

	col := core.NewBaseCollection("todos")
	col.Fields.Add(&core.TextField{Name: "title"}, &core.BoolField{Name: "completed"}, &core.TextField{Name: "owner"})
	_ = app.Save(col)

	// Seed one completed + one active todo.
	for _, seed := range []struct {
		id, title string
		done      bool
	}{
		{"done11111111111", "Done 1", true},
		{"active111111111", "Active 1", false},
	} {
		r := core.NewRecord(col)
		r.Id = seed.id
		r.Set("title", seed.title)
		r.Set("completed", seed.done)
		r.Set("owner", "user-test")
		_ = app.Save(r)
	}

	pub := nats.NewCrudPublisher(js)
	consumer := nats.NewCrudConsumer(app, js, "gogogo-fullstack-template")
	ctx := t.Context()
	go func() { _ = consumer.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	pub.Publish(nats.CrudOpClearCompleted, "user-test", nil)
	time.Sleep(500 * time.Millisecond)

	// done1 should be deleted.
	if _, err := app.FindRecordById("todos", "done11111111111"); err == nil {
		t.Fatal("completed todo was not cleared by consumer")
	}
	// active1 should still exist.
	if _, err := app.FindRecordById("todos", "active111111111"); err != nil {
		t.Fatal("active todo was incorrectly deleted by consumer")
	}
}

// TestCrudPublisherNilSafe verifies that calling methods on a nil
// *CrudPublisher does not panic.
func TestCrudPublisherNilSafe(_ *testing.T) {
	var pub *nats.CrudPublisher // nil
	// Must not panic.
	pub.Publish(nats.CrudOpCreate, "user", &nats.CrudOpData{ID: "safe", Title: "safe"})
	pub.Close()
}

// TestNewCrudPublisherReturnsNil verifies that NewCrudPublisher
// returns nil when js is nil.
func TestNewCrudPublisherReturnsNil(t *testing.T) {
	pub := nats.NewCrudPublisher(nil)
	if pub != nil {
		t.Fatal("NewCrudPublisher(nil) should return nil")
	}
}
