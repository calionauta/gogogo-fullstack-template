//go:build !turbine

package handlers

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/calionauta/cali-go-stack/internal/queue"
)

// RegisterOnboardingRoutes is a no-op when the binary is built without
// the `turbine` build tag. Production builds without Turbine never see
// this route, and tests that don't need workflows skip the registration.
func RegisterOnboardingRoutes(_ *pocketbase.PocketBase, _ *queue.Queue, _ *router.Router[*core.RequestEvent]) {
	// Turbine not available without -tags turbine
}
