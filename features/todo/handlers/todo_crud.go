package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/components"
	dshelpers "github.com/calionauta/gogogo-fullstack-template/internal/datastar"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
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

// handleListFragment returns the todo list region as a PLAIN HTML fragment
// (not SSE) so a client can morph #todo-list in place after a PocketBase
// realtime record change. This is the "records" half of the realtime
// strategy: PocketBase realtime (per-user scoped) notifies the browser that
// a todo changed, and the browser re-fetches this fragment via Datastar's
// programmatic @get. The SSE hub (handleList) stays for ephemeral signals
// (retry feedback, suggest, workflow progress).
func (h *TodoHandler) handleListFragment(c *core.RequestEvent) error {
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Warn("todo: fragment auth load", "error", err)
	} else if c.Auth == nil {
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	filter := c.Request.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	todos, err := h.listTodos(c, filter)
	if err != nil {
		slog.Error("todo: fragment list failed", "filter", filter, "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	var buf bytes.Buffer
	if err := components.TodoListRegion(todo.Signals{
		Todos:      todos,
		Filter:     filter,
		ItemCount:  len(todos),
		LLMEnabled: h.llmEnabled(),
	}).Render(c.Request.Context(), &buf); err != nil {
		slog.Error("todo: fragment render failed", "error", err)
		return c.String(statusInternal, "error rendering list")
	}
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Datastar morph hints: when a client re-fetches this fragment (after a
	// PocketBase realtime record change), the outer morph targets #todo-list
	// and replaces the whole region — so deletes disappear and creates/updates
	// merge in. Set by the server so the client's @get needs no opts.
	c.Response.Header().Set("datastar-selector", "#todo-list")
	c.Response.Header().Set("datastar-mode", "outer")
	return c.HTML(http.StatusOK, buf.String())
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
	// idem_key comes from the createForm (hidden input named
	// "idem_key") and is consumed by the PBStore + OnRecordCreateRequest
	// hook for offline-replay dedup. Empty for programmatic callers
	// (e.g. the onboarding worker via CreateTodoForOnboarding).
	idemKey := c.Request.FormValue("idem_key")
	if err := h.saveTodo(c, &item, ownerOf(c), idemKey); err != nil {
		slog.Error("todo: save failed", "error", err)
		return c.String(statusInternal, "save failed")
	}

	// Publish to NATS for cross-instance sync (desktop→server).
	h.publishCrudOp(nats.CrudOpCreate, ownerOf(c), &nats.CrudOpData{
		ID: item.ID, Title: item.Title, Completed: item.Completed,
	})

	// Resume the event-driven onboarding flow if this user has a
	// pending onboarding (set by OnboardingStart on login). The first
	// todo they create moves the durable workflow to its second half
	// (todo captured -> scheduled pause -> complete). No-op otherwise.
	if h.onboarding != nil {
		h.onboarding.ResumeOnboarding(ownerOf(c))
	}

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
	// DB actions now propagate to every client (including the
	// originator's other tabs) via PocketBase realtime — the record
	// mutation fires OnModelAfter*Success, which PocketBase broadcasts
	// to all subscribers of the "todos" topic. The SSE hub is reserved
	// for ephemeral signals (retry feedback, suggest, workflow
	// progress), not record mutations.
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

	id := c.Request.PathValue("id")
	current, err := h.st().Get(ctxOf(c), ownerOf(c), id)
	if errors.Is(err, store.ErrNotFound) || err != nil {
		return c.String(statusNotFound, "not found")
	}
	toggled, err := h.st().Update(ctxOf(c), ownerOf(c), id, map[string]any{
		"completed": !current.Completed,
	})
	if err != nil {
		slog.Error("todo: toggle save failed", "id", id, "error", err)
		return c.String(statusInternal, "toggle failed")
	}

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list after toggle failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	// Publish to NATS for cross-instance sync.
	h.publishCrudOp(nats.CrudOpToggle, ownerOf(c), &nats.CrudOpData{
		ID: toggled.ID, Completed: toggled.Completed,
	})

	sse := sdk.NewSSE(c.Response, c.Request)
	// Record propagation is via PocketBase realtime (see handleCreate).
	return h.patchTodoListWithSelfOrigin(sse, todos)
}

func (h *TodoHandler) handleConfirmDelete(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Load the app session cookie explicitly so the
	// owner-scoped check below works correctly.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Debug("todo: confirm-delete auth load", "error", err)
	}

	id := c.Request.PathValue("id")
	t, err := h.st().Get(ctxOf(c), ownerOf(c), id)
	if errors.Is(err, store.ErrNotFound) || err != nil {
		return c.String(statusNotFound, "not found")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	// Open the shared confirmation modal by setting the two signals the
	// <dialog> reads. Using an SSE signal merge (rather than an inline
	// data-on:click assignment) keeps the behaviour identical across
	// every client and is trivially testable.
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"confirmingDeleteId":    t.ID,
		"confirmingDeleteTitle": t.Title,
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

	id := c.Request.PathValue("id")
	t, err := h.st().Get(ctxOf(c), ownerOf(c), id)
	if errors.Is(err, store.ErrNotFound) || err != nil {
		return c.String(statusNotFound, "not found")
	}
	if delErr := h.st().Delete(ctxOf(c), ownerOf(c), id); delErr != nil && !errors.Is(delErr, store.ErrNotFound) {
		slog.Error("todo: delete failed", "id", id, "error", delErr)
		return c.String(statusInternal, "delete failed")
	}

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list after delete failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	// Publish to NATS for cross-instance sync.
	h.publishCrudOp(nats.CrudOpDelete, ownerOf(c), &nats.CrudOpData{ID: t.ID})

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
	// Record propagation is via PocketBase realtime (see handleCreate).
	return emitToast(sse, fmt.Sprintf("Deleted “%s”", t.Title), "info")
}

func (h *TodoHandler) handleClearCompleted(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths, so c.Auth is nil
	// here by default. Load the app session cookie explicitly so the
	// store scopes the clear to the logged-in user instead of clearing
	// every user's completed todos.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Debug("todo: clear-completed auth load", "error", err)
	}

	count, err := h.st().ClearCompleted(ctxOf(c), ownerOf(c))
	if err != nil {
		slog.Error("todo: clear completed failed", "error", err)
		return c.String(statusInternal, "clear failed")
	}

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list after clear failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	// Publish to NATS for cross-instance sync.
	h.publishCrudOp(nats.CrudOpClearCompleted, ownerOf(c), nil)

	sse := sdk.NewSSE(c.Response, c.Request)
	if err := h.patchTodoListWithSelfOrigin(sse, todos); err != nil {
		return err
	}
	// Record propagation is via PocketBase realtime (see handleCreate); no
	// SSE hub broadcast needed here.
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
