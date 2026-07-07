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
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/cali-go-stack/config"
	"github.com/calionauta/cali-go-stack/features/todo"
	"github.com/calionauta/cali-go-stack/features/todo/components"
	dshelpers "github.com/calionauta/cali-go-stack/internal/datastar"
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

// TodoHandler serves /api/todos/* and /api/todos/stream, and registers
// the worker-side handler for "todo_created" jobs.
type TodoHandler struct {
	app *pocketbase.PocketBase
	q   *queue.Queue
	cfg *config.Config
}

// New constructs a TodoHandler. Used by both production wiring (router.Init)
// and integration tests (testFixture).
func New(app *pocketbase.PocketBase, q *queue.Queue, cfg *config.Config) *TodoHandler {
	return &TodoHandler{app: app, q: q, cfg: cfg}
}

// RegisterRoutes wires the HTTP routes on a PocketBase serve event.
func (h *TodoHandler) RegisterRoutes(se *core.ServeEvent) {
	se.Router.GET("/api/todos", h.handleList)
	se.Router.POST("/api/todos", h.handleCreate)
	se.Router.POST("/api/todos/{id}/toggle", h.handleToggle)
	se.Router.POST("/api/todos/completed/delete", h.handleClearCompleted)
	se.Router.POST("/api/todos/{id}/delete", h.handleDelete)
	se.Router.GET("/api/todos/stream", h.handleSSEStream)
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

func (h *TodoHandler) handleList(c *core.RequestEvent) error {
	filter := c.Request.URL.Query().Get("filter")
	todos, err := h.listTodos(filter)
	if err != nil {
		slog.Error("todo: list failed", "filter", filter, "error", err)
		return c.String(statusInternal, "error listing todos")
	}

	sse := sdk.NewSSE(c.Response, c.Request)
	return dshelpers.MergeSignals(sse, todo.Signals{
		Todos: todos, Filter: filter, ItemCount: len(todos),
	})
}

func (h *TodoHandler) handleCreate(c *core.RequestEvent) error {
	if err := c.Request.ParseForm(); err != nil {
		return c.String(statusBadRequest, "invalid form")
	}
	title := c.Request.FormValue("title")
	if title == "" {
		return c.String(statusBadRequest, "title required")
	}

	item := todo.Todo{
		Title:     title,
		Completed: false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := h.saveTodo(&item); err != nil {
		slog.Error("todo: save failed", "error", err)
		return c.String(statusInternal, "save failed")
	}

	if err := h.enqueueCreatedEvent(c, item); err != nil {
		// Enqueue failure is non-fatal for the HTTP response: the todo
		// was saved, so the client will see it in the patch. The toast
		// simply won't arrive via SSE.
		slog.Warn("todo: enqueue failed", "error", err)
	}

	todos, err := h.listTodos("all")
	if err != nil {
		slog.Error("todo: list after create failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	// No immediate toast — the worker emits the "Added" toast via SSE
	// once it picks up the "todo_created" job. This exercises the full
	// HTTP → queue → worker → SSE pipeline and lets the retry layer
	// stream per-attempt feedback if Hub delivery fails.
	return dshelpers.RenderAndPatch(sse, h.renderTodoList(todos))
}

// enqueueCreatedEvent packages a "todo_created" job into the queue so
// the worker can stream the success toast back to the originating client.
// ClientID is read from the query string so the worker routes the toast
// to the right browser tab instead of broadcasting to everyone.
func (h *TodoHandler) enqueueCreatedEvent(c *core.RequestEvent, item todo.Todo) error {
	clientID := c.Request.URL.Query().Get("clientID")
	payload, err := json.Marshal(map[string]string{
		"type": "todo_created", "todoID": item.ID, "title": item.Title,
	})
	if err != nil {
		return fmt.Errorf("marshal create payload: %w", err)
	}
	job := queue.Job{Type: "todo_created", ClientID: clientID, Payload: payload}
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job envelope: %w", err)
	}
	return h.q.Enqueue(c.Request.Context(), body)
}

func (h *TodoHandler) handleToggle(c *core.RequestEvent) error {
	rec, err := h.app.FindRecordById("todos", c.Request.PathValue("id"))
	if err != nil {
		return c.String(statusNotFound, "not found")
	}
	rec.Set("completed", !rec.GetBool("completed"))
	if saveErr := h.app.Save(rec); saveErr != nil {
		slog.Error("todo: toggle save failed", "id", rec.Id, "error", saveErr)
		return c.String(statusInternal, "toggle failed")
	}

	todos, err := h.listTodos("all")
	if err != nil {
		slog.Error("todo: list after toggle failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	return dshelpers.RenderAndPatch(sse, h.renderTodoList(todos))
}

func (h *TodoHandler) handleDelete(c *core.RequestEvent) error {
	rec, err := h.app.FindRecordById("todos", c.Request.PathValue("id"))
	if err != nil {
		return c.String(statusNotFound, "not found")
	}
	title := rec.GetString("title")
	if delErr := h.app.Delete(rec); delErr != nil {
		slog.Error("todo: delete failed", "id", rec.Id, "error", delErr)
		return c.String(statusInternal, "delete failed")
	}

	todos, err := h.listTodos("all")
	if err != nil {
		slog.Error("todo: list after delete failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	if err := dshelpers.RenderAndPatch(sse, h.renderTodoList(todos)); err != nil {
		return err
	}
	return emitToast(sse, fmt.Sprintf("Deleted \u201c%s\u201d", title), "info")
}

func (h *TodoHandler) handleClearCompleted(c *core.RequestEvent) error {
	records, err := h.app.FindRecordsByFilter("todos", "completed=true", "", 0, 0)
	if err != nil {
		slog.Error("todo: find completed failed", "error", err)
		return c.String(statusInternal, "find failed")
	}
	count := len(records)
	for _, r := range records {
		if delErr := h.app.Delete(r); delErr != nil {
			slog.Warn("todo: clear-completed delete failed", "id", r.Id, "error", delErr)
		}
	}

	todos, err := h.listTodos("all")
	if err != nil {
		slog.Error("todo: list after clear failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	if err := dshelpers.RenderAndPatch(sse, h.renderTodoList(todos)); err != nil {
		return err
	}
	if count == 0 {
		return emitToast(sse, "Nothing to clear", "info")
	}
	return emitToast(sse, fmt.Sprintf("Cleared %d completed", count), "success")
}

func (h *TodoHandler) handleSSEStream(c *core.RequestEvent) error {
	clientID := c.Request.URL.Query().Get("clientID")
	if clientID == "" {
		clientID = uuid.New().String()
	}

	sse := sdk.NewSSE(c.Response, c.Request)
	ch := make(chan []byte, sseClientBuffer)
	h.q.Hub().Register(clientID, ch)
	defer h.q.Hub().Unregister(clientID)

	todos, err := h.listTodos("all")
	if err != nil {
		slog.Error("todo: list on sse open failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	if err := dshelpers.MergeSignals(sse, todo.Signals{
		Todos: todos, Filter: "all", ItemCount: len(todos),
	}); err != nil {
		return err
	}

	for {
		select {
		case <-c.Request.Context().Done():
			return nil
		case msg := <-ch:
			if err := h.dispatchStreamMessage(sse, msg); err != nil {
				slog.Warn("todo: sse dispatch failed", "error", err)
			}
		}
	}
}

// dispatchStreamMessage routes a queued SSE chunk to the right client-side
// patch. Worker output arrives as a queue.Job envelope: type "toast" is
// rendered as a Templ component, type "retry" is merged as a signal so
// the user sees per-attempt feedback, anything else is forwarded as a
// raw signal (defensive default).
func (h *TodoHandler) dispatchStreamMessage(sse *sdk.ServerSentEventGenerator, msg []byte) error {
	var job queue.Job
	if err := json.Unmarshal(msg, &job); err != nil {
		return fmt.Errorf("decode stream envelope: %w", err)
	}
	switch job.Type {
	case "toast":
		var p struct {
			ToastType string `json:"toastType"`
			Message   string `json:"message"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("decode toast payload: %w", err)
		}
		if p.ToastType == "" {
			p.ToastType = "info"
		}
		return emitToast(sse, p.Message, p.ToastType)
	case "retry":
		return dshelpers.MergeSignals(sse, map[string]any{
			"lastRetry": string(job.Payload),
		})
	default:
		return dshelpers.MergeSignals(sse, map[string]any{
			"lastJob": string(msg),
		})
	}
}

// emitToast renders the Toast component and appends it to #toast-container
// on the client. The toast's open state, dismiss timer, and progress bar
// are all driven by Datastar attributes on the rendered template.
func emitToast(sse *sdk.ServerSentEventGenerator, message, toastType string) error {
	return dshelpers.RenderAndPatch(
		sse,
		components.Toast(message, toastType, components.NewToastID()),
		sdk.WithSelectorID("toast-container"),
		sdk.WithModeAppend(),
	)
}

// --- Repository ---

func (h *TodoHandler) listTodos(filter string) ([]todo.Todo, error) {
	var filterExpr string
	switch filter {
	case "active":
		filterExpr = "completed=false"
	case "completed":
		filterExpr = "completed=true"
	default:
		filterExpr = ""
	}
	records, err := h.app.FindRecordsByFilter("todos", filterExpr, "-created", 0, 0)
	if err != nil {
		return nil, fmt.Errorf("find todos (filter=%q): %w", filter, err)
	}
	res := make([]todo.Todo, len(records))
	for i, r := range records {
		res[i] = todoFromRecord(r)
	}
	return res, nil
}

func (h *TodoHandler) saveTodo(item *todo.Todo) error {
	col, err := h.app.FindCollectionByNameOrId("todos")
	if err != nil {
		return fmt.Errorf("find todos collection: %w", err)
	}
	rec := core.NewRecord(col)
	// PocketBase auto-generates a 15-char id when none is set on the
	// record. Don't pass a client-side uuid here — the collection's
	// primary key has Max=15 enforced by PocketBase.
	rec.Set("title", item.Title)
	rec.Set("completed", item.Completed)
	if err := h.app.Save(rec); err != nil {
		return fmt.Errorf("save todo: %w", err)
	}
	item.ID = rec.Id
	return nil
}

func todoFromRecord(r *core.Record) todo.Todo {
	return todo.Todo{
		ID:        r.Id,
		Title:     r.GetString("title"),
		Completed: r.GetBool("completed"),
		CreatedAt: r.GetDateTime("created").Time(),
		UpdatedAt: r.GetDateTime("updated").Time(),
	}
}

func (h *TodoHandler) renderTodoList(todos []todo.Todo) templ.Component {
	return components.TodoList(todo.Signals{
		Todos: todos, Filter: "all", ItemCount: len(todos),
	})
}
