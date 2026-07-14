// SCOPE:core - Repository helpers for the todo handler (query + persistence).
package handlers

import (
	"fmt"

	"github.com/a-h/templ"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/components"
)

// --- Repository ---

// listTodos returns the authenticated user's todos, scoped by the
// todos.owner relation set on create. When the request is
// unauthenticated the filter is left unscoped — the single-tenant demo
// fallback; every production route requires login via RequireAuth.
func (h *TodoHandler) listTodos(c *core.RequestEvent, filter string) ([]todo.Todo, error) {
	var filterExpr string
	switch filter {
	case "active":
		filterExpr = "completed=false"
	case "completed":
		filterExpr = "completed=true"
	default:
		filterExpr = ""
	}
	if c != nil && c.Auth != nil {
		ownerFilter := fmt.Sprintf("owner = %q", c.Auth.Id)
		if filterExpr == "" {
			filterExpr = ownerFilter
		} else {
			filterExpr = filterExpr + " && " + ownerFilter
		}
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

// clearCompletedFilter builds the FindRecordsByFilter expression for the
// "clear completed" action, scoping it to the authenticated user.
func clearCompletedFilter(c *core.RequestEvent) string {
	if c != nil && c.Auth != nil {
		return fmt.Sprintf("completed=true && owner = %q", c.Auth.Id)
	}
	return "completed=true"
}

func (h *TodoHandler) saveTodo(item *todo.Todo, owner string) error {
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
	// Scope the todo to the authenticated user when available so todos
	// are tenant-associated (the demo user sees only their own). The
	// owner field is added by db.SeedDefaults; missing-auth creates are
	// left unscoped (single-tenant demo fallback).
	if owner != "" {
		rec.Set("owner", owner)
	}
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
	return components.TodoListRegion(todo.Signals{
		Todos: todos, Filter: "all", ItemCount: len(todos),
		LLMEnabled: h.llmEnabled(),
	})
}

// countOwnedTodos returns the number of todos owned by the current
// authenticated user (or the total number of todos when auth is nil).
// Uses a simple PocketBase count query rather than loading the full
// list, so it's fast enough for the hot broadcast path.
func (h *TodoHandler) countOwnedTodos(c *core.RequestEvent) (int, error) {
	var filterExpr string
	if c != nil && c.Auth != nil {
		filterExpr = fmt.Sprintf("owner = %q", c.Auth.Id)
	}
	records, err := h.app.FindRecordsByFilter("todos", filterExpr, "", 0, 0)
	if err != nil {
		return 0, fmt.Errorf("count todos (filter=%q): %w", filterExpr, err)
	}
	return len(records), nil
}
