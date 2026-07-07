// Package main wires the binary's startup sequence: PocketBase, the
// goqite queue, optional NATS JetStream and Turbine runtimes, and the
// HTTP routes. Run with `make dev` (live reload) or `make build`
// (single binary).
//
// All Fatalf calls live at the top of main and the actual long-running
// call (pb.Start) returns instead of Fatalf-ing so deferred shutdown
// hooks fire on the way out.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/calionauta/cali-go-stack/config"
	"github.com/calionauta/cali-go-stack/db"
	"github.com/calionauta/cali-go-stack/features/todo/handlers"
	"github.com/calionauta/cali-go-stack/internal/queue"
	"github.com/calionauta/cali-go-stack/router"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("startup failed: %v", err)
	}
}

func run() error {
	cfg := config.Load()

	pb, err := db.Init(cfg)
	if err != nil {
		return fmt.Errorf("PocketBase init: %w", err)
	}

	if seedErr := db.SeedDefaults(pb); seedErr != nil {
		return fmt.Errorf("seed: %w", seedErr)
	}

	q, err := queue.New(cfg)
	if err != nil {
		return fmt.Errorf("queue init: %w", err)
	}
	defer q.Close()

	todoH := handlers.New(pb, q, cfg)
	todoH.RegisterHandlers(q.Registry())

	workers := q.StartWorkers()
	_ = workers // held for parity with turbine runtime var; unused at call site

	startNATS(cfg)

	startTurbine(cfg)
	defer shutdownTurbine()

	var workflowRt router.WorkflowRuntime
	if rt := getTurbineRuntime(); rt != nil {
		if typed, ok := rt.(router.WorkflowRuntime); ok {
			workflowRt = typed
		}
	}
	router.Init(pb, q, cfg, workflowRt, todoH)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	os.Args = append([]string{os.Args[0]}, "--http", addr)

	log.Printf("listening on %s", addr)
	if startErr := pb.Start(); startErr != nil {
		return fmt.Errorf("server: %w", startErr)
	}
	return nil
}
