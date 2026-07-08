// Package handlers implements the HTTP and worker handlers for the todo
// feature. The HTTP handlers are SSE-friendly: every mutation patches
// signals or appends toast HTML to the client. The worker handler
// (handleTodoCreatedJob) demonstrates the SSE-aware retry pattern: it
// streams a success toast back to the originating client, with retry
// feedback delivered between attempts when delivery fails.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/calionauta/cali-go-stack/config"
	"github.com/calionauta/cali-go-stack/features/todo"
	"github.com/calionauta/cali-go-stack/internal/llm"
	"github.com/calionauta/cali-go-stack/internal/nats"
	"github.com/calionauta/cali-go-stack/internal/queue"
)

// HTTP status codes used by the handlers. Centralized so the lint
// exception for "magic numbers" stays scoped to this package.
const (
	statusBadRequest = 400
	statusNotFound   = 404
	statusInternal   = 500
)

// SSE channel buffer size per client. Each buffered chunk is a few
// hundred bytes (one Datastar event), so 64 gives ~30KB headroom per
// slow client before backpressure kicks in.
const sseClientBuffer = 64

// TodoBroadcaster publishes todo mutations so every connected client
// receives them in real time. It is defined in the nats package (two
// implementations: in-memory default, JetStream with -tags jetstream).
// When nil, mutations are still visible to the originating client via
// the per-request SSE patch but are NOT broadcast to others.
type TodoBroadcaster = nats.TodoBroadcaster

// TodoHandler serves /api/todos/* and /api/todos/stream, and registers
// the worker-side handler for "todo_created" jobs.
type TodoHandler struct {
	app         *pocketbase.PocketBase
	q           *queue.Queue
	cfg         *config.Config
	broadcaster TodoBroadcaster
	llm         *llm.Client
}

// New constructs a TodoHandler. Used by both production wiring (router.Init)
// and integration tests (testFixture).
func New(app *pocketbase.PocketBase, q *queue.Queue, cfg *config.Config) *TodoHandler {
	return &TodoHandler{app: app, q: q, cfg: cfg}
}

// SetLLMClient wires the LLM client used by the AI suggest handler.
// Pass nil to disable AI features (the suggest route won't be
// registered in that case).
func (h *TodoHandler) SetLLMClient(c *llm.Client) { h.llm = c }

// llmEnabled reports whether the AI suggest pathway is live. Used
// by handlers that build Signals so the UI hides the Suggest button
// when the LLM isn't configured.
func (h *TodoHandler) llmEnabled() bool {
	return h.llm != nil && h.llm.Configured()
}

// SetBroadcaster wires the realtime layer. Pass nil (the default) to
// skip cross-client broadcasting; pass the JetStream or in-memory
// broadcaster from the nats package to share todo mutations with all
// connected clients.
func (h *TodoHandler) SetBroadcaster(b TodoBroadcaster) {
	h.broadcaster = b
}

// broadcastTodo publishes a todo mutation event to every connected
// client. No-op when no broadcaster is configured.
func (h *TodoHandler) broadcastTodo(c *core.RequestEvent, event string, item todo.Todo) {
	if h.broadcaster == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"event": event,
		"id":    item.ID,
		"title": item.Title,
		"done":  item.Completed,
	})
	if err != nil {
		slog.Warn("todo: broadcast marshal failed", "error", err)
		return
	}
	if err := h.broadcaster.PublishTodoUpdate(c.Request.Context(), payload); err != nil {
		slog.Warn("todo: broadcast failed", "error", err)
	}
}

// RegisterRoutes wires the HTTP routes on a PocketBase serve event.
func (h *TodoHandler) RegisterRoutes(se *core.ServeEvent) {
	se.Router.GET("/api/todos", h.handleList)
	se.Router.POST("/api/todos", h.handleCreate)
	se.Router.POST("/api/todos/{id}/toggle", h.handleToggle)
	se.Router.POST("/api/todos/completed/delete", h.handleClearCompleted)
	se.Router.POST("/api/todos/{id}/delete", h.handleDelete)
	se.Router.GET("/api/todos/stream", h.handleSSEStream)
	if h.cfg.AdminToken != "" {
		se.Router.POST("/api/admin/unlock", h.handleAdminUnlock)
	}
	if h.llm != nil && h.llm.Configured() {
		se.Router.POST("/api/todos/suggest", h.handleSuggest)
	}
}

// RegisterRoutesOn registers the same routes on a raw router for tests
// that want to drive the handlers via httptest.NewServer without going
// through PocketBase's serve command.
func (h *TodoHandler) RegisterRoutesOn(r *router.Router[*core.RequestEvent]) {
	r.GET("/api/todos", h.handleList)
	r.POST("/api/todos", h.handleCreate)
	r.POST("/api/todos/{id}/toggle", h.handleToggle)
	r.POST("/api/todos/completed/delete", h.handleClearCompleted)
	r.POST("/api/todos/{id}/delete", h.handleDelete)
	r.GET("/api/todos/stream", h.handleSSEStream)
	if h.cfg.AdminToken != "" {
		r.POST("/api/admin/unlock", h.handleAdminUnlock)
	}
	if h.llm != nil && h.llm.Configured() {
		r.POST("/api/todos/suggest", h.handleSuggest)
	}
}

// RegisterHandlers wires the todo handler's background jobs into the
// queue's HandlerRegistry. Call before StartWorkers so the worker pool
// dispatches incoming "todo_created" messages to the toast streamer.
func (h *TodoHandler) RegisterHandlers(reg *queue.HandlerRegistry) {
	reg.Register("todo_created", h.handleTodoCreatedJob)
}

// handleTodoCreatedJob is the worker-side handler invoked when a
// "todo_created" message is dequeued. Sends a success toast via the SSE
// Hub to the client that originated the request. The retry layer
// (RetryConfig.Do) wraps this so transient Hub delivery failures are
// retried with exponential backoff and per-attempt feedback streamed
// to the same client.
func (h *TodoHandler) handleTodoCreatedJob(ctx context.Context, hub *queue.SSEHub, job queue.Job) error {
	var payload struct {
		Type   string `json:"type"`
		TodoID string `json:"todoID"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode todo_created payload: %w", err)
	}

	message := fmt.Sprintf("\u201c%s\u201d added", payload.Title)
	toastPayload, err := json.Marshal(map[string]string{
		"toastType": "success",
		"message":   message,
	})
	if err != nil {
		return fmt.Errorf("marshal toast payload: %w", err)
	}
	envelope := queue.Job{Type: "toast", Payload: toastPayload}
	chunk, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal toast envelope: %w", err)
	}
	if job.ClientID != "" {
		return hub.SendBlocking(ctx, job.ClientID, chunk)
	}
	hub.Broadcast(chunk)
	return nil
}
