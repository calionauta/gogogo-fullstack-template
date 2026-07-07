//go:build turbine

package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/YakirOren/turbine"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/cali-go-stack/internal/datastar"
	"github.com/calionauta/cali-go-stack/internal/queue"
	"github.com/calionauta/cali-go-stack/internal/workflow"
)

// PocketBaseTodoCreator implements workflow.TodoCreator by writing to the
// main app's "todos" collection. Registered as the package-level creator
// in RegisterOnboardingRoutes so the WelcomeOnboarding workflow can reach
// the main app DB from its durable steps.
type PocketBaseTodoCreator struct {
	app *pocketbase.PocketBase
}

// CreateExampleTodo inserts a new todo with the given title into the main
// app's "todos" collection and returns its PocketBase-generated id.
func (c *PocketBaseTodoCreator) CreateExampleTodo(ctx context.Context, title string) (string, error) {
	col, err := c.app.FindCollectionByNameOrId("todos")
	if err != nil {
		return "", fmt.Errorf("find todos collection: %w", err)
	}
	rec := core.NewRecord(col)
	rec.Set("title", title)
	rec.Set("completed", false)
	if err := c.app.Save(rec); err != nil {
		return "", fmt.Errorf("save todo: %w", err)
	}
	return rec.Id, nil
}

// RegisterOnboardingRoutes wires the onboarding HTTP routes into the
// router and registers the PocketBase-backed TodoCreator so the workflow
// can write to the main app DB. Build-tag gated: only compiled when the
// binary is built with `-tags turbine`.
func RegisterOnboardingRoutes(app *pocketbase.PocketBase, q *queue.Queue, rt *workflow.Runtime, r *router.Router[*core.RequestEvent]) {
	workflow.RegisterTodoCreator(&PocketBaseTodoCreator{app: app})

	if r != nil && rt != nil {
		h := &OnboardingHandler{app: app, q: q, rt: rt}
		r.POST("/api/onboarding/start", h.handleStart)
	}
}

// OnboardingHandler exposes the WelcomeOnboarding workflow over HTTP.
// The workflow runs asynchronously inside Turbine, so the HTTP response
// is just a "started" toast + a "started" signal. The todo list will
// grow as each step completes (each step writes a todo via TodoCreator).
type OnboardingHandler struct {
	app *pocketbase.PocketBase
	q   *queue.Queue
	rt  *workflow.Runtime
}

func (h *OnboardingHandler) handleStart(c *core.RequestEvent) error {
	if err := c.Request.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form")
	}
	user := c.Request.FormValue("user")
	if user == "" {
		user = "friend"
	}

	// Fire the workflow in a goroutine. Turbine persists state in SQLite
	// after each step, so a crash mid-run resumes at the last completed
	// step — no duplicate todos.
	go func() {
		handle, err := turbine.Run(h.rt.T(), workflow.WelcomeOnboarding, user)
		if err != nil {
			slog.Error("onboarding: workflow start failed", "user", user, "error", err)
			return
		}
		// Poll for completion to log the outcome. The actual progress is
		// visible in the todo list as each step's side effect lands.
		for {
			result, err := handle.GetResult()
			if err == nil {
				slog.Info("onboarding: workflow completed",
					"user", user, "todos", len(result))
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()

	sse := sdk.NewSSE(c.Response, c.Request)
	if err := datastar.MergeSignals(sse, map[string]any{
		"onboardingStarted": true,
		"onboardingUser":    user,
	}); err != nil {
		return err
	}
	return emitToast(sse, "Onboarding started — watch the list grow", "info")
}
