// SCOPE:layer=feature,removal=feature — Auth-gated read-only /config view (masked secrets)
//
// Package wiring notes
//
//   - GET /config — renders the read-only config view. The route is
//     registered DIRECTLY on se.Router (same pattern as todo + whiteboard).
//   - The handler is gated by RequireAuthOrRedirect, the same middleware
//     applied to / and /whiteboard. Per the issue's decision: any
//     logged-in user can view; superusers are not required.
//   - The template is rendered as an inline HTML page (NOT exposed
//     under /api) so it never appears in API enumeration or service
//     worker caches. See router/noCacheHTML for the no-cache header.
package config

import (
	"log/slog"
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	appcfg "github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/auth"
)

// Handler serves the /config page. It holds the same *config.Config
// pointer the rest of the app uses; rendering reads live state
// (env-decrypted values from the secrets loader) but only ever
// surfaces a redacted view.
type Handler struct {
	cfg *appcfg.Config
}

// New builds the handler. cfg is retained by pointer; caller must
// not mutate it after passing.
func New(cfg *appcfg.Config) *Handler {
	return &Handler{cfg: cfg}
}

// RegisterRoutes wires the HTTP routes on a PocketBase serve event.
// Same registration shape as features/todo/handlers and
// features/whiteboard so a future maintainer sees one consistent
// pattern across every page-style handler in this codebase.
func (h *Handler) RegisterRoutes(se *core.ServeEvent) {
	se.Router.GET("/config", h.handleIndex)
}

// handleIndex renders the read-only /config page. Reads the live
// config via BuildPageData (which never sees secrets raw), wraps
// the resulting rows in the templ-generated Page component, and
// writes the response. Auth is enforced through the same helper
// as /todo and /whiteboard so the redirect target behaviour stays
// consistent across all auth-gated HTML pages.
func (h *Handler) handleIndex(c *core.RequestEvent) error {
	if err := auth.RequireAuthOrRedirect(c); err != nil {
		return err
	}
	email := ""
	if c.Auth != nil {
		email = c.Auth.Email()
	}
	if email == "" {
		slog.Debug("config: auth record has no email; navbar will show Sign in")
	}
	data := BuildPageData(h.cfg)
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response.WriteHeader(http.StatusOK)
	return Page(data, email, h.cfg.BuildLabel, h.cfg.BuildCommit).Render(c.Request.Context(), c.Response)
}
