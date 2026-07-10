//go:build dagnats

package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/danmestas/dagnats/server"
	"github.com/danmestas/dagnats/worker"

	"github.com/calionauta/gogogo-fullstack-template/internal/dagnats"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// TestOnboarding_ResumeSignalsRun is the end-to-end regression guard
// for the app→workflow signal path. It boots a real DagNats
// server, registers the onboarding workflow, starts a run, then
// drives the exact code the create-todo path triggers:
// OnboardingHandler.ResumeOnboarding(user) → client.Signal(first-todo)
// → the blocked WaitForSignal step resumes → run completes.
//
// If someone breaks the signal name ("first-todo"), the
// runID plumbing (activeRunID), or the ResumeOnboarding wiring,
// this test fails instead of hanging silently in production.
func TestOnboarding_ResumeSignalsRun(t *testing.T) {
	hub := queue.NewSSEHub()
	broadcaster := nats.NewInMemoryBroadcaster(hub)

	h := &OnboardingHandler{
		client:      dagnats.NewClient("http://127.0.0.1:18097"),
		broadcaster: broadcaster,
	}

	// Boot a real DagNats server on the conventional NATS port (same
	// wiring cmd/web/dagnats.go uses).
	srv := server.New(server.Config{
		DataDir:       t.TempDir(),
		HTTPAddr:      "127.0.0.1:18097",
		NATSPort:      4222,
		MaxStoreBytes: 1 << 30,
	})

	// Register the same task handlers the real app registers in
	// cmd/web/dagnats.go (names must match OnboardingWorkflowJSON).
	// EmbeddedWorker MUST be called before Run().
	shim := server.EmbeddedWorker(srv)
	shim.Handle("onboarding-greet", func(ctx worker.TaskContext) error {
		return ctx.Complete([]byte(`"welcomed"`))
	})
	shim.Handle("onboarding-await-first-todo", func(ctx worker.TaskContext) error {
		if _, err := ctx.WaitForSignal("first-todo", 50*time.Minute); err != nil {
			return ctx.Fail(err)
		}
		return ctx.Complete([]byte(`"resumed"`))
	})
	shim.Handle("onboarding-create-todo", func(ctx worker.TaskContext) error {
		return ctx.Complete([]byte(`"created"`))
	})
	shim.Handle("onboarding-finalize", func(ctx worker.TaskContext) error {
		return ctx.Complete([]byte(`"done"`))
	})

	go func() { _ = srv.Run() }()
	waitForDagNatsReady(t, "127.0.0.1:18097")

	ctx := context.Background()
	registerOnboardingWorkflow(t, h.client)

	runID, err := h.client.StartRun(ctx, "onboarding", map[string]any{"user": "tester"})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if runID == "" {
		t.Fatal("empty run id")
	}

	// The create path sets h.activeRunID (via handleStart) and then
	// calls ResumeOnboarding on first todo. We mirror that here.
	h.mu.Lock()
	h.activeRunID = runID
	h.mu.Unlock()

	// Simulate the user creating their first todo → resume the run.
	h.ResumeOnboarding("tester")

	// Run must now complete (greet → await-signaled → create x3 → finalize).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := h.client.GetRun(ctx, runID)
		if err == nil && st.Status == "completed" {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("onboarding run did not complete after ResumeOnboarding signal")
}

func waitForDagNatsReady(t *testing.T, httpAddr string) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + httpAddr + "/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("dagnats /ready not reached within timeout")
}

func registerOnboardingWorkflow(t *testing.T, client *dagnats.Client) {
	t.Helper()
	for attempt := 0; attempt < 80; attempt++ {
		if err := client.RegisterWorkflow(context.Background(), []byte(dagnats.OnboardingWorkflowJSON)); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("failed to register onboarding workflow")
}
