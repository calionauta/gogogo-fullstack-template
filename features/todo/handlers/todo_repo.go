// SCOPE:layer=feature,removal=feature — Repository helpers for the todo handler (thin wrappers
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
	basecoat "github.com/calionauta/gogogo-fullstack-template/web/skins/basecoat"
	morpheus "github.com/calionauta/gogogo-fullstack-template/web/skins/morpheus"
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

// renderTodoList builds the SSE-friendly HTML for the list region,
// dispatching to the skin-specific list template so a morpheus (or
// basecoat) client receives morpheus (or basecoat) HTML on every
// patch — not the default DaisyUI rows. Without this, filter clicks
// and CRUD mutations replace the morpheus card / neo-checkbox rows
// with DaisyUI rows that no longer match the surrounding morpheus
// chrome (CAL-14). Falls back to the shared DaisyUI component when
// the skin is unrecognised so old behaviour is preserved for
// anything we haven't taught about yet.
func (h *TodoHandler) renderTodoList(todos []todo.Todo, skinName string) templ.Component {
	signals := todo.Signals{
		Todos: todos, Filter: "all", ItemCount: len(todos),
		LLMEnabled: h.llmEnabled(),
	}
	return h.renderTodoListRegion(signals, skinName)
}

// renderTodoListRegion is the Signals-flavoured sibling of
// renderTodoList. handleList / handleListFragment already build their
// own todo.Signals (carrying the current filter etc.) and just need a
// template dispatch.
func (h *TodoHandler) renderTodoListRegion(signals todo.Signals, skinName string) templ.Component {
	switch skinName {
	case "morpheus":
		return morpheus.TodoListRegion(signals)
	case "basecoat":
		return basecoat.TodoListRegion(signals)
	default:
		return components.TodoListRegion(signals)
	}
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

// st returns the configured store. router.Init calls SetStore
// before the handler serves traffic; st() then returns that store
// directly. If SetStore was never called (test paths that wire
// via handlers.New() but skip SetStore), the first concurrent
// caller initializes a PBStore on demand; subsequent callers
// return that same instance.
//
// sync.Once is the race-condition fix: the previous "if h.store ==
// nil { h.store = ... }" pattern wrote to h.store from concurrent
// request handlers + SSE stream openers, which `-race` caught in CI.
func (h *TodoHandler) st() store.EntityStore[todo.Todo] {
	h.stOnce.Do(func() {
		if h.store == nil {
			h.stFallback = pbstore.New(h.app, "todos")
		}
	})
	if h.store == nil {
		return h.stFallback
	}
	return h.store
}

// Compile-time guard: the handlers package depends on EntityStore
// being wired by router.Init via SetStore. Without it, listTodos /
// saveTodo / countOwnedTodos would panic on first use. pbstore.PBStore
// is the default implementation; the guard uses the concrete type so
// the build fails if PBStore drifts from the interface.
var _ store.EntityStore[todo.Todo] = (*pbstore.PBStore)(nil)
