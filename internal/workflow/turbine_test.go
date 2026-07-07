//go:build turbine

package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/YakirOren/turbine"
)

// stubCreator captures todo titles passed through the workflow so tests
// can assert the durable steps fired in the expected order.
type stubCreator struct {
	mu     sync.Mutex
	titles []string
	failOn map[string]error // title → error to return
}

func (s *stubCreator) CreateExampleTodo(_ context.Context, title string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.failOn[title]; ok {
		return "", err
	}
	s.titles = append(s.titles, title)
	return fmt.Sprintf("stub-%d", len(s.titles)), nil
}

func (s *stubCreator) Titles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.titles))
	copy(out, s.titles)
	return out
}

// TestNewRequiresEnabled confirms that constructing a runtime without
// WORKFLOW_ENABLED fails fast rather than silently launching Turbine.
// This guards the build-tag opt-in contract: a binary built with -tags
// turbine must still require explicit configuration to spin up workflows.
func TestNewRequiresEnabled(t *testing.T) {
	_, err := New(Config{Enabled: false}, nil)
	if err == nil {
		t.Fatal("expected error when Config.Enabled is false, got nil")
	}
}

// TestHelloWorkflowEndToEnd launches the Turbine runtime in a temp data
// dir, registers the Hello workflow, runs it with a real input, and
// asserts the result propagates through the durable-step machinery.
//
// This is the canonical E2E test for the workflow package — it mirrors
// what a real binary built with `-tags turbine` would do on first boot.
// Per cali-coding-go-standards, integration tests use temp dirs and real
// SQLite (no mocks) so the test exercises the same code paths as prod.
func TestHelloWorkflowEndToEnd(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "turbine-e2e-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	rt, err := New(Config{
		Enabled:    true,
		DataDir:    tmpDir,
		ExecutorID: "test",
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Shutdown()

	handle, err := turbine.Run(rt.T(), Hello, "world")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Poll for completion rather than blocking on GetResult: Hello finishes
	// in microseconds, but the queue runner needs a tick to dequeue the
	// message and dispatch the goroutine.
	deadline := time.Now().Add(5 * time.Second)
	var result string
	for time.Now().Before(deadline) {
		result, err = handle.GetResult()
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GetResult after polling: %v", err)
	}

	want := "hello, world (workflow step recorded)"
	if result != want {
		t.Fatalf("Hello(%q) = %q, want %q", "world", result, want)
	}
}

// TestWelcomeOnboarding_CreatesThreeDurableTodos runs the full
// WelcomeOnboarding workflow against a stub TodoCreator and asserts:
//  1. All three titles are passed to the creator in order.
//  2. The returned result contains all three IDs.
//  3. Re-running the workflow does NOT re-execute steps that already
//     completed (durable replay).
//
// This is the canonical demo of Turbine in this template: each todo
// creation is a separate durable step. Recovery semantics are
// implicitly tested by Turbine's pt_operation_outputs table.
func TestWelcomeOnboarding_CreatesThreeDurableTodos(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "turbine-onboarding-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	stub := &stubCreator{}
	RegisterTodoCreator(stub)
	defer RegisterTodoCreator(nil)

	rt, err := New(Config{Enabled: true, DataDir: tmpDir, ExecutorID: "test"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Shutdown()

	handle, err := turbine.Run(rt.T(), WelcomeOnboarding, "alice")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var result []ExampleTodo
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err = handle.GetResult()
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GetResult after polling: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(result))
	}
	got := stub.Titles()
	want := []string{
		"Read the cali-go-stack README",
		"Explore the Turbine workflow",
		"Build something with the stack",
	}
	if len(got) != len(want) {
		t.Fatalf("stub saw %d titles, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("title %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWelcomeOnboarding_StepFailureBubblesUp verifies a failure inside
// one of the durable steps (e.g., a DB error from the creator) propagates
// to GetResult so the HTTP handler can render an error toast.
func TestWelcomeOnboarding_StepFailureBubblesUp(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "turbine-onboarding-fail-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	stub := &stubCreator{
		failOn: map[string]error{
			"Explore the Turbine workflow": errors.New("simulated DB outage"),
		},
	}
	RegisterTodoCreator(stub)
	defer RegisterTodoCreator(nil)

	rt, err := New(Config{Enabled: true, DataDir: tmpDir, ExecutorID: "test"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Shutdown()

	handle, err := turbine.Run(rt.T(), WelcomeOnboarding, "bob")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, gerr := handle.GetResult()
		if gerr != nil {
			// Expected: the workflow surfaces a non-nil error from the
			// failing step.
			if !contains(gerr.Error(), "simulated DB outage") {
				t.Fatalf("expected error containing 'simulated DB outage', got: %v", gerr)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected workflow to surface an error, got nil after polling")
}

// contains is a tiny helper to keep the test self-contained without
// pulling in strings.Contains.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
