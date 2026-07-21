// SCOPE:layer=infra,removal=core — Server bootstrap (PocketBase, queue, router)
// Package server wires the binary's startup sequence shared by every
// frontend target (web, desktop, future mobile): PocketBase, the goqite
// queue, the todo handlers, and the HTTP routes.
//
// Engine starters (DagNats, NATS JetStream realtime) are build-tag gated
// and live in the cmd/* main packages, NOT here. Callers start those
// engines, then call Run with the resulting JetStream (or nil). This
// keeps internal/server importable by both cmd/web and cmd/desktop
// without a circular dependency on the gated engine code.
//
// Run does everything up to (but not including) pb.Start(). Callers own
// the run loop:
//   - cmd/web:     Run(...) then pb.Start() on the main goroutine
//   - cmd/desktop: Run(...) then pb.Start() in a goroutine + wails.Run()
package server

import (
	"fmt"
	"os"

	"github.com/pocketbase/pocketbase"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/db"
	"github.com/calionauta/gogogo-fullstack-template/features/app"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/handlers"
	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	"github.com/calionauta/gogogo-fullstack-template/router"
)

// Run initializes PocketBase, the queue, workers, the todo handlers, and
// registers all HTTP routes. It returns the ready-to-serve
// *pocketbase.PocketBase, the todo handler (so callers can wire
// build-tag gated engines like DagNats), and a shutdown func callers
// should defer. Run does NOT call pb.Start() — the caller owns the run
// loop.
//
// js may be nil (no realtime broadcaster); the router tolerates a nil
// JetStream and the handlers fall back to in-process SSE.
// Run starts the PocketBase app, queue, handlers, and the router.
//
// Returning 5 values instead of 4 (added *queue.Queue in Phase 3)
// lets main.go wire the SSE Hub into CRDTStore.SetPublisher without
// the router leaking queue refs. Keep this signature in one line
// (or fold to a struct) if more deps appear — call sites get ugly.
func Run(cfg *config.Config, js nats.JetStreamLike) (
	pb *pocketbase.PocketBase,
	todoH *handlers.TodoHandler,
	q *queue.Queue,
	shutdown func(),
	err error,
) {
	pb, initErr := db.Init(cfg)
	if initErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("PocketBase init: %w", initErr)
	}
	if seedErr := db.SeedDefaults(pb, cfg.OfflineSync.Enabled); seedErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("seed: %w", seedErr)
	}

	q, qErr := queue.New(cfg)
	if qErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("queue init: %w", qErr)
	}

	// Context bundles cross-cutting deps; used for LogStartupSummary and
	// as the seam for downstream projects to add cross-cutting middleware.
	_ = app.New(cfg, q)

	todoHL := handlers.New(pb, q, cfg)
	todoH = todoHL
	todoH.RegisterHandlers(q.Registry())
	todoH.SetLLMClient(llm.New(cfg.GoAI.APIKey))

	// Simulated LLM is wired by default (keyless) so the AI Suggest feature
	// and the queue + retry demo work out of the box without a real API
	// key. Opt out explicitly with SIMULATE_LLM=false. The real LLM (when
	// GOAI_API_KEY is set) takes precedence; handleSuggest falls back to
	// the simulated client when the real one is not configured.
	if v := os.Getenv("SIMULATE_LLM"); v != "false" {
		todoH.SetSimulatedLLMClient(llm.NewSimulated())
	}

	workersLocal := q.StartWorkers()
	_ = workersLocal

	router.Init(pb, q, cfg, js, todoH)

	shutdownFn := func() { q.Close() }
	shutdown = shutdownFn
	return pb, todoH, q, shutdown, nil
}
