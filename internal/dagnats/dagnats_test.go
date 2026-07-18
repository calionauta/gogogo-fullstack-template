package dagnats

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danmestas/dagnats/server"
	"github.com/danmestas/dagnats/worker"
)

// startTestServer boots a real DagNats server in-process on an ephemeral
// NATS port and the given HTTP addr, registers the onboarding workflow,
// and returns a ready Client. It is the same wiring cmd/web uses, so the
// test exercises the real integration contract (REST register + run +
// signal), not a mock.
func startTestServer(t *testing.T, httpAddr, dataDir string) *Client {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	srv := server.New(server.Config{
		DataDir:       dataDir,
		HTTPAddr:      httpAddr,
		NATSPort:      -1, // ephemeral (nats-server: -1 = random port; 0 = no listener)
		MaxStoreBytes: 256 << 20, // 256 MiB. The test workflow is tiny; 1 GiB was a needless reservation. A smaller store boots the engine lighter, which helps when several packages run their engines under -p 1.
	})

	// Register the same task handlers the app uses (names must match the
	// workflow JSON). In the test they are no-ops — we validate the
	// engine contract (greet -> await signal -> create x3 -> finalize),
	// not PocketBase writes (covered by TodoHandler tests).
	shim := server.EmbeddedWorker(srv)
	shim.Handle("onboarding-greet", func(ctx worker.TaskContext) error {
		return ctx.Complete([]byte(`"welcomed"`))
	})
	shim.Handle("onboarding-await-first-todo", func(ctx worker.TaskContext) error {
		_, err := ctx.WaitForSignal("first-todo", 50*time.Minute)
		if err != nil {
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

	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run() }()
	t.Cleanup(func() {
		srv.Stop()
		if err := <-runErr; err != nil {
			t.Logf("dagnats test server stopped: %v", err)
		}
	})

	client := NewClient("http://" + httpAddr)
	// Register the workflow, retrying until the API is up. Under -race
	// the engine boots slower, so allow up to ~20s.
	for range 80 {
		if err := client.RegisterWorkflow(context.Background(), []byte(OnboardingWorkflowJSON)); err == nil {
			return client
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("test server: failed to register onboarding workflow on %s", httpAddr)
	return nil
}

func TestOnboardingWorkflow_RegistersAndRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dagnats-test")
	client := startTestServer(t, "127.0.0.1:18091", dir)
	ctx := context.Background()

	runID, err := client.StartRun(ctx, "onboarding", map[string]any{"user": "tester"})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if runID == "" {
		t.Fatal("empty run id")
	}

	// After the greet step the run must pause at await-first-todo
	// (the WaitForSignal step) and stay running.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st, err := client.GetRun(ctx, runID)
		if err == nil && st.Status == "running" {
			raw, _ := client.GetRunRaw(ctx, runID)
			if stepStatus(raw, "await-first-todo") == "running" &&
				stepStatus(raw, "greet") == "completed" {
				goto paused
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
paused:
	raw, _ := client.GetRunRaw(ctx, runID)
	if stepStatus(raw, "await-first-todo") != "running" {
		t.Fatalf("expected run to pause at await-first-todo, got steps=%v", stepStatuses(raw))
	}

	// Deliver the signal the blocked step is waiting for.
	if err := client.Signal(ctx, runID, "first-todo", []byte(`{"resumed":true}`)); err != nil {
		t.Fatalf("signal: %v", err)
	}

	// Run must now complete with all steps done. Under -race the
	// engine is much slower, so allow up to 30s.
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := client.GetRun(ctx, runID)
		if err == nil && st.Status == "completed" {
			raw, _ := client.GetRunRaw(ctx, runID)
			for _, s := range []string{"greet", "await-first-todo", "todo-1", "todo-2", "todo-3", "finalize"} {
				if stepStatus(raw, s) != "completed" {
					t.Fatalf("step %s not completed: %v", s, stepStatuses(raw))
				}
			}
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("run did not reach completed within timeout")
}

func stepStatus(run map[string]any, id string) string {
	steps, ok := run["steps"].(map[string]any)
	if !ok {
		return ""
	}
	s, ok := steps[id].(map[string]any)
	if !ok {
		return ""
	}
	st, _ := s["status"].(string)
	return st
}

func stepStatuses(run map[string]any) map[string]string {
	out := map[string]string{}
	steps, ok := run["steps"].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range steps {
		if m, ok := v.(map[string]any); ok {
			out[k], _ = m["status"].(string)
		}
	}
	return out
}
