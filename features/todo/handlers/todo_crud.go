package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/cali-go-stack/features/todo"
	dshelpers "github.com/calionauta/cali-go-stack/internal/datastar"
	"github.com/calionauta/cali-go-stack/internal/queue"
)

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
		AdminEnabled: h.cfg.AdminToken != "",
		LLMEnabled:   h.llmEnabled(),
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

	h.broadcastTodo(c, "created", item)

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

	toggled := todo.Todo{
		ID:        rec.Id,
		Title:     rec.GetString("title"),
		Completed: rec.GetBool("completed"),
	}
	h.broadcastTodo(c, "toggled", toggled)

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

	h.broadcastTodo(c, "deleted", todo.Todo{ID: rec.Id, Title: title})

	todos, err := h.listTodos("all")
	if err != nil {
		slog.Error("todo: list after delete failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	if err := dshelpers.RenderAndPatch(sse, h.renderTodoList(todos)); err != nil {
		return err
	}
	return emitToast(sse, fmt.Sprintf("Deleted “%s”", title), "info")
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
