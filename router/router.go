package router

import (
	"net/http"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/cali-go-stack/config"
	"github.com/calionauta/cali-go-stack/features/todo/handlers"
	"github.com/calionauta/cali-go-stack/internal/queue"
	"github.com/calionauta/cali-go-stack/web/resources"
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

		// Register example feature: Todo MVC. Use the same handler
		// instance the caller registered for background jobs so route
		// state (PocketBase app ref, queue ref, config) is consistent
		// across HTTP and worker paths.
		if todoH != nil {
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
