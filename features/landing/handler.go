// SCOPE:layer=feature,removal=feature — Public marketing landing page (GET /)
//
// Handler serves the marketing landing page at GET /. The page is
// public — guests and signed-in users see the same hero — so we
// DO NOT redirect on missing auth (the rule for /, /whiteboard,
// and /todo is the opposite: those pages require login).
package landing

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	appcfg "github.com/calionauta/gogogo-fullstack-template/config"
)

// Handler serves the landing page. State is limited to build
// metadata; the page never reads the database.
type Handler struct {
	cfg *appcfg.Config
}

// New constructs a Handler. cfg is read for build label + commit.
func New(cfg *appcfg.Config) *Handler {
	return &Handler{cfg: cfg}
}

// RegisterRoutes wires the GET / route. Wires DIRECTLY on
// se.Router (same pattern as todo + whiteboard + config).
func (h *Handler) RegisterRoutes(se *core.ServeEvent) {
	se.Router.GET("/", h.handleIndex)
}

// handleIndex renders the landing page. Email is empty for guests
// (the navbar shows Sign in) and the authed email otherwise.
func (h *Handler) handleIndex(c *core.RequestEvent) error {
	email := ""
	if c.Auth != nil {
		email = c.Auth.Email()
	}
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response.WriteHeader(http.StatusOK)
	return Index(email, h.cfg.BuildLabel, h.cfg.BuildCommit).Render(c.Request.Context(), c.Response)
}
