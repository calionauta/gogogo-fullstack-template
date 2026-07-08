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
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/db"
	"github.com/calionauta/gogogo-fullstack-template/features/app"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/handlers"
	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	"github.com/calionauta/gogogo-fullstack-template/router"
)

func main() {
	// `health` is a scratch-compatible healthcheck subcommand: it does an
	// internal GET to /health and exits 0 on 200, 1 otherwise. The image
	// is scratch (no shell, no wget/curl), so the compose HEALTHCHECK uses
	// `CMD ["/app", "health"]` (exec form) rather than a shell command.
	if len(os.Args) > 1 && os.Args[1] == "health" {
		if err := runHealthcheck(); err != nil {
			log.Fatalf("healthcheck failed: %v", err)
		}
		return
	}
	if err := run(); err != nil {
		log.Fatalf("startup failed: %v", err)
	}
}

func runHealthcheck() error {
	cfg := config.Load()
	url := fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil) // #nosec G107
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return nil
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

	// Context bundles the cross-cutting dependencies (queue, LLM
	// client) and is the seam where downstream projects add their own
	// cross-cutting middleware. Currently only used for LogStartupSummary
	// below; future feature handlers can take *app.Context instead
	// of (q, llm, ...) to avoid the dependency-assembly boilerplate.
	_ = app.New(cfg, q)

	todoH := handlers.New(pb, q, cfg)
	todoH.RegisterHandlers(q.Registry())
	todoH.SetLLMClient(llm.New(cfg.GoAI.APIKey))

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
	// PocketBase v0.39+ uses a Cobra root command. pb.Start() calls
	// RootCmd.Execute() internally, so we set the args on the command
	// (not os.Args — that confuses the help printer and makes it exit
	// 1 with the usage text). Default to `serve` with our bind addr
	// when no subcommand is given, but pass through any explicit
	// subcommand (e.g. `superuser upsert`, `migrate`) so CLI admin
	// tasks work instead of always forcing `serve`.
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"serve", "--http", addr}
	}
	pb.RootCmd.SetArgs(args)

	log.Printf("listening on %s", addr)
	if startErr := pb.Start(); startErr != nil {
		return fmt.Errorf("server: %w", startErr)
	}
	return nil
}
