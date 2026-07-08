package router

import (
	"net/http"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-template/config"
	"github.com/calionauta/gogogo-template/features/auth"
	"github.com/calionauta/gogogo-template/features/todo/handlers"
	"github.com/calionauta/gogogo-template/internal/queue"
	"github.com/calionauta/gogogo-template/web/resources"
)

// WorkflowRuntime is a marker for the Turbine runtime. The router only
// checks for non-nil to decide whether to wire onboarding routes; the
// type assertion to *workflow.Runtime happens in the build-tag-gated
// onboarding file. Keeping this as a marker (no methods) means default
// builds don't need to construct anything that satisfies it.
type WorkflowRuntime interface {
	// Marker method — implemented by *workflow.Runtime.
	IsWorkflowRuntime()
}

// Init registers custom routes on PocketBase's serve event.
// Call before pb.Start(). Pass workflowRt = nil if Turbine is not
// enabled (default builds without `-tags turbine`). Pass todoH as the
// same handler instance the caller used for RegisterHandlers so the
// routes and the SSE worker path share state.
func Init(
	app *pocketbase.PocketBase,
	q *queue.Queue,
	cfg *config.Config,
	workflowRt WorkflowRuntime,
	todoH *handlers.TodoHandler,
) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.GET("/health", func(c *core.RequestEvent) error {
			return c.String(200, "ok")
		})

		se.Router.GET("/static/*", func(c *core.RequestEvent) error {
			fs := http.StripPrefix("/static/", http.FileServer(resources.StaticFS()))
			fs.ServeHTTP(c.Response, c.Request)
			return nil
		})

		// Auth: login/logout/cookie middleware. Wires the demo login
		// page and ensures every request has e.Auth populated from the
		// pb_auth cookie before reaching feature handlers.
		auth.CookieSecure = !cfg.Dev && cfg.Host != "127.0.0.1"
		auth.RegisterAuth(app)

		// Register example feature: Todo MVC. Use the same handler
		// instance the caller registered for background jobs so route
		// state (PocketBase app ref, queue ref, config) is consistent
		// across HTTP and worker paths.
		if todoH != nil {
			// Wire the realtime broadcaster so todo mutations are shared
			// with every connected client (JetStream with -tags jetstream,
			// in-memory fan-out otherwise).
			todoH.SetBroadcaster(newTodoBroadcaster(q.Hub()))
			todoH.RegisterRoutes(se)
		} else {
			// Defensive fallback: construct a fresh handler if the
			// caller forgot to pass one. Lets the rest of the router
			// keep working even in misconfigured test setups.
			fallback := handlers.New(app, q, cfg)
			fallback.RegisterRoutes(se)
		}

		// Onboarding workflow routes are wired here when Turbine is
		// enabled. The handler reads the concrete *workflow.Runtime via
		// RegisterOnboardingRoutes' build-tag switch.
		registerOnboarding(app, q, se, workflowRt)

		return se.Next()
	})
}
