package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/cali-go-stack/features/todo"
	"github.com/calionauta/cali-go-stack/features/todo/components"
	dshelpers "github.com/calionauta/cali-go-stack/internal/datastar"
	"github.com/calionauta/cali-go-stack/internal/queue"
)

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
		AdminEnabled: h.cfg.AdminToken != "",
		LLMEnabled:   h.llmEnabled(),
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
		AdminEnabled: h.cfg.AdminToken != "",
		LLMEnabled:   h.llmEnabled(),
	})
}
