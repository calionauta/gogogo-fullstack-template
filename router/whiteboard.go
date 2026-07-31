// SCOPE:feature - REMOVE if not using whiteboard.
package router

import (
	"io/fs"
	"log"

	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/whiteboard"
	"github.com/calionauta/gogogo-fullstack-template/internal/collab"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	"github.com/calionauta/gogogo-fullstack-template/web/resources"
)

// registerWhiteboard wires the collaborative whiteboard with a dedicated
// SSEHub (separate from the todo feature's hub) and NATS as the optional
// cross-instance transport. Creates the shared DocStore that both the
// whiteboard's WebSyncWorker and the SyncWorker (collab sync) use, so
// browser ops and NATS-delivered updates converge the same in-memory docs.
//
// hub is a SEPARATE SSEHub instance (not q.Hub()) so whiteboard events
// never reach todo clients and vice-versa. Pass queue.NewSSEHub() from
// router.Init.
//
// Also serves the whiteboard's own static assets (rough.min.js,
// whiteboard.js) via exact /static/* routes — same pattern as core
// static assets in router.go. These move with the feature on removal.
//
// Returns the DocStore for registerCollabSync to use.
func registerWhiteboard(se *core.ServeEvent, _ *queue.Queue, hub *queue.SSEHub, cfg *config.Config) *collab.DocStore {
	docs := collab.NewDocStore()
	persister := collab.NewPocketBasePersister(se.App)
	nc := nats.Conn() // may be nil if NATS not started; WebSyncWorker handles nil

	// Serve whiteboard static assets (rough.min.js, whiteboard.js) so the
	// feature is self-contained: when the package is removed, its static
	// assets go with it. Same exact-route pattern as core static assets.
	staticFS := whiteboard.StaticFS()
	staticAssets := resources.AssetHandler(staticFS, "/static")
	if err := fs.WalkDir(staticFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		route := "/static/" + path
		se.Router.GET(route, func(c *core.RequestEvent) error {
			staticAssets.ServeHTTP(c.Response, c.Request)
			return nil
		})
		return nil
	}); err != nil {
		log.Printf("whiteboard: error walking static assets: %v", err)
	}

	h := whiteboard.New(se.App, hub, persister, docs, nc, cfg)
	h.RegisterRoutes(se)
	return docs
}
