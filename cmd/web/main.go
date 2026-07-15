// SCOPE:core - DO NOT REMOVE - Server entry point. This is the main binary.
// Package main wires the binary's startup sequence: PocketBase, the
// goqite queue, optional NATS JetStream realtime, the DagNats durable
// workflow engine (build tag dagnats), and the HTTP routes. Run with
// `make dev` (live reload) or `make build` (single binary).
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
	"github.com/calionauta/gogogo-fullstack-template/features/store/crdtstore"
	"github.com/calionauta/gogogo-fullstack-template/internal/server"
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

	pb, todoH, q, shutdown, err := server.Run(cfg, nil)
	if err != nil {
		return err
	}
	defer shutdown()

	// DagNats (build tag dagnats) owns the embedded NATS on :4222 and
	// must boot first so the realtime broadcaster can attach to it.
	startDagNats(cfg, pb, todoH)
	defer shutdownDagNats()

	js := startNATS(cfg)
	_ = js // startNATS has the side-effect of wiring the global broadcaster

	// Phase 2: wire the CRDTStore JetStream transport when the chosen
	// store is a *crdtstore.CRDTStore and JetStream is available. The
	// router exposes the concrete type via a package var; we install
	// the transport here (post-Init) so the NATS broadcaster and the
	// CRDT store are both ready.
	if concrete := router.ConcreteTodoStore(); concrete != nil {
		if cs, ok := concrete.(*crdtstore.CRDTStore); ok {
			stopTransport := server.WireCRDTStoreTransport(context.Background(), cs)
			defer stopTransport()
			// Phase 3: wire the SSE Hub publisher so cross-store
			// CRDT ops trigger a client-side resync. Same lifetime
			// as the transport wire; both halt on shutdown.
			_ = server.WireCRDTStorePublisher(cs, q.Hub())
		}
	}

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
