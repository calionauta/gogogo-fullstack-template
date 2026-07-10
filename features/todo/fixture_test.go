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

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/db"
	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/handlers"
	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Shared test fixtures. Lifted out of inline literals so golangci-lint
// goconst doesn't fire and so the source of truth is one place.
const (
	titleField   = "title"
	demoEmail    = "demo@demo.app"
	demoPassword = "demo1234456"
)

func testFixture(t *testing.T) (string, *queue.Queue, *pocketbase.PocketBase, *handlers.TodoHandler, func()) {
	return buildFixture(t, nil)
}

// testFixtureSimulated is like testFixture but wires an in-process
// simulated LLM client, so the /api/todos/suggest-simulated route is
// live and the full queue + retry + SSE path can be exercised keyless.
func testFixtureSimulated(t *testing.T) (string, *queue.Queue, *pocketbase.PocketBase, *handlers.TodoHandler, func()) {
	return buildFixture(t, llm.NewSimulated())
}

// buildFixture spins up a real PocketBase + goqite stack on temp dirs
// and serves the todo routes via httptest.NewServer. Mirrors the
// production wiring from db.Init + queue.New + router.Init +
// handlers.New. When simClient is non-nil it is wired as the simulated
// LLM so the suggest-simulated route is live (and Closed on cleanup).
func buildFixture(t *testing.T, simClient *llm.Client) (
	string, *queue.Queue, *pocketbase.PocketBase, *handlers.TodoHandler, func(),
) {
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
	if simClient != nil {
		h.SetSimulatedLLMClient(simClient)
	}
	// Mirror router.Init: share the SSE hub via the in-memory broadcaster
	// so todo mutations fan out to every connected client (the realtime
	// path the demo relies on). Without this, broadcastTodo is a no-op
	// and cross-tab sync silently fails.
	h.SetBroadcaster(nats.NewInMemoryBroadcaster(q.Hub()))

	workers := q.StartWorkers()

	// Seed the demo user so auth login has a target. testFixture is
	// shared by feature tests; existing todo tests don't exercise
	// auth, so this is purely additive. We call both the OnServe
	// binding (matches production wiring) AND run the demo-user
	// seed inline — OnServe doesn't fire in tests, so the user
	// would never be seeded.
	if seedErr := db.SeedDefaults(app); seedErr != nil {
		t.Fatalf("testFixture: SeedDefaults: %v", seedErr)
	}
	if err = seedDemoUserInline(app); err != nil {
		t.Fatalf("testFixture: seedDemoUserInline: %v", err)
	}

	r := router.NewRouter[*core.RequestEvent](newRequestEventFactory(app))

	// Bind the auth cookie middleware BEFORE routes so every route
	// (todo + auth) populates c.Auth from the gogogo_auth cookie.
	auth.CookieSecure = false
	r.BindFunc(auth.LoadAuthFromCookie)

	// Wire todo + auth routes onto the same router. Order matters:
	// todo first, then auth /login + /logout.
	h.RegisterRoutesOn(r)
	r.GET("/login", auth.RedirectIfAuthed).BindFunc(auth.HandleLoginGetForTest)
	r.POST("/login", auth.HandlePasswordLogin)
	r.POST("/logout", auth.HandleLogout)

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
		if simClient != nil {
			simClient.Close()
		}
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
	}
	return server.URL, q, app, h, cleanup
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
			return dbx.Open("sqlite3", "file:"+dbPath+pragmas)
		},
	})
}

// createTodosCollection registers the schema the handler expects. Idempotent.
func createTodosCollection(app core.App) error {
	col := core.NewBaseCollection("todos")
	col.Fields.Add(
		&core.TextField{Name: titleField},
		&core.BoolField{Name: "completed"},
		&core.DateField{Name: "created"},
		&core.DateField{Name: "updated"},
		// owner carries the demo user id so workflow-created todos are
		// tenant-scoped (mirrors the production collection).
		&core.TextField{Name: "owner"},
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

// seedDemoUserInline runs the demo-user seed synchronously so tests
// can log in immediately. In production the same seed runs via
// db.SeedDefaults → OnServe.bindFunc → ensureDemoUser; the test
// path bypasses OnServe because the test app's OnServe is never invoked,
// so we call the seed function directly. Mirrors db.ensureDemoUser
// (kept local to avoid an import cycle through db_test).
func seedDemoUserInline(app core.App) error {
	col, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return err
	}
	email := demoEmail
	password := demoPassword

	if existing, err := app.FindAuthRecordByEmail(col.Name, email); err == nil && existing != nil {
		existing.SetPassword(password)
		return app.Save(existing)
	}

	record := core.NewRecord(col)
	record.SetEmail(email)
	record.SetPassword(password)
	return app.Save(record)
}
