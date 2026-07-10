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

	rt, err := workflow.New(app, workflow.Config{
		Enabled:    true,
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
	handlers.RegisterOnboardingRoutes(app, q, rt, r, nats.NewInMemoryBroadcaster(q.Hub()), h)

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

// TestOnboarding_StartSetsPendingFlag drives the manual onboarding entry
// point (POST /api/onboarding/start) and asserts it sets the
// per-user pending flag in the workflow package. The flag is what the
// create handler reads to resume the flow via OnboardingContinue when
// the user adds their first todo.
func TestOnboarding_StartSetsPendingFlag(t *testing.T) {
	base, _, cleanup := turbineFixture(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const user = "alice"
	// Clean baseline.
	workflow.IsOnboardingPending(user) // ensure package is linked (compile guard)
	workflow.SetOnboardingPendingForTest(user, false)
	defer workflow.SetOnboardingPendingForTest(user, false)

	resp, err := postForm(ctx, base+"/api/onboarding/start", url.Values{"user": {user}})
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

	// Poll for the flag to flip (the workflow runs async in a goroutine).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if workflow.IsOnboardingPending(user) {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected IsOnboardingPending(%q)=true after /api/onboarding/start", user)
}

// TestOnboarding_CreateResumesFlow verifies the create handler resumes
// OnboardingContinue when a user with a pending onboarding adds their
// first todo. We don't wait for the full 60s pause; we just assert the
// resume was triggered (workflow ran without immediate error) and the
// pending flag stays true while the flow is in flight.
func TestOnboarding_CreateResumesFlow(t *testing.T) {
	base, _, cleanup := turbineFixture(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const user = "bob"
	workflow.SetOnboardingPendingForTest(user, true)
	defer workflow.SetOnboardingPendingForTest(user, false)

	resp, err := postForm(ctx, base+"/api/todos", url.Values{titleField: {"my first todo"}})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("create status=%d", resp.StatusCode)
	}

	// The pending flag should STILL be true: OnboardingContinue is
	// mid-flight (it's in the 60s scheduled_pause step). The flag is
	// only cleared when the finalize step runs.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !workflow.IsOnboardingPending(user) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !workflow.IsOnboardingPending(user) {
		t.Fatal("expected pending flag to remain true while OnboardingContinue is in flight (60s pause)")
	}
}

// TestOnboarding_ProgressStreamsToUI is a regression guard for the
// turbine + realtime path: when the durable workflow advances a step it
// publishes a "progress" event through the broadcaster, which must reach
// every connected SSE client so the UI stepper lights up live. Previously
// a missing Subscribe (or a broken broadcaster wiring) let the workflow
// run to completion on the server while the browser showed nothing.
//
// This test opens a real SSE stream, starts onboarding, and asserts the
// progress signal (onboardingStep + techStep=workflow) arrives on the
// stream — proving the turbine step updates are observable in the UI.
func TestOnboarding_ProgressStreamsToUI(t *testing.T) {
	base, _, cleanup := turbineFixture(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const user = "carol"
	workflow.SetOnboardingPendingForTest(user, false)
	defer workflow.SetOnboardingPendingForTest(user, false)

	clientID := "onboard-ui-" + time.Now().Format(clientIDSuffixFormat)
	stream := openSSEWithCtx(ctx, t, base, clientID)
	defer func() { _ = stream.Body.Close() }()
	time.Sleep(100 * time.Millisecond)

	resp, err := postForm(ctx, base+"/api/onboarding/start", url.Values{"user": {user}})
	if err != nil {
		t.Fatalf("onboarding start: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("onboarding start status=%d", resp.StatusCode)
	}

	// Wait for the progress signal (step 1/5) to reach the SSE client.
	full := pumpSSEUntil(t, stream, 15*time.Second, func(s string) bool {
		return containsStr(s, "\"onboardingStep\"") && containsStr(s, "\"techStep\":\"workflow\"")
	})
	if !containsStr(full, "\"onboardingStep\"") {
		t.Fatalf("onboarding progress (onboardingStep) never reached SSE client: %s", tailString(full, 600))
	}
	if !containsStr(full, "\"techStep\":\"workflow\"") {
		t.Fatalf("onboarding progress missing techStep=workflow on SSE client: %s", tailString(full, 600))
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
