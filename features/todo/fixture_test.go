package todo_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/calionauta/cali-go-stack/config"
	"github.com/calionauta/cali-go-stack/features/todo/handlers"
	"github.com/calionauta/cali-go-stack/internal/queue"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// testFixture spins up a real PocketBase + goqite stack on temp dirs
// and serves the todo routes via httptest.NewServer. Mirrors the
// production wiring from db.Init + queue.New + router.Init +
// handlers.New.
//
// Per cali-coding-go-standards: temp-dir PB + Bootstrap + real SQLite
// (no mocks). Returns the base URL for HTTP calls, the queue (for
// direct assertions), the PB app (for collection queries), and a
// cleanup func that callers MUST defer.
func testFixture(t *testing.T) (string, *queue.Queue, *pocketbase.PocketBase, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "todo-int-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	cfg := &config.Config{
		Host:          "127.0.0.1",
		Port:          0,
		Dev:           true,
		DataDir:       tmpDir,
		DBPath:        tmpDir + "/app.db",
		EncryptionKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}

	app := newPocketBaseApp(cfg)
	if bootErr := app.Bootstrap(); bootErr != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Bootstrap: %v", bootErr)
	}

	if collErr := createTodosCollection(app); collErr != nil {
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("create todos collection: %v", collErr)
	}

	q, err := queue.New(cfg)
	if err != nil {
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("queue.New: %v", err)
	}

	h := handlers.New(app, q, cfg)
	h.RegisterHandlers(q.Registry())

	workers := q.StartWorkers()

	r := router.NewRouter[*core.RequestEvent](newRequestEventFactory(app))
	h.RegisterRoutesOn(r)

	mux, err := r.BuildMux()
	if err != nil {
		workers.Stop()
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("BuildMux: %v", err)
	}

	server := httptest.NewServer(mux)
	cleanup := func() {
		server.Close()
		workers.Stop()
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
	}
	return server.URL, q, app, cleanup
}

// mustReset rolls back PocketBase bootstrap state on test failure.
// Best-effort: errors are logged, never fatal, since the caller is
// already in a cleanup/teardown or fatal path.
func mustReset(t *testing.T, app core.App) {
	t.Helper()
	if err := app.ResetBootstrapState(); err != nil {
		t.Logf("ResetBootstrapState: %v", err)
	}
}

// newPocketBaseApp mirrors the production wiring from db.Init: same
// pragmas, same config shape. Kept in one place so future changes
// (e.g. WAL tuning) propagate to tests automatically.
func newPocketBaseApp(cfg *config.Config) *pocketbase.PocketBase {
	return pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       cfg.DataDir,
		DefaultEncryptionEnv: cfg.EncryptionKey,
		DBConnect: func(dbPath string) (*dbx.DB, error) {
			pragmas := "?_pragma=busy_timeout(10000)" +
				"&_pragma=journal_mode(WAL)" +
				"&_pragma=journal_size_limit(200000000)" +
				"&_pragma=synchronous(NORMAL)" +
				"&_pragma=foreign_keys(ON)" +
				"&_pragma=temp_store(MEMORY)" +
				"&_pragma=cache_size(-32000)"
			return dbx.Open("sqlite3", dbPath+pragmas)
		},
	})
}

// createTodosCollection registers the schema the handler expects. Idempotent.
func createTodosCollection(app core.App) error {
	col := core.NewBaseCollection("todos")
	col.Fields.Add(
		&core.TextField{Name: "title"},
		&core.BoolField{Name: "completed"},
		&core.DateField{Name: "created"},
		&core.DateField{Name: "updated"},
	)
	return app.Save(col)
}

// newRequestEventFactory returns the router.EventFactoryFunc used by
// the production router: build a RequestEvent from the recorder-backed
// ResponseWriter and the HTTP request.
func newRequestEventFactory(app core.App) router.EventFactoryFunc[*core.RequestEvent] {
	return func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{
			App: app,
			Event: router.Event{
				Response: w,
				Request:  req,
			},
		}, nil
	}
}

// readBody drains and returns an HTTP response body as a string.
// Callers may safely ignore resp.Body.Close afterward.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf)
}
