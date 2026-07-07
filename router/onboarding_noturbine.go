//go:build !turbine

package router

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/cali-go-stack/internal/queue"
)

// registerOnboarding is a no-op when Turbine is not enabled. The router
// stays importable from default builds; the onboarding routes simply
// don't exist.
func registerOnboarding(_ *pocketbase.PocketBase, _ *queue.Queue, _ *core.ServeEvent, _ WorkflowRuntime) {
	// Turbine not available without -tags turbine
}
