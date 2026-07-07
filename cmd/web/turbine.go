//go:build turbine

package main

import (
	"log"

	"github.com/calionauta/cali-go-stack/config"
	"github.com/calionauta/cali-go-stack/internal/workflow"
)

var turbineRuntime *workflow.Runtime

func startTurbine(cfg *config.Config) {
	if !cfg.Workflow.Enabled {
		return
	}
	rt, err := workflow.New(workflow.Config{
		Enabled:    true,
		DataDir:    cfg.Workflow.DataDir,
		ExecutorID: cfg.Workflow.ExecutorID,
	}, nil)
	if err != nil {
		log.Fatalf("workflow init: %v", err)
	}
	if err := rt.Start(); err != nil {
		log.Fatalf("workflow start: %v", err)
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
