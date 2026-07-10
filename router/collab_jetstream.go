//go:build jetstream

package router

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	natsio "github.com/nats-io/nats.go"

	"github.com/calionauta/gogogo-fullstack-template/internal/collab"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"

	"github.com/pocketbase/pocketbase/core"
)

// registerCollabSync wires the Loro CRDT SyncWorker: it subscribes to
// app.sync.> on the embedded NATS and persists resolved whiteboard docs
// to the PocketBase "whiteboards" collection. This is the central-server
// side of the edge-sync design (Phase C). No-op if NATS is disabled.
//
// The worker runs in a goroutine until the serve event's context ends.
func registerCollabSync(se *core.ServeEvent) {
	if nats.JetStream() == nil {
		return
	}
	nc := nats.Conn()
	if nc == nil {
		return
	}
	persister := collab.NewPocketBasePersister(se.App)
	worker := collab.NewSyncWorker(nc, persister)
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		se.App.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			cancel()
			return e.Next()
		})
		if err := worker.Run(ctx); err != nil {
			slog.Error("collab sync worker stopped", "error", err)
		}
	}()

	// Ephemeral presence bridge: browser clients subscribe to a whiteboard's
	// cursors via Server-Sent Events at /api/collab/presence/<docID>. The
	// handler subscribes the same app.presence.<docID> NATS subject the
	// desktop edges publish to, so cursors from any edge (including Leaf
	// Node replicas) stream live to the browser. No persistence.
	se.Router.GET("/api/collab/presence/{docID}", func(c *core.RequestEvent) error {
		docID := c.Request.PathValue("docID")
		if docID == "" {
			return c.NoContent(400)
		}
		c.Response.Header().Set("Content-Type", "text/event-stream")
		c.Response.Header().Set("Cache-Control", "no-cache")
		c.Response.Header().Set("Connection", "keep-alive")
		flusher, ok := c.Response.(http.Flusher)
		if !ok {
			return c.NoContent(500)
		}
		// Best-effort client disconnect detection.
		ctx := c.Request.Context()
		sub, err := nc.Subscribe(collab.PresenceSubject(docID), func(m *natsio.Msg) {
			select {
			case <-ctx.Done():
				return
			default:
			}
			fmt.Fprintf(c.Response, "data: %s\n\n", m.Data)
			flusher.Flush()
		})
		if err != nil {
			return c.NoContent(503)
		}
		defer sub.Unsubscribe()
		<-ctx.Done()
		return nil
	})
}
