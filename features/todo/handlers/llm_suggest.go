package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	dshelpers "github.com/calionauta/gogogo-fullstack-template/internal/datastar"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// handleSuggest enqueues a "suggest" background job instead of calling the
// LLM inline. Suggesting completions is exactly the kind of slow, failure-
// prone work the queue exists for: an LLM call can take seconds and can
// fail, so we hand it to the worker pool and stream the result back over
// SSE (see handleSuggestJob + the "suggest_result" case in the SSE
// dispatcher). The HTTP response only flips suggestPending so the UI can
// show a spinner.
func (h *TodoHandler) handleSuggest(c *core.RequestEvent) error {
	if h.llm == nil || !h.llm.Configured() {
		return c.String(http.StatusServiceUnavailable, "LLM not configured: set GOAI_API_KEY")
	}
	if err := c.Request.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form")
	}
	partial := c.Request.FormValue("partial")
	if partial == "" {
		// The suggest form posts the current input as "title"; treat
		// that as the partial when no explicit "partial" field is sent.
		partial = c.Request.FormValue("title")
	}
	if partial == "" {
		// No context provided — still useful to suggest generic next
		// steps rather than 400'ing the request (which would surface as a
		// console error). The button is also disabled when the input is
		// empty, so this branch is purely defensive.
		partial = "Plan my next tasks"
	}
	return h.enqueueSuggest(c, "suggest", partial)
}

// handleSuggestSimulated enqueues a "suggest_simulated" job. It uses the
// in-process fake LLM (wired via SetSimulatedLLMClient when SIMULATE_LLM=true)
// so the same queue + retry path is demoed without a real API key. The fake
// scripts error → retry → slow response, which the worker surfaces as
// per-attempt toasts.
func (h *TodoHandler) handleSuggestSimulated(c *core.RequestEvent) error {
	if h.llmSimulated == nil || !h.llmSimulated.Configured() {
		return c.String(http.StatusServiceUnavailable, "simulated LLM not enabled: set SIMULATE_LLM=true")
	}
	if err := c.Request.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form")
	}
	partial := c.Request.FormValue("partial")
	if partial == "" {
		partial = "Plan my week"
	}
	return h.enqueueSuggest(c, "suggest_simulated", partial)
}

// enqueueSuggest builds and enqueues a suggest job of the given type,
// threading the originating clientID so the worker routes the result (and
// retry feedback) back to the right browser tab.
func (h *TodoHandler) enqueueSuggest(c *core.RequestEvent, jobType, partial string) error {
	clientID := c.Request.URL.Query().Get("clientID")
	payload, err := json.Marshal(map[string]string{"partial": partial})
	if err != nil {
		return c.String(http.StatusInternalServerError, "marshal failed")
	}
	job := queue.Job{Type: jobType, ClientID: clientID, Payload: payload}
	body, err := json.Marshal(job)
	if err != nil {
		return c.String(http.StatusInternalServerError, "marshal job failed")
	}
	if err := h.q.Enqueue(c.Request.Context(), body); err != nil {
		return c.String(http.StatusInternalServerError, "enqueue failed: "+err.Error())
	}

	sse := sdk.NewSSE(c.Response, c.Request)
	return dshelpers.MergeSignals(sse, map[string]any{
		signalSuggestions:    []string{},
		signalSuggestErr:     "",
		signalSuggestPending: true,
	})
}

// handleSuggestJob is the worker-side handler for "suggest" and
// "suggest_simulated" jobs. It runs inside the queue's RetryConfig.Do, so
// transient failures (e.g. a simulated 500) stream per-attempt feedback to
// the originating client via the SSE "retry" event. On success it pushes
// the suggestions back over SSE; on failure it pushes an error result and
// returns the error so the worker retries.
func (h *TodoHandler) handleSuggestJob(ctx context.Context, hub *queue.SSEHub, job queue.Job) error {
	var p struct {
		Partial string `json:"partial"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode suggest payload: %w", err)
	}

	client := h.llm
	if job.Type == "suggest_simulated" {
		client = h.llmSimulated
	}
	if client == nil || !client.Configured() {
		return fmt.Errorf("llm client not configured for job %s", job.Type)
	}

	suggestions, err := client.ChatSuggest(ctx, p.Partial)
	result := map[string]any{
		signalSuggestions:    []string{},
		signalSuggestErr:     "",
		signalSuggestPending: false,
	}
	if err != nil {
		result[signalSuggestErr] = "AI suggest failed: " + err.Error()
		slog.Error("todo: suggest job failed", "job", job.Type, "error", err)
	} else {
		result[signalSuggestions] = suggestions
	}

	body, marshalErr := json.Marshal(queue.Job{Type: "suggest_result", Payload: mustJSON(result)})
	if marshalErr != nil {
		return fmt.Errorf("marshal suggest result: %w", marshalErr)
	}
	if job.ClientID != "" {
		hub.Send(job.ClientID, body)
		return err // worker retries on failure (visible SSE feedback)
	}
	hub.Broadcast(body)
	return err
}
