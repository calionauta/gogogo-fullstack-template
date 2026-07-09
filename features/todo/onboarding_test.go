//go:build turbine

package todo_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/handlers"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	"github.com/calionauta/gogogo-fullstack-template/internal/workflow"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// turbineFixture spins up PocketBase + goqite + the Turbine runtime on
// temp dirs and serves the todo AND onboarding routes via httptest.
// Mirrors router.Init with -tags turbine (real SQLite, real Turbine,
// no mocks) so the test exercises the same durable-step path as prod.
func turbineFixture(t *testing.T) (string, *pocketbase.PocketBase, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "todo-onboarding-*")
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

	rt, err := workflow.New(workflow.Config{
		Enabled:    true,
		DataDir:    tmpDir,
		ExecutorID: "test-onboarding",
	}, nil)
	if err != nil {
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("workflow.New: %v", err)
	}
	if startErr := rt.Start(); startErr != nil {
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("workflow.Start: %v", startErr)
	}

	h := handlers.New(app, q, cfg)
	h.RegisterHandlers(q.Registry())
	workers := q.StartWorkers()

	r := router.NewRouter[*core.RequestEvent](newRequestEventFactory(app))
	h.RegisterRoutesOn(r)
	// Wire the Turbine-gated onboarding routes the same way router.Init does.
	handlers.RegisterOnboardingRoutes(app, q, rt, r, nats.NewInMemoryBroadcaster(q.Hub()))

	mux, err := r.BuildMux()
	if err != nil {
		workers.Stop()
		rt.Shutdown()
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("BuildMux: %v", err)
	}

	server := httptest.NewServer(mux)
	cleanup := func() {
		server.Close()
		workers.Stop()
		rt.Shutdown()
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
	}
	return server.URL, app, cleanup
}

// TestOnboarding_StartCreatesThreeTodos drives the full HTTP → Turbine →
// PocketBase path: POST /api/onboarding/start fires WelcomeOnboarding in
// the background, each durable step writes a todo via PocketBaseTodoCreator,
// and we assert all three land in the "todos" collection. Also confirms
// the HTTP response merges the onboardingStarted signal.
func TestOnboarding_StartCreatesThreeTodos(t *testing.T) {
	base, app, cleanup := turbineFixture(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := postForm(ctx, base+"/api/onboarding/start", url.Values{"user": {"alice"}})
	if err != nil {
		t.Fatalf("onboarding start: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("onboarding start status=%d body=%s", resp.StatusCode, body)
	}
	if !containsStr(body, "onboardingStarted") {
		t.Fatalf("response missing onboardingStarted signal: %s", body)
	}

	// Poll the "todos" collection until all three durable steps land.
	want := []string{
		"Read the gogogo-fullstack-template README",
		"Explore the Turbine workflow",
		"Build something with the stack",
	}
	deadline := time.Now().Add(20 * time.Second)
	var titles []string
	var recs []*core.Record
	var ferr error
	for time.Now().Before(deadline) {
		recs, ferr = app.FindRecordsByFilter("todos", "", "", 0, 0)
		if ferr == nil {
			titles = titles[:0]
			for _, r := range recs {
				titles = append(titles, r.GetString(titleField))
			}
			if len(titles) >= 3 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	if len(titles) < 3 {
		t.Fatalf("onboarding created %d todos, want >= 3 (saw %v)", len(titles), titles)
	}
	for _, w := range want {
		if !containsStrSlice(titles, w) {
			t.Errorf("expected todo %q among created todos, got %v", w, titles)
		}
	}
	// Regression guard: durable-workflow todos must be scoped to the
	// demo user via the owner field, not orphaned. handleStart passes
	// c.Auth.Id (or the "user" form value) and PocketBaseTodoCreator
	// sets rec.owner, so every created row must carry that owner.
	for _, r := range recs {
		if got := r.GetString("owner"); got != "alice" {
			t.Errorf("todo %q owner = %q, want alice", r.GetString(titleField), got)
		}
	}
}

// containsStr reports whether substr is inside s (kept local to avoid a
// strings import in the turbine-only build).
func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsStrSlice(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}
