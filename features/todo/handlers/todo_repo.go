// SCOPE:core - Repository helpers for the todo handler (thin wrappers
// around the configured EntityStore so the HTTP layer doesn't need to
// know which storage strategy is wired in).
package handlers

import (
	"context"
	"fmt"

	"github.com/a-h/templ"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/store/pbstore"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/components"
)

// listTodos returns the authenticated user's todos, scoped by the
// store (the strategy filters by owner internally). The handler-side
// filter values are: "" (all), "active", "completed".
func (h *TodoHandler) listTodos(c *core.RequestEvent, filter string) ([]todo.Todo, error) {
	owner := ownerOf(c)
	todos, err := h.st().List(ctxOf(c), owner, filter)
	if err != nil {
		return nil, fmt.Errorf("list todos (filter=%q): %w", filter, err)
	}
	return todos, nil
}

// saveTodo persists a new todo owned by owner. idemKey is the
// client-generated UUID used for offline-replay dedup (PBStore uses it
// via the OnRecordCreateRequest hook; CRDTStore would use op IDs).
func (h *TodoHandler) saveTodo(c *core.RequestEvent, item *todo.Todo, owner, idemKey string) error {
	out, err := h.st().Create(ctxOf(c), *item, owner, idemKey)
	if err != nil {
		return fmt.Errorf("save todo: %w", err)
	}
	*item = out
	return nil
}

// countOwnedTodos returns the number of todos owned by the current
// authenticated user (or the total when auth is nil). Cheap — uses
// the store's count query, no full load.
func (h *TodoHandler) countOwnedTodos(c *core.RequestEvent) (int, error) {
	return h.st().Count(ctxOf(c), ownerOf(c))
}

// renderTodoList builds the SSE-friendly HTML for the list region.
// Unchanged from before — it's a presentation concern, not storage.
func (h *TodoHandler) renderTodoList(todos []todo.Todo) templ.Component {
	return components.TodoListRegion(todo.Signals{
		Todos: todos, Filter: "all", ItemCount: len(todos),
		LLMEnabled: h.llmEnabled(),
	})
}

// ctxOf returns a context.Context derived from the request. Falls back
// to context.Background() for synthetic calls (no request, e.g. the
// onboarding worker that calls saveTodo programmatically).
func ctxOf(c *core.RequestEvent) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// st returns the configured store, falling back to a lazily-built
// PBStore on the handler's *pocketbase.PocketBase when SetStore was
// never called. The lazy fallback keeps existing tests (which wire
// the handler via New() but don't call SetStore) working without
// modification; production wiring in router.Init calls SetStore
// explicitly so the fallback is dead code at runtime.
func (h *TodoHandler) st() store.EntityStore[todo.Todo] {
	if h.store == nil {
		h.store = pbstore.New(h.app, "todos")
	}
	return h.store
}

// Compile-time guard: the handlers package depends on EntityStore
// being wired by router.Init via SetStore. Without it, listTodos /
// saveTodo / countOwnedTodos would panic on first use. pbstore.PBStore
// is the default implementation; the guard uses the concrete type so
// the build fails if PBStore drifts from the interface.
var _ store.EntityStore[todo.Todo] = (*pbstore.PBStore)(nil)
