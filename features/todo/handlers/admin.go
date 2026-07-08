package handlers

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	dshelpers "github.com/calionauta/gogogo-template/internal/datastar"
)

// handleAdminUnlock demonstrates the "real secret in the demo app"
// pattern: the user submits a master password; the handler compares it
// (constant-time) against the AdminToken loaded from the
// age-encrypted secrets file at startup. On match, all todos are
// deleted and a success toast is streamed. On mismatch, 403.
//
// The route is only registered (and the UI button only rendered) when
// cfg.AdminToken != "". This makes the example honest: the
// feature exists, but it only works for projects that have run
// bin/init-secrets and set ADMIN_UNLOCK_TOKEN.
func (h *TodoHandler) handleAdminUnlock(c *core.RequestEvent) error {
	if h.cfg.AdminToken == "" {
		return c.String(http.StatusNotFound, "admin unlock not configured")
	}
	if err := c.Request.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form")
	}
	submitted := c.Request.FormValue("token")
	if submitted == "" {
		return c.String(http.StatusBadRequest, "token required")
	}

	// Constant-time compare to avoid leaking token length or prefix.
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(h.cfg.AdminToken)) != 1 {
		return c.String(http.StatusForbidden, "wrong token")
	}

	records, err := h.app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil {
		slog.Error("admin: find all todos failed", "error", err)
		return c.String(http.StatusInternalServerError, "find failed")
	}
	count := 0
	for _, r := range records {
		if delErr := h.app.Delete(r); delErr != nil {
			slog.Warn("admin: delete failed", "id", r.Id, "error", delErr)
			continue
		}
		count++
	}
	slog.Info("admin unlock: cleared all todos", "count", count)

	sse := sdk.NewSSE(c.Response, c.Request)
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"todos":     []any{},
		"itemCount": 0,
		"unlocked":  true,
	}); err != nil {
		return err
	}
	return emitToast(sse, fmt.Sprintf("Admin unlock: cleared %d todos", count), "warning")
}
