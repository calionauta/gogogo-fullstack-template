//go:build turbine

package main

import (
	"log"

	"github.com/pocketbase/pocketbase"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/internal/workflow"
)

var turbineRuntime *workflow.Runtime

func startTurbine(app *pocketbase.PocketBase, cfg *config.Config) {
	if !cfg.Workflow.Enabled {
		return
	}
	// workflow.New wires Launch into app.OnServe, so the runtime boots after
	// the main app is bootstrapped and migrations have run. We do NOT call
	// rt.Start() here — that would double-launch (the OnServe hook already
	// launches it). Tests that never call app.Start() call Start() manually
	// after Bootstrapping the app.
	rt, err := workflow.New(app, workflow.Config{
		Enabled:    true,
		ExecutorID: cfg.Workflow.ExecutorID,
	}, nil)
	if err != nil {
		log.Printf("WARN: workflow init failed, durable workflows disabled: %v", err)
		return
	}
	turbineRuntime = rt
}

func shutdownTurbine() {
	if turbineRuntime != nil {
		turbineRuntime.Shutdown()
	}
}

// getTurbineRuntime returns the workflow runtime started by startTurbine,
// or nil if Turbine is disabled. Used by main.go to pass the runtime into
// the router so onboarding routes can be wired up.
func getTurbineRuntime() any {
	if turbineRuntime == nil {
		return nil
	}
	return turbineRuntime
}
