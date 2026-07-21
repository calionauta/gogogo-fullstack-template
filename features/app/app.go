// SCOPE:layer=feature,removal=core — AppContext (cross-cutting deps bundle)
// Package app provides the cross-cutting "application core" pattern
// for projects built on top of gogogo-fullstack-template. In a real project
// cloned from this template, this is where you'd put:
//
//   - shared middleware (request_id, logging, recovery, CORS)
//   - cross-feature types (RequestContext, feature flags)
//   - a single struct (Context) that bundles the dependencies
//     every feature needs (queue, broadcaster, LLM client, config)
//     so handlers don't have to assemble them individually
//
// The template itself keeps Context lightweight — only the
// dependency bundle is here, since gogogo-fullstack-template is a single
// binary and most of the cross-cutting concerns (auth, SSE, queue
// retry) are already in their own internal/* packages. Downstream
// projects that grow to multiple features will appreciate having
// this struct as the dependency container; feature handlers can
// accept *Context instead of (pb, q, cfg, llm, broadcaster).
package app

import (
	"log/slog"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// Context is the dependency container passed to feature
// handlers. Constructed once at startup (see cmd/web/main.go) and
// shared via embedding or explicit injection.
//
// Fields are exported so handlers can read them directly:
//   - cfg:           env-derived config (Host, Port, AdminToken, ...)
//   - queue:         the goqite-backed background queue + SSE Hub
//   - llm:           the LLM client (may be nil if no API key set)
//
// The template's current feature handlers (features/todo) take
// individual dependencies; future features can take *Context
// instead. Context does NOT depend on PocketBase or NATS —
// the PocketBase *core.App reference and the realtime broadcaster
// stay where they are (TodoHandler / router) because they're
// tightly coupled to the request lifecycle.
type Context struct {
	Cfg *config.Config

	// Queue is the goqite-backed work queue. Handler-side, feature
	// code calls ctx.Queue.Hub() to get the SSEHub for streaming
	// events back to the browser.
	Queue *queue.Queue

	// LLM is the GoAI-backed LLM client. nil when GOAI_API_KEY is
	// unset — handlers must check before calling.
	LLM *llm.Client
}

// New constructs an Context with the standard dependencies. The
// LLM client is built from the env-loaded config (caller has
// already run config.Load() and populated cfg.GoAI.APIKey).
func New(cfg *config.Config, q *queue.Queue) *Context {
	return &Context{
		Cfg:   cfg,
		Queue: q,
		LLM:   llm.New(cfg.GoAI.APIKey),
	}
}

// LogStartupSummary writes a one-line human-readable summary of the
// app's configuration to slog. Use at the end of main() so the
// operator can see what's loaded in a glance (port, demo creds set,
// LLM configured, etc.).
//
// The function is deliberately idempotent and read-only — it's
// safe to call multiple times during dev. Never logs secrets.
func (a *Context) LogStartupSummary() {
	llmConfigured := a.LLM != nil && a.LLM.Configured()
	slog.Info(
		"app: startup summary",
		"host", a.Cfg.Host,
		"port", a.Cfg.Port,
		"llm_configured", llmConfigured,
		"admin_token_set", a.Cfg.AdminToken != "",
		"data_dir", a.Cfg.DataDir,
	)
}
