//go:build !turbine

package main

import (
	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/pocketbase/pocketbase"
)

func startTurbine(app *pocketbase.PocketBase, cfg *config.Config) {
	// Turbine not available without -tags turbine
	_ = app
	_ = cfg
}

func shutdownTurbine() {}

// getTurbineRuntime returns nil on non-turbine builds. The router
// receives nil and skips wiring onboarding routes.
func getTurbineRuntime() any { return nil }
