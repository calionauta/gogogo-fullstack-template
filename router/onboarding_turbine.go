//go:build turbine

package router

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/cali-go-stack/features/todo/handlers"
	"github.com/calionauta/cali-go-stack/internal/queue"
	"github.com/calionauta/cali-go-stack/internal/workflow"
)

// registerOnboarding wires the WelcomeOnboarding workflow into the
// PocketBase router. Called from Init when Turbine is enabled.
func registerOnboarding(app *pocketbase.PocketBase, q *queue.Queue, se *core.ServeEvent, rt WorkflowRuntime) {
	if rt == nil {
		return
	}
	concrete, ok := rt.(*workflow.Runtime)
	if !ok {
		return
	}
	handlers.RegisterOnboardingRoutes(app, q, concrete, se.Router)
}
