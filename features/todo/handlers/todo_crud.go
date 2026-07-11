package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/components"
	dshelpers "github.com/calionauta/gogogo-fullstack-template/internal/datastar"
)

func (h *TodoHandler) handleList(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Without loading the app session, listTodos would
	// return EVERY user's todos (no owner filter) — the bug where
	// clicking a filter tab revealed other users' tasks. LoadAppAuth is
	// the /api-aware variant that does NOT skip /api.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Warn("todo: list auth load", "error", err)
	} else if c.Auth == nil {
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	filter := c.Request.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	todos, err := h.listTodos(c, filter)
	if err != nil {
		slog.Error("todo: list failed", "filter", filter, "error", err)
		return c.String(statusInternal, "error listing todos")
	}

	// Merge the filter + itemCount signals so the tabs flip and the
	// header/footer count update; then patch the #todo-list region with
	// the matching rows. Both must happen on the same SSE response so
	// the active-tab class and the visible rows update in lockstep.
	sse := sdk.NewSSE(c.Response, c.Request)
	if err := dshelpers.MergeSignals(sse, todo.Signals{
		Filter:     filter,
		ItemCount:  len(todos),
		Loading:    false,
		LLMEnabled: h.llmEnabled(),
	}); err != nil {
		return err
	}
	return dshelpers.RenderAndPatch(
		sse, components.TodoListRegion(todo.Signals{
			Todos: todos, Filter: filter, ItemCount: len(todos),
			LLMEnabled: h.llmEnabled(),
		}),
		sdk.WithSelector("#todo-list"),
	)
}

// patchTodoListWithSelfOrigin is the per-handler exit for local todo
// mutations: it tags the originating mutation as "self" (so the UI
// picks the self-origin entry animation) and patches the rendered
// list, with view transitions enabled for a smooth cross-fade. Each
// HTTP handler returns the toast AFTER this, so the toast rides on the
// same SSE response and reaches the user together with the new list.
//
// Also merges the latest itemCount into the $itemCount signal so the
// header badge and the footer count update together — previously the
// count lived only on the rendered HTML and never refreshed from the
// server.
func (h *TodoHandler) patchTodoListWithSelfOrigin(sse *sdk.ServerSentEventGenerator, todos []todo.Todo) error {
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"lastItemSource": "self",
		signalItemCount:  len(todos),
	}); err != nil {
		return err
	}
	return dshelpers.RenderAndPatch(sse, h.renderTodoList(todos),
		sdk.WithSelector("#todo-list"),
		sdk.WithViewTransitions())
}

func (h *TodoHandler) handleCreate(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Load the app session cookie explicitly so
	// ownerOf(c) scopes the new todo to the logged-in user instead of
	// saving with an empty owner (which makes it invisible to every
	// authenticated list query).
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Debug("todo: create auth load", "error", err)
	}

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
	if err := h.saveTodo(&item, ownerOf(c)); err != nil {
		slog.Error("todo: save failed", "error", err)
		return c.String(statusInternal, "save failed")
	}

	// Resume the event-driven onboarding flow if this user has a
	// pending onboarding (set by OnboardingStart on login). The first
	// todo they create moves the durable workflow to its second half
	// (todo captured -> scheduled pause -> complete). No-op otherwise.
	if h.onboarding != nil {
		h.onboarding.ResumeOnboarding(ownerOf(c))
	}

	h.broadcastTodo(c, "created", item)

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list after create failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	// Reset loading + clear the input on success so the form returns
	// to a clean idle state. $loading=true flips the button spinner,
	// $newTitle='' clears the title input.
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"loading":       false,
		"newTitle":      "",
		signalItemCount: len(todos),
	}); err != nil {
		return err
	}
	// The patch below is synchronous: the broadcaster already re-renders
	// other connected clients in real time, so the queue would only add
	// latency for a fast local mutation. View Transitions give a free
	// cross-fade on the new item (and any reordered/removed items).
	if err := h.patchTodoListWithSelfOrigin(sse, todos); err != nil {
		return err
	}
	return emitToast(sse, "Added", "success")
}

func (h *TodoHandler) handleToggle(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Load the app session cookie explicitly so the
	// owner-scoped check below works correctly — without it the check
	// is always skipped (c.Auth is nil) and any user can toggle any
	// todo.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Debug("todo: toggle auth load", "error", err)
	}

	rec, err := h.app.FindRecordById("todos", c.Request.PathValue("id"))
	if err != nil {
		return c.String(statusNotFound, "not found")
	}
	// Owner-scoped: a user can only mutate their own todos. Todos with
	// an empty owner (legacy seeds) are allowed so the single-tenant
	// demo keeps working.
	if c.Auth != nil && rec.GetString("owner") != "" && rec.GetString("owner") != c.Auth.Id {
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

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list after toggle failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	return h.patchTodoListWithSelfOrigin(sse, todos)
}

func (h *TodoHandler) handleConfirmDelete(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Load the app session cookie explicitly so the
	// owner-scoped check below works correctly.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Debug("todo: confirm-delete auth load", "error", err)
	}

	rec, err := h.app.FindRecordById("todos", c.Request.PathValue("id"))
	if err != nil {
		return c.String(statusNotFound, "not found")
	}
	// Owner-scoped: a user can only confirm-delete their own todos.
	if c.Auth != nil && rec.GetString("owner") != "" && rec.GetString("owner") != c.Auth.Id {
		return c.String(statusNotFound, "not found")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	// Open the shared confirmation modal by setting the two signals the
	// <dialog> reads. Using an SSE signal merge (rather than an inline
	// data-on:click assignment) keeps the behaviour identical across
	// every client and is trivially testable.
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"confirmingDeleteId":    rec.Id,
		"confirmingDeleteTitle": rec.GetString("title"),
	}); err != nil {
		return err
	}
	return nil
}

func (h *TodoHandler) handleDelete(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Load the app session cookie explicitly so the
	// owner-scoped check below works correctly.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Debug("todo: delete auth load", "error", err)
	}

	rec, err := h.app.FindRecordById("todos", c.Request.PathValue("id"))
	if err != nil {
		return c.String(statusNotFound, "not found")
	}
	// Owner-scoped: a user can only delete their own todos.
	if c.Auth != nil && rec.GetString("owner") != "" && rec.GetString("owner") != c.Auth.Id {
		return c.String(statusNotFound, "not found")
	}
	title := rec.GetString("title")
	if delErr := h.app.Delete(rec); delErr != nil {
		slog.Error("todo: delete failed", "id", rec.Id, "error", delErr)
		return c.String(statusInternal, "delete failed")
	}

	h.broadcastTodo(c, "deleted", todo.Todo{ID: rec.Id, Title: title})

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list after delete failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	// Close the confirmation modal on every client by clearing the
	// two signals the <dialog> reads.
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"confirmingDeleteId":    "",
		"confirmingDeleteTitle": "",
	}); err != nil {
		return err
	}
	if err := h.patchTodoListWithSelfOrigin(sse, todos); err != nil {
		return err
	}
	return emitToast(sse, fmt.Sprintf("Deleted “%s”", title), "info")
}

func (h *TodoHandler) handleClearCompleted(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Load the app session cookie explicitly so
	// clearCompletedFilter scopes the query to the logged-in user
	// instead of clearing every user's completed todos.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Debug("todo: clear-completed auth load", "error", err)
	}

	records, err := h.app.FindRecordsByFilter("todos", clearCompletedFilter(c), "", 0, 0)
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

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list after clear failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	if err := h.patchTodoListWithSelfOrigin(sse, todos); err != nil {
		return err
	}
	if count == 0 {
		return emitToast(sse, "Nothing to clear", "info")
	}
	return emitToast(sse, fmt.Sprintf("Cleared %d completed", count), "success")
}

// ownerOf returns the authenticated user's id, or "" if the request is
// unauthenticated. Used to scope created todos to a tenant so the demo
// user sees only their own todos.
func ownerOf(c *core.RequestEvent) string {
	if c == nil || c.Auth == nil {
		return ""
	}
	return c.Auth.Id
}
