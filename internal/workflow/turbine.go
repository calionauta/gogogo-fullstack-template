//go:build turbine

// Package workflow wires Turbine durable workflows into the binary.
//
// Turbine (https://turbine.yakir.io) embeds a PocketBase app to persist
// workflow state in SQLite. We use the standalone constructor so workflow
// state lives in its own data dir and does not contend with the main
// PocketBase database. Build with `-tags turbine` to enable.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/YakirOren/turbine"
)

// TodoCreator is the interface a workflow step uses to write todos to the
// main app's PocketBase database. Production code registers a real
// implementation via RegisterTodoCreator before the runtime starts;
// tests can inject a stub. The workflow runtime itself does not touch
// the main app DB — it only persists its own step results.
type TodoCreator interface {
	CreateExampleTodo(ctx context.Context, title string) (id string, err error)
}

var (
	creatorMu sync.RWMutex
	creator   TodoCreator
)

// RegisterTodoCreator wires a TodoCreator implementation into the package.
// Must be called before New() so workflows registered after this point can
// reach the main app DB. Passing nil clears the registration (used by tests).
func RegisterTodoCreator(c TodoCreator) {
	creatorMu.Lock()
	defer creatorMu.Unlock()
	creator = c
}

func getCreator() TodoCreator {
	creatorMu.RLock()
	defer creatorMu.RUnlock()
	return creator
}

// ErrTodoCreatorNotRegistered is returned by workflows that need to write
// to the main app DB but find no TodoCreator registered. This guards
// against a workflow firing during test setup before the production
// creator is wired.
var ErrTodoCreatorNotRegistered = errors.New("workflow: no TodoCreator registered")

// Runtime is a thin handle around a Turbine runtime. It is safe to use
// zero-value before Start has been called; the embedded runtime handles
// all synchronization internally.
type Runtime struct {
	t *turbine.Runtime
}

// IsWorkflowRuntime is a marker that lets the router package accept a
// *Runtime via its WorkflowRuntime interface without importing this
// package at the type level. Keeps the build-tag boundary clean.
func (r *Runtime) IsWorkflowRuntime() {}

// T returns the underlying Turbine runtime. Exposed for tests that need
// to call turbine.Run against already-registered workflows. Production
// code should not need this — workflows are triggered via HTTP handlers
// or scheduled jobs, not directly.
func (r *Runtime) T() *turbine.Runtime { return r.t }

// Hello is the minimal example workflow. Each step's result is recorded
// in SQLite and replayed on recovery after a crash, so the function may
// be called many times per workflow run without re-executing side effects.
func Hello(ctx turbine.Context, name string) (string, error) {
	greeting, err := turbine.Do(ctx, func(ctx context.Context) (string, error) {
		return fmt.Sprintf("hello, %s", name), nil
	}, turbine.WithStepName("greet"))
	if err != nil {
		return "", err
	}

	return turbine.Do(ctx, func(ctx context.Context) (string, error) {
		return greeting + " (workflow step recorded)", nil
	}, turbine.WithStepName("finalize"))
}

// ExampleTodo is the data shape produced by the WelcomeOnboarding workflow.
// Each step's output is durable; on crash recovery, recorded steps replay
// their saved ExampleTodo instead of re-running the side effect.
type ExampleTodo struct {
	Title string `json:"title"`
	ID    string `json:"id"`
}

// WelcomeOnboarding is the canonical demonstration of Turbine in this
// template. It models the classic onboarding flow:
//
//  1. Greet the user (pure step, no side effects).
//  2. Create three example todos in the main app DB via TodoCreator.
//     Each todo creation is a separate durable step, so if the server
//     crashes after creating 2 of 3, recovery resumes at step 2 and
//     only creates the third.
//  3. Finalize: returns the list of created todo IDs so the HTTP
//     handler can show a toast / redirect.
//
// Why this is the right demo for Turbine in a todo app:
//   - The side effects (DB writes) are exactly the kind of operation
//     you want to make durable — re-running them would create duplicate
//     todos.
//   - The number of steps is small (5 total) but each is meaningful,
//     keeping the example scannable.
//   - It surfaces the recovery semantics: kill the server mid-run,
//     restart, watch it resume at the last incomplete step.
//
// To trigger from a handler:
//
//	handle, _ := turbine.Run(rt.T(), workflow.WelcomeOnboarding, "alice")
//	ids, _ := handle.GetResult() // []ExampleTodo
//
// The signature is `func(turbine.Context, T) (R, error)` because Turbine
// dispatches workflows as plain functions. The input type is the user's
// name (string); the output is the list of created ExampleTodo.
func WelcomeOnboarding(ctx turbine.Context, user string) ([]ExampleTodo, error) {
	greeting, err := turbine.Do(ctx, func(ctx context.Context) (string, error) {
		return fmt.Sprintf("Welcome, %s!", user), nil
	}, turbine.WithStepName("greet"))
	if err != nil {
		return nil, err
	}
	_ = greeting // could be surfaced via a progress channel; omitted for brevity

	titles := []string{
		"Read the cali-go-stack README",
		"Explore the Turbine workflow",
		"Build something with the stack",
	}

	todos := make([]ExampleTodo, 0, len(titles))
	for i, title := range titles {
		// Each loop iteration is a separate durable step. If the server
		// crashes between iterations, recovery resumes at the first
		// incomplete step (the recorded results for prior iterations
		// are replayed from SQLite).
		var created ExampleTodo
		created, err = turbine.Do(ctx, func(ctx context.Context) (ExampleTodo, error) {
			c := getCreator()
			if c == nil {
				return ExampleTodo{}, ErrTodoCreatorNotRegistered
			}
			id, err := c.CreateExampleTodo(ctx, title)
			if err != nil {
				return ExampleTodo{}, fmt.Errorf("create example todo %q: %w", title, err)
			}
			return ExampleTodo{Title: title, ID: id}, nil
		}, turbine.WithStepName(fmt.Sprintf("create_example_todo_%d", i+1)))
		if err != nil {
			return nil, err
		}
		todos = append(todos, created)
	}

	finalized, err := turbine.Do(ctx, func(ctx context.Context) ([]ExampleTodo, error) {
		return todos, nil
	}, turbine.WithStepName("finalize"))
	if err != nil {
		return nil, err
	}
	return finalized, nil
}

// Config is the subset of the application config that workflow construction
// needs. Defined here so tests can construct one without importing the full
// config package (which transitively loads the secrets package).
type Config struct {
	Enabled    bool
	DataDir    string
	ExecutorID string
}

// New creates a Turbine runtime configured to store workflow state under
// cfg.DataDir. The runtime is launched asynchronously by Start; callers must
// invoke Shutdown to drain pending workflows on exit.
func New(cfg Config, logger *slog.Logger) (*Runtime, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("workflow: WORKFLOW_ENABLED must be true to construct runtime")
	}

	tcfg := turbine.Config{
		ExecutorID:         cfg.ExecutorID,
		ApplicationVersion: "cali-go-stack",
		Logger:             logger,
	}

	// NewStandalone owns a fresh PocketBase app so workflow state is
	// isolated from the main app DB. It also bootstraps the app and runs
	// migrations on Launch, then resets state on Shutdown.
	rt := turbine.NewStandalone(tcfg)

	turbine.Register(rt, Hello)
	turbine.Register(rt, WelcomeOnboarding)

	return &Runtime{t: rt}, nil
}

// Start launches the runtime. It creates the pt_* collections on first run,
// recovers any pending workflows from a previous process, and starts the
// queue runner and cron scheduler.
func (r *Runtime) Start() error {
	return r.t.Launch()
}

// Shutdown drains pending workflows (up to the configured ShutdownTimeout)
// and resets the embedded PocketBase bootstrap state.
func (r *Runtime) Shutdown() {
	r.t.Shutdown()
}
