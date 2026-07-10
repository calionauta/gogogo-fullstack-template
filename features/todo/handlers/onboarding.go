//go:build turbine

package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/YakirOren/turbine"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/internal/datastar"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	"github.com/calionauta/gogogo-fullstack-template/internal/workflow"
)

// publishOnboardingProgress streams a "progress" event through the
// broadcaster so every connected client sees the durable workflow
// advance step by step — Turbine's durability and JetStream's realtime
// delivery, observed together in the UI stepper.
func publishOnboardingProgress(b nats.TodoBroadcaster, ctx context.Context, step, total int, phase, detail string) {
	if b == nil {
		return
	}
	p := mustJSON(map[string]any{
		"step":   step,
		"total":  total,
		"phase":  phase,
		"detail": detail,
	})
	envelope := mustJSON(queue.Job{Type: "progress", Payload: p})
	if err := b.PublishTodoUpdate(ctx, envelope); err != nil {
		slog.Warn("onboarding: publish progress failed", "error", err)
	}
}

// RegisterOnboardingRoutes wires the onboarding HTTP routes into the
// router and registers the PocketBase-backed TodoCreator so the workflow
// can write to the main app DB. Build-tag gated: only compiled when the
// binary is built with `-tags turbine`.
//
// It also registers a login hook: every successful password login fires
// OnboardingStart for THAT user (scoped by PocketBase record id), so each
// browser session gets its own onboarding instance — not a global broadcast.
// The flow then waits for the user to create their first todo; the create
// handler resumes it via OnboardingContinue (see resumeOnboardingIfPending).
func RegisterOnboardingRoutes(
	app *pocketbase.PocketBase,
	q *queue.Queue,
	rt *workflow.Runtime,
	r *router.Router[*core.RequestEvent],
	broadcaster nats.TodoBroadcaster,
	todoH *TodoHandler,
) {
	h := &OnboardingHandler{app: app, q: q, rt: rt, broadcaster: broadcaster}
	// Link the onboarding handler into TodoHandler so the create path
	// can resume the flow when a user with a pending onboarding adds a
	// todo (see ResumeOnboarding).
	todoH.onboarding = h

	// Manual "Start onboarding" button (original entry point).
	if r != nil {
		// LoadAppAuth populates c.Auth from the demo user's gogogo_auth
		// cookie (the global LoadAuthFromCookie skips /api/), so the
		// workflow scopes the created todos to the logged-in user's
		// tenant instead of falling back to an anonymous "friend".
		r.POST("/api/onboarding/start", h.handleStart).BindFunc(auth.LoadAppAuth)
	}

	// Automatic entry point: fire OnboardingStart on every successful
	// password login. The app uses a custom cookie auth (not PocketBase
	// native), so we register a callback the auth package invokes after a
	// successful login. Scoped to the logged-in user id.
	auth.SetOnLoginHook(func(userID string) {
		triggerOnboardingStart(h, userID)
	})
}

// triggerOnboardingStart fires the first half of the event-driven
// onboarding flow for a specific user. Safe to call from the login path
// (runs the durable workflow in a goroutine, non-blocking).
func triggerOnboardingStart(h *OnboardingHandler, user string) {
	if user == "" {
		return
	}
	publishOnboardingProgress(h.broadcaster, context.Background(), 1, 5, "greet", "Step 1/5 — Greeting user")
	go func() {
		handle, err := turbine.Run(h.rt.T(), workflow.OnboardingStart, user)
		if err != nil {
			slog.Error("onboarding: start failed", "user", user, "error", err)
			return
		}
		// Poll until the (short) first half completes, then announce the
		// "awaiting your first todo" state so the UI shows step 2/5.
		for {
			_, err := handle.GetResult()
			if err == nil {
				publishOnboardingProgress(h.broadcaster, context.Background(), 2, 5, "await_todo", "Step 2/5 — Awaiting your first todo")
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
}

// ResumeOnboarding implements OnboardingResumer. It fires the second
// half of the event-driven onboarding flow (OnboardingContinue) when a
// user with a pending onboarding creates their first todo. Called from
// the create handler via the TodoHandler.onboarding interface.
func (h *OnboardingHandler) ResumeOnboarding(user string) {
	resumeOnboardingIfPending(h, user)
}

// resumeOnboardingIfPending fires the second half of the event-driven
// onboarding flow (OnboardingContinue) when a user with a pending
// onboarding creates their first todo. No-op if the user has no pending
// onboarding.
func resumeOnboardingIfPending(h *OnboardingHandler, user string) {
	if user == "" || !workflow.IsOnboardingPending(user) {
		return
	}
	// Step 3/5 immediately (todo captured), then the workflow paces the
	// rest (scheduled pause + finalize) and broadcasts progress itself.
	publishOnboardingProgress(h.broadcaster, context.Background(), 3, 5, "todo_captured", "Step 3/5 — First todo captured")
	go func() {
		handle, err := turbine.Run(h.rt.T(), workflow.OnboardingContinue, user)
		if err != nil {
			slog.Error("onboarding: continue failed", "user", user, "error", err)
			return
		}
		// Steps 4 (scheduled pause) and 5 (finalize) are announced as the
		// durable workflow advances; the finalize step also emits the
		// workflow-completed alert via the creator/broadcaster.
		for {
			_, err := handle.GetResult()
			if err == nil {
				publishOnboardingProgress(h.broadcaster, context.Background(), 5, 5, "finalize", "Step 5/5 — Onboarding complete")
				if h.broadcaster != nil {
					_ = h.broadcaster.PublishTodoUpdate(context.Background(), todoUpdateJob("workflow-completed", "remote", "", "", false))
				}
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
}

// OnboardingHandler exposes the WelcomeOnboarding workflow over HTTP.
// The workflow runs asynchronously inside Turbine, so the HTTP response
// is just a "started" toast + a "started" signal. The todo list will
// grow as each step completes (each step writes a todo via TodoCreator).
type OnboardingHandler struct {
	app *pocketbase.PocketBase
	q   *queue.Queue
	rt  *workflow.Runtime
	// broadcaster is used to refresh every client's list once the
	// workflow finishes.
	broadcaster nats.TodoBroadcaster
}

func (h *OnboardingHandler) handleStart(c *core.RequestEvent) error {
	if err := c.Request.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form")
	}
	user := c.Request.FormValue("user")
	if user == "" && c.Auth != nil {
		// Default to the authenticated user who clicked the button so the
		// example todos are scoped to their tenant and actually appear
		// in their list. The "user" form value (e.g. a scanned
		// profile) still wins when present.
		user = c.Auth.Id
	}
	if user == "" {
		user = "friend"
	}

	// Reset the per-user step counter and announce step 1 so the UI
	// stepper lights up the moment the workflow starts.
	publishOnboardingProgress(h.broadcaster, context.Background(), 1, 5, "greet", "Step 1/5 — Greeting user")

	// Fire the first half of the event-driven onboarding flow. It ends
	// at "awaiting your first todo"; the create handler resumes the
	// second half (OnboardingContinue) when the user adds a todo.
	go func() {
		handle, err := turbine.Run(h.rt.T(), workflow.OnboardingStart, user)
		if err != nil {
			slog.Error("onboarding: workflow start failed", "user", user, "error", err)
			// Surface the failure to the user instead of failing silently.
			if h.broadcaster != nil {
				_ = h.broadcaster.PublishTodoUpdate(context.Background(), todoUpdateJob("workflow-error", "remote", "", err.Error(), false))
			}
			return
		}
		for {
			_, err := handle.GetResult()
			if err == nil {
				publishOnboardingProgress(h.broadcaster, context.Background(), 2, 5, "await_todo", "Step 2/5 — Awaiting your first todo")
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
