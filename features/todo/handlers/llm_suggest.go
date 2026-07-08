package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	dshelpers "github.com/calionauta/gogogo-template/internal/datastar"
	"github.com/calionauta/gogogo-template/internal/llm"
)

// handleSuggest is the "AI suggest next todo" endpoint. Reads the
// partial title from the form, calls llm.Client.ChatSuggest (which
// uses project-level exponential backoff), and patches the
// `suggestions` signal with the result. The UI renders the
// suggestions as clickable items that fill the input.
//
// Failure modes:
//   - 503: client not configured (no API key). Suggests the user
//     set GOAI_API_KEY or run bin/init-secrets.
//   - 502: LLM provider failed after retries. Logs the full error;
//     shows a generic toast to the user.
//   - 200: 0-3 suggestions, JSON-parsed from the LLM response.
func (h *TodoHandler) handleSuggest(c *core.RequestEvent) error {
	if h.llm == nil || !h.llm.Configured() {
		if h.llm == nil {
			return c.String(http.StatusServiceUnavailable, "AI suggest not configured")
		}
		return c.String(http.StatusServiceUnavailable, h.llm.MustConfigured().Error())
	}
	if err := c.Request.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form")
	}
	partial := c.Request.FormValue("partial")
	if partial == "" {
		return c.String(http.StatusBadRequest, "partial title required")
	}

	suggestions, err := h.llm.ChatSuggest(c.Request.Context(), partial)
	if err != nil {
		// ErrNoAPIKey would have been caught above; if it reaches here
		// the key was deleted between the check and the call (very
		// unlikely). Other errors: provider timeout, parse failure,
		// network error.
		if errors.Is(err, llm.ErrNoAPIKey) {
			return c.String(http.StatusServiceUnavailable, h.llm.MustConfigured().Error())
		}
		slog.Error("todo: AI suggest failed", "partial", partial, "error", err)
		// Still patch a suggestions signal so the UI clears stale ones.
		sse := sdk.NewSSE(c.Response, c.Request)
		mergeErr := dshelpers.MergeSignals(sse, map[string]any{
			"suggestions": []string{},
			"suggestErr":  "AI suggest failed. See server logs.",
		})
		if mergeErr != nil {
			return mergeErr
		}
		return nil
	}

	sse := sdk.NewSSE(c.Response, c.Request)
	if mergeErr := dshelpers.MergeSignals(sse, map[string]any{
		"suggestions": suggestions,
		"suggestErr":  "",
	}); mergeErr != nil {
		return mergeErr
	}
	return nil
}
