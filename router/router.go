// SCOPE:core - DO NOT REMOVE - Main server routing.
package router

import (
	"io/fs"
	"log"
	"log/slog"
	"net/http"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/store/crdtstore"
	"github.com/calionauta/gogogo-fullstack-template/features/store/pbstore"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/handlers"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	"github.com/calionauta/gogogo-fullstack-template/web/resources"
)

// Init registers custom routes on PocketBase's serve event.
// Call before pb.Start(). Pass todoH as the
// same handler instance the caller used for RegisterHandlers so the
// routes and the SSE worker path share state.
func Init(
	app *pocketbase.PocketBase,
	q *queue.Queue,
	cfg *config.Config,
	js nats.JetStreamLike,
	todoH *handlers.TodoHandler,
) {
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Priority: -100,
		Func: func(se *core.ServeEvent) error {
			se.Router.BindFunc(auth.LoadAuthFromCookie)
			return se.Next()
		},
	})

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Global auth middleware is bound by the priority -100 hook above.
		se.Router.GET("/health", func(c *core.RequestEvent) error {
			return c.String(200, "ok")
		})

		// Serve embedded static assets (CSS, JS, images).
		//
		// PocketBase registers a catch-all dashboard route
		// (`e.Router.GET("/{path...}", apis.Static(...))`) that requires
		// superuser auth and redirects unauthenticated requests to /login.
		// That catch-all SHADOWS any wildcard route we register (e.g.
		// /static/*) — the handler is never reached, so every /static/*
		// request 303-redirects to /login and the browser chokes on the
		// HTML response when it expected CSS/JS (strict MIME checking).
		//
		// Echo gives EXACT routes the highest priority, so we register
		// one exact route per embedded file under /static/. Exact routes
		// win over the catch-all, the assets are served with correct MIME
		// types, and no auth is required (the dashboard auth is per-route,
		// not a global guard — proven by /api/todos serving unauthed).
		staticFS := resources.StaticFS()
		if err := fs.WalkDir(staticFS, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			route := "/static/" + path
			se.Router.GET(route, func(c *core.RequestEvent) error {
				hs := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
				hs.ServeHTTP(c.Response, c.Request)
				return nil
			})
			return nil
		}); err != nil {
			log.Printf("static: error walking embedded assets: %v", err)
		}

		// Auth: login/logout/cookie middleware. Wires the demo login
		// page and ensures every request has e.Auth populated from the
		// pb_auth cookie before reaching feature handlers.
		auth.CookieSecure = !cfg.Dev && cfg.Host != "127.0.0.1"
		auth.RegisterAuth(se)
		// Remove auth: delete this line + delete features/auth/

		// Wire the realtime broadcaster so todo mutations are shared
		// with every connected client. Uses NATS JetStream when available,
		// falls back to in-memory SSE Hub fan-out. Built ONCE and shared
		// across all handlers to avoid "already bound" consumer errors.
		//
		// The todo and whiteboard features use SEPARATE SSEHub instances
		// so a broadcast from one never reaches the other's clients.
		// This prevents wasted parsing (a whiteboard shape event reaching
		// a todo stream, or a todo toast reaching a whiteboard stream)
		// at negligible memory cost (two small maps instead of one).
		// If you need only one feature, the unused hub is GC'd with its
		// package — zero overhead.
		todoHub := q.Hub() // queue's own hub — used by goqite workers + todo SSE
		broadcaster := newTodoBroadcaster(js, todoHub)
		if jsBroadcaster, ok := broadcaster.(interface{ Subscribe(*queue.SSEHub) }); ok {
			jsBroadcaster.Subscribe(todoHub)
		}
		// Wire the NATS CRUD publisher for cross-instance sync. Only
		// initialized when OfflineSync is enabled AND NATS JetStream is
		// available (js is non-nil). When disabled, CrudPublisher is nil
		// and the handler's publishCrudOp is a no-op — zero cost.
		var crudPub *nats.CrudPublisher
		if cfg.OfflineSync.Enabled {
			crudPub = nats.NewCrudPublisher(js)
		}
		if todoH != nil {
			todoH.SetBroadcaster(broadcaster)
			todoH.SetCrudPublisher(crudPub)
			// Wire the pluggable persistence layer. PBStore is the
			// default; CRDTStore (Loro+JetStream, future) plugs in via
			// the same SetStore hook without touching the handlers.
			todoStore, storeErr := buildTodoStore(app, cfg.EntityStore)
			if storeErr != nil {
				slog.Error("router: build todo store failed; falling back to PBStore",
					"strategy", cfg.EntityStore, "error", storeErr)
				todoStore = pbstore.New(app, "todos")
			}
			todoH.SetStore(todoStore)
			todoH.RegisterRoutes(se)
		} else {
			// Defensive fallback: construct a fresh handler if the
			// caller forgot to pass one.
			fallback := handlers.New(app, q, cfg)
			fallback.SetBroadcaster(broadcaster)
			fallback.SetCrudPublisher(crudPub)
			fallback.RegisterRoutes(se)
		}
		// Remove todos: delete this block + delete features/todo/ + delete db/pocketbase.go seed

		// Onboarding: DagNats durable workflow (WelcomeOnboarding).
		// Dependencies: DagNats running on :8090, TodoHandler, NATS broadcaster.
		// Guarded by DagNats.Enabled so the reverse-proxy routes aren't
		// registered when DagNats is disabled (avoids zombie 502 routes).
		if cfg.DagNats.Enabled {
			registerOnboarding(app, q, se, broadcaster, todoH, cfg.DagNats.HTTPAddr)
			// Remove onboarding: delete this block + delete features/todo/handlers/onboarding.go + delete internal/dagnats/
		}

		// Whiteboard: collaborative canvas (Loro CRDT + Rough.js + SSE hub
		// + NATS sync). Dependencies: SSE Hub, PocketBase whiteboards,
		// NATS sync worker.
		// Uses a SEPARATE SSEHub from the todo feature so shapes and presence
		// events never reach todo clients (and vice-versa).
		// Creates the shared DocStore used by both WebSyncWorker and SyncWorker.
		whiteboardHub := queue.NewSSEHub()
		docs := registerWhiteboard(se, q, whiteboardHub)
		// Remove whiteboard: delete this line + delete features/whiteboard/
		// + delete internal/collab/

		// Collab sync: subscribes app.sync.> on NATS, persists whiteboard docs.
		// Uses the same DocStore as the whiteboard handler (shared convergence).
		registerCollabSync(se, docs)
		// Remove collab sync: delete this line + delete internal/collab/sync.go

		// NATS CRUD consumer: subscribes app.crud.todo.> and writes todo
		// operations to PocketBase. This is the server-side counterpart
		// to the CrudPublisher: desktop edges publish CRUD ops to their
		// local NATS; the Leaf Node replicates to the server; this
		// consumer writes them to the server's PocketBase.
		// Only started when OfflineSync is enabled AND NATS is available.
		// No-op when either condition is false.
		if cfg.OfflineSync.Enabled {
			registerCrudConsumer(se, js, cfg.AppName)
		}
		// Remove crud consumer: delete this line + delete internal/nats/crudproxy.go

		return se.Next()
	})
}

// buildTodoStore selects the persistence strategy for todo entities
// based on cfg.EntityStore. Currently supports "pb" (default,
// PocketBase records) and "crdt" (Loro per owner + PB snapshot).
// Future strategies plug in here without touching the handlers —
// the EntityStore interface is the contract.
func buildTodoStore(app core.App, strategy string) (store.EntityStore[todo.Todo], error) {
	switch strategy {
	case "", "pb":
		return pbstore.New(app, "todos"), nil
	case "crdt":
		s := crdtstore.New(app)
		if err := s.EnsureSchema(); err != nil {
			return nil, err
		}
		return s, nil
	default:
		return nil, errUnknownStoreStrategy(strategy)
	}
}

// errUnknownStoreStrategy is returned when the configured
// ENTITY_STORE doesn't match any built-in strategy. The router
// catches this and falls back to PBStore with a logged warning,
// but typing it lets future code (e.g. tests) distinguish "not
// configured" from "configured wrong".
type errUnknownStoreStrategy string

func (e errUnknownStoreStrategy) Error() string {
	return "router: unknown ENTITY_STORE strategy: " + string(e)
}
