// Command desktop builds the Wails v3 native app shell around the exact
// same Go backend the web app uses (PocketBase + goqite queue + router +
// handlers). It boots the server via internal/server.Run, serves
// PocketBase in a goroutine, and points the Wails webview at it through a
// reverse proxy — so the desktop app and the web app share 100% of the
// business logic.
//
// Build with -tags jetstream: the desktop becomes a NATS Leaf Node that
// syncs its JetStream streams with the central server (NATS_LEAFNODE_URL)
// — Phase B of the edge-sync design. Phase C (Loro CRDT collab) builds on
// top of this transport.
//
//go:build jetstream

package main

import (
	"context"
	"fmt"
	"log"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/internal/collab"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/server"
)

func main() {
	cfg := config.Load()

	// Edge sync (Phase B): if a central NATS URL is configured, boot this
	// instance as a Leaf Node so its JetStream streams replicate to/from
	// the central server (offline edits replay on reconnect). Otherwise
	// start a standalone embedded NATS for local realtime only.
	var js nats.JetStreamLike
	if cfg.NATS.LeafNodeURL != "" {
		if err := nats.StartLeafNode(cfg.NATS.StoreDir, cfg.NATS.LeafNodeURL); err != nil {
			log.Printf("WARN: leaf node start failed, falling back to standalone NATS: %v", err)
		} else {
			js = nats.JetStream()
		}
	} else if cfg.NATS.Enabled {
		if err := nats.StartEmbedded(cfg.NATS.StoreDir); err != nil {
			log.Printf("WARN: NATS start failed, in-memory broadcaster only: %v", err)
		} else {
			js = nats.JetStream()
		}
	}

	// Boot the shared backend (PocketBase + queue + router + handlers).
	// js is the JetStream the broadcaster uses (Leaf Node or standalone);
	// nil falls back to in-process SSE.
	pb, _, shutdown, err := server.Run(cfg, js)
	if err != nil {
		log.Fatalf("boot failed: %v", err)
	}

	// Edge sync (Phase C): publish local Loro updates on app.sync.<docID>.
	// When running as a Leaf Node, these replicate to the central server
	// (and the central SyncWorker persists them to PocketBase). When
	// standalone, they fan out to local realtime subscribers. The doc
	// edit below is a minimal end-to-end smoke of the publish path.
	if js != nil && nats.Conn() != nil {
		pub := collab.NewPublisher(nats.Conn())
		demoDoc := collab.NewDoc("desktop-demo")
		if pubErr := pub.PublishUpdate(demoDoc, nil); pubErr != nil {
			log.Printf("WARN: collab publish failed: %v", pubErr)
		} else {
			log.Printf("desktop: published collab update for %s", demoDoc.ID())
		}

		// Ephemeral presence: broadcast this desktop's cursor on the demo
		// doc so other edges / the browser see it live. A real whiteboard
		// UI would call PublishCursor on pointer move; here we tick a demo
		// cursor so the presence path is exercised end-to-end.
		pres := collab.NewPresence(nats.Conn(), "desktop-demo", "desktop", 0, 0)
		presCtx, presCancel := context.WithCancel(context.Background())
		defer presCancel()
		go func() {
			_ = pres.Subscribe(presCtx)
		}()
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			var x float64
			for {
				select {
				case <-presCtx.Done():
					return
				case <-t.C:
					x = (x + 0.1)
					if x > 1 {
						x = 0
					}
					_ = pres.PublishCursor(x, 0.5)
				}
			}
		}()
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	// PocketBase blocks on pb.Start(), so run it on a goroutine. The
	// Wails webview reaches it via the reverse proxy below.
	go func() {
		args := []string{"serve", "--http", addr}
		if len(os.Args) > 1 {
			args = os.Args[1:]
		}
		pb.RootCmd.SetArgs(args)
		if startErr := pb.Start(); startErr != nil {
			log.Fatalf("pocketbase start: %v", startErr)
		}
	}()

	// Reverse proxy: the Wails webview loads http://localhost:<port>
	// (where PocketBase serves the UI) through this handler.
	target, err := url.Parse(fmt.Sprintf("http://%s", addr))
	if err != nil {
		log.Fatalf("parse target url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	app := application.New(application.Options{
		Name: "Gogogo Template",
		Assets: application.AssetOptions{
			Handler: proxy,
		},
		// Shut the backend down when the Wails app closes (window quit),
		// not via defer — defer would be skipped on a fatal error and the
		// gocritic linter flags it. OnShutdown runs on a clean exit.
		OnShutdown: func() {
			shutdown()
		},
	})

	log.Printf("desktop: PocketBase on %s, webview proxied", addr)
	if runErr := app.Run(); runErr != nil {
		log.Fatalf("wails run: %v", runErr)
	}
}
