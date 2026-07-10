package router

import (
	"io/fs"
	"log"
	"net/http"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/handlers"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	"github.com/calionauta/gogogo-fullstack-template/web/resources"
)

// WorkflowRuntime is a marker for the Turbine runtime. The router only
// checks for non-nil to decide whether to wire onboarding routes; the
// type assertion to *workflow.Runtime happens in the build-tag-gated
// onboarding file. Keeping this as a marker (no methods) means default
// builds don't need to construct anything that satisfies it.
type WorkflowRuntime interface {
	// Marker method — implemented by *workflow.Runtime.
	IsWorkflowRuntime()
}

// Init registers custom routes on PocketBase's serve event.
// Call before pb.Start(). Pass workflowRt = nil if Turbine is not
// enabled (default builds without `-tags turbine`). Pass todoH as the
// same handler instance the caller used for RegisterHandlers so the
// routes and the SSE worker path share state.
func Init(
	app *pocketbase.PocketBase,
	q *queue.Queue,
	cfg *config.Config,
	workflowRt WorkflowRuntime,
	js nats.JetStreamLike,
	todoH *handlers.TodoHandler,
) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Global auth middleware: populate e.Auth from the pb_auth cookie
		// on every request so custom route handlers can check e.Auth != nil.
		// (Defined in features/auth but only wired here — the test harness
		// wires its own copy.) Must run before route handlers read e.Auth.
		se.Router.BindFunc(auth.LoadAuthFromCookie)

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

		// Wire the realtime broadcaster so todo mutations are shared
		// with every connected client (JetStream with -tags jetstream,
		// in-memory fan-out otherwise). Build it ONCE and share it:
		// constructing it twice in one process makes the second
		// JetStream durable consumer fail with "already bound".
		//
		// The jetstream broadcaster must Subscribe to its stream BEFORE
		// any publish, so it re-emits every mutation to the SSE Hub that
		// fans out to all connected tabs. Without Subscribe the published
		// events sit in JetStream unread and the demo shows no realtime
		// sync (the bug we hit: broadcasts registered but never reached
		// remote clients).
		broadcaster := newTodoBroadcaster(js, q.Hub())
		if jsBroadcaster, ok := broadcaster.(interface{ Subscribe(*queue.SSEHub) }); ok {
			jsBroadcaster.Subscribe(q.Hub())
		}
		if todoH != nil {
			// Use the same handler instance the caller registered for
			// background jobs so route state (PocketBase app ref, queue
			// ref, config) is consistent across HTTP and worker paths.
			todoH.SetBroadcaster(broadcaster)
			todoH.RegisterRoutes(se)
		} else {
			// Defensive fallback: construct a fresh handler if the
			// caller forgot to pass one. Lets the rest of the router
			// keep working even in misconfigured test setups.
			fallback := handlers.New(app, q, cfg)
			fallback.SetBroadcaster(broadcaster)
			fallback.RegisterRoutes(se)
		}

		// Onboarding workflow routes are wired here when Turbine is
		// enabled. The handler reads the concrete *workflow.Runtime via
		// RegisterOnboardingRoutes' build-tag switch.
		registerOnboarding(app, q, se, workflowRt, broadcaster, todoH)

		return se.Next()
	})
}
