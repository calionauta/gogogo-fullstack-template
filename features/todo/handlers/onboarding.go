//go:build dagnats

package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/internal/dagnats"
	"github.com/calionauta/gogogo-fullstack-template/internal/datastar"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// OnboardingHandler exposes the DagNats onboarding workflow over HTTP.
// The workflow runs asynchronously inside DagNats; the HTTP response is a
// "started" toast + signal, and the handler polls the run status and
// streams progress to every connected client via the broadcaster so the
// UI stepper lights up live — DagNats durability + JetStream realtime
// observed together.
type OnboardingHandler struct {
	app         *pocketbase.PocketBase
	client      *dagnats.Client
	broadcaster nats.TodoBroadcaster

	mu          sync.Mutex
	activeRunID string // the run currently awaiting the first-todo signal
}

// RegisterOnboardingRoutes wires the onboarding HTTP routes into the
// router. Build-tag gated: only compiled with -tags dagnats. The DagNats
// engine must already be running (started in cmd/web/dagnats.go) so the
// client can reach its REST API on baseURL.
func RegisterOnboardingRoutes(
	app *pocketbase.PocketBase,
	_ *queue.Queue,
	baseURL string,
	r *router.Router[*core.RequestEvent],
	broadcaster nats.TodoBroadcaster,
	todoH *TodoHandler,
) {
	if r == nil || broadcaster == nil {
		return
	}
	h := &OnboardingHandler{
		app:         app,
		client:      dagnats.NewClient(baseURL),
		broadcaster: broadcaster,
	}
	// Link into TodoHandler so the create path can reach it via the
	// OnboardingResumer interface and resume the durable run when the
	// user creates their first todo.
	todoH.onboarding = h

	r.POST("/api/onboarding/start", h.handleStart).BindFunc(auth.LoadAppAuth)
}

// ResumeOnboarding signals the paused onboarding run so it resumes
// creating the example todos. Wired from the create path: when a user
// with a pending onboarding adds their first todo, this delivers the
// "first-todo" signal to the blocked WaitForSignal step. No-op if no
// run is currently awaiting the signal.
func (h *OnboardingHandler) ResumeOnboarding(_ string) {
	h.mu.Lock()
	runID := h.activeRunID
	h.mu.Unlock()
	if runID == "" {
		slog.Debug("onboarding: resume called but no active run")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.client.Signal(ctx, runID, "first-todo", []byte(`{"resumed":true}`)); err != nil {
		slog.Warn("onboarding: signal first-todo failed", "run", runID, "error", err)
		if h.broadcaster != nil {
			_ = h.broadcaster.PublishTodoUpdate(ctx, todoUpdateJob("workflow-error", "remote", "", "resume failed: "+err.Error(), false))
		}
		return
	}
	slog.Info("onboarding: signalled first-todo", "run", runID)
	// Surface to the UI that creating the todo resumed the durable run,
	// so the user understands the event-driven link (step 2 → step 3).
	if h.broadcaster != nil {
		_ = h.broadcaster.PublishTodoUpdate(ctx, todoUpdateJob("workflow-resumed", "remote", "", "First todo captured — workflow resuming", false))
	}
}

// publishProgress streams a "progress" event through the broadcaster so
// every connected client sees the durable workflow advance step by step.
func (h *OnboardingHandler) publishProgress(ctx context.Context, step, total int, phase, detail string) {
	if h.broadcaster == nil {
		return
	}
	p := mustJSON(map[string]any{
		"step":   step,
		"total":  total,
		"phase":  phase,
		"detail": detail,
	})
	envelope := mustJSON(queue.Job{Type: "progress", Payload: p})
	if err := h.broadcaster.PublishTodoUpdate(ctx, envelope); err != nil {
		slog.Warn("onboarding: publish progress failed", "error", err)
	}
}

func (h *OnboardingHandler) handleStart(c *core.RequestEvent) error {
	if err := c.Request.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form")
	}
	user := c.Request.FormValue("user")
	if user == "" && c.Auth != nil {
		user = c.Auth.Id
	}
	if user == "" {
		user = "friend"
	}

	// Reset the stepper and announce step 1 so the UI lights up the
	// moment the workflow starts.
	h.publishProgress(context.Background(), 1, 6, "greet", "Step 1/6 — Greeting user")

	go func() {
		runID, err := h.client.StartRun(context.Background(), "onboarding", map[string]any{"user": user})
		if err != nil {
			slog.Error("onboarding: start failed", "user", user, "error", err)
			if h.broadcaster != nil {
				_ = h.broadcaster.PublishTodoUpdate(context.Background(),
					todoUpdateJob("workflow-error", "remote", "", err.Error(), false))
			}
			return
		}
		h.mu.Lock()
		h.activeRunID = runID
		h.mu.Unlock()
		h.pollRun(runID)
	}()

	sse := sdk.NewSSE(c.Response, c.Request)
	if err := datastar.MergeSignals(sse, map[string]any{
		"onboardingStarted": true,
		"onboardingUser":    user,
	}); err != nil {
		return err
	}
	return emitToast(sse, "Onboarding started — create a todo to continue", "info")
}

// pollRun watches the DagNats run and streams progress to the UI until
// the run reaches a terminal state, reading DagNats' REST status. It is
// driven by the REAL run state (not a fixed sleep countdown), so the
// stepper reflects what the durable workflow is actually doing — including
// the "waiting for your first todo" suspend, which can last indefinitely
// until the user creates a todo. The button is re-enabled (OnboardingActive
// cleared) on completion, failure, or a hard timeout so the user is never
// trapped in a permanently disabled state.
func (h *OnboardingHandler) pollRun(runID string) {
	ctx := context.Background()
	stepLabels := []string{
		"Greeting user",
		"Waiting for your next to-do",
		"Creating example todo 1/3",
		"Creating example todo 2/3",
		"Creating example todo 3/3",
		"Finalizing onboarding",
	}
	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timeout:
			// Give up: re-enable the button so the user isn't stuck.
			h.publishProgress(ctx, 0, 0, "idle", "Onboarding timed out")
			if h.broadcaster != nil {
				_ = h.broadcaster.PublishTodoUpdate(ctx,
					todoUpdateJob("workflow-timeout", "remote", "", "", false))
			}
			return
		case <-ticker.C:
		}
		st, err := h.client.GetRun(ctx, runID)
		if err != nil {
			// Transient engine error — keep polling unless timed out.
			continue
		}
		total := st.Total
		if total <= 0 {
			total = len(stepLabels)
		}
		switch st.Status {
		case "completed":
			h.publishProgress(ctx, total, total, "finalize", "Step "+itoa(total)+"/"+itoa(total)+" — Onboarding complete")
			if h.broadcaster != nil {
				_ = h.broadcaster.PublishTodoUpdate(ctx,
					todoUpdateJob("workflow-completed", "remote", "", "", false))
			}
			return
		case "failed":
			h.publishProgress(ctx, st.Step, total, "error", "Onboarding failed: "+st.Detail)
			if h.broadcaster != nil {
				_ = h.broadcaster.PublishTodoUpdate(ctx,
					todoUpdateJob("workflow-error", "remote", "", st.Detail, false))
			}
			return
		case "waiting":
			// Suspended on WaitForSignal("first-todo"). Show the prompt
			// and keep polling — it will resume when the user creates a todo.
			h.publishProgress(ctx, st.Step, total, "workflow",
				"Step "+itoa(st.Step)+"/"+itoa(total)+" — Waiting for your next to-do (create one to continue)")
		default:
			label := ""
			if st.Step >= 1 && st.Step <= len(stepLabels) {
				label = stepLabels[st.Step-1]
			} else {
				label = "Working"
			}
			h.publishProgress(ctx, st.Step, total, "workflow",
				"Step "+itoa(st.Step)+"/"+itoa(total)+" — "+label)
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
