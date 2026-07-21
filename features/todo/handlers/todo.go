// SCOPE:layer=feature,removal=feature — Todo MVC example (reference implementation)
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
	"net/http"
	"sync"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/store"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/components"
	dshelpers "github.com/calionauta/gogogo-fullstack-template/internal/datastar"
	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
	morpheus "github.com/calionauta/gogogo-fullstack-template/web/skins/morpheus"

	// basecoat skin imported via features/todo/components/skin_imports.go
	basecoat "github.com/calionauta/gogogo-fullstack-template/web/skins/basecoat"
)

// HTTP status codes used by the handlers. Centralized so the lint
// exception for "magic numbers" stays scoped to this package.
const (
	statusBadRequest = 400
	statusNotFound   = 404
	statusInternal   = 500
)

// resolveSkin picks the active UI skin for a request. Mirrors the
// logic in handleIndex: the `?skin=` query param overrides the
// configured default (UI_SKIN env var / config.Config.Skin). Returns
// "daisyui" when the requested skin is unknown so the caller always
// has a valid skin name to dispatch on. Used by SSE handlers
// (handleList, handleListFragment, patchTodoListWithSelfOrigin) that
// must morph #todo-list with HTML matching the skin the client is
// currently rendering — without this, every filter click / mutation
// swapped morpheus rows for DaisyUI rows (the bug behind CAL-14).
func (h *TodoHandler) resolveSkin(c *core.RequestEvent) string {
	skinName := h.cfg.Skin
	if c != nil && c.Request != nil {
		if q := c.Request.URL.Query().Get("skin"); q != "" {
			skinName = q
		}
	}
	switch skinName {
	case "morpheus", "basecoat":
		return skinName
	default:
		return "daisyui"
	}
}

// SSE channel buffer size per client. Each buffered chunk is a few
// hundred bytes (one Datastar event), so the default (64) gives ~30KB
// headroom per slow client before backpressure kicks in. Defined in
// config.DefaultClientQueueSize — change there to tune globally.

// TodoBroadcaster publishes todo mutations so every connected client
// receives them in real time. It is defined in the nats package (two
// implementations: in-memory default, JetStream when a JetStream context
// is wired).
// When nil, mutations are still visible to the originating client via
// the per-request SSE patch but are NOT broadcast to others.
type TodoBroadcaster = nats.TodoBroadcaster

// TodoHandler serves /api/todos/* and /api/todos/stream, and registers
// the worker-side handlers for "retry_demo", "suggest", and
// "suggest_simulated" jobs.
type TodoHandler struct {
	// store is the plugin persistence layer. Wired by router.Init
	// via SetStore. Defaults to a PBStore (features/store/pbstore) when
	// config.EntityStore is "pb" (the only option today); future
	// CRDTStore lands behind the same interface.
	store        store.EntityStore[todo.Todo]
	app          *pocketbase.PocketBase
	q            *queue.Queue
	cfg          *config.Config
	broadcaster  TodoBroadcaster
	crudPub      *nats.CrudPublisher // publishes CRUD ops to NATS for cross-instance sync
	llm          *llm.Client
	llmSimulated *llm.Client
	// onboarding drives the event-driven onboarding flow. It is an
	// interface so the default build can hold a nil without importing
	// the dagnats-tagged OnboardingHandler. The concrete
	// *OnboardingHandler is wired in RegisterOnboardingRoutes (dagnats
	// build only); nil when dagnats is disabled.
	onboarding OnboardingResumer

	// stOnce protects the lazy fallback in st() so concurrent
	// goroutines (every request handler + the SSE stream opener
	// for each client) all converge on a single PBStore instance.
	// Without it the cross-goroutine writes to h.store trip
	// `-race` in CI. SetStore writes h.store through a separate
	// path; st() only fills h.stFallback.
	stOnce     sync.Once
	stFallback store.EntityStore[todo.Todo]
}

// OnboardingResumer is the capability the create path needs from the
// onboarding flow: resume it when a user with a pending onboarding adds
// their first todo. Declared here (default build) so handleCreate can
// call it unconditionally; the dagnats build supplies the real impl.
type OnboardingResumer interface {
	ResumeOnboarding(user string)
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

// SetSimulatedLLMClient wires the in-process fake LLM client used by the
// "Suggest (simulated)" handler. Enabled via SIMULATE_LLM=true so the
// queue + retry async path can be demoed without a real API key.
func (h *TodoHandler) SetSimulatedLLMClient(c *llm.Client) { h.llmSimulated = c }

// CreateTodoForOnboarding programmatically creates a todo. Used by the
// DagNats onboarding worker handlers (always compiled; no-op when
// DAGNATS_ENABLED=false) to write example
// todos into the main PocketBase collection as the durable workflow
// advances. owner scopes the todo to a user; pass "" for the unscoped
// demo fallback. It reuses the same validation/save path as the HTTP
// create handler so the todos appear identically in the UI and broadcast
// to the subscribed client (per-user scoped via the owner rule).
func (h *TodoHandler) CreateTodoForOnboarding(title, owner string) error {
	item := &todo.Todo{Title: title, Completed: false}
	return h.saveTodo(nil, item, owner, "")
}

// llmEnabled reports whether the AI suggest pathway is live. Used
// by handlers that build Signals so the UI hides the Suggest button
// when the LLM isn't configured.
func (h *TodoHandler) llmEnabled() bool {
	return h.llm != nil && h.llm.Configured()
}

// simulatedLLMEnabled reports whether the simulated LLM pathway is live.
func (h *TodoHandler) simulatedLLMEnabled() bool {
	return h.llmSimulated != nil && h.llmSimulated.Configured()
}

// SetBroadcaster wires the realtime layer for EPHEMERAL signals (retry
// feedback, suggest, workflow progress). Record mutations are NO LONGER
// broadcast here — they flow through PocketBase realtime (per-user scoped)
// on the client, which re-fetches /api/todos/fragment. Pass nil (the
// default) to skip cross-client broadcasting of ephemeral signals too.
func (h *TodoHandler) SetBroadcaster(b TodoBroadcaster) {
	h.broadcaster = b
}

// SetStore wires the plugin persistence layer. Called by
// router.Init; tests may wire a different store for isolation. The
// PBStore from features/store/pbstore is the default; CRDTStore
// (future) plugs in here without any change to this handler.
func (h *TodoHandler) SetStore(s store.EntityStore[todo.Todo]) {
	h.store = s
}

// SetCrudPublisher wires the NATS CRUD publisher for cross-instance sync.
// When set, every todo CRUD operation is also published to JetStream
// after being written to PocketBase, so other instances (or the server,
// for desktop edges) converge. Pass nil (the default) to disable.
func (h *TodoHandler) SetCrudPublisher(p *nats.CrudPublisher) {
	h.crudPub = p
}

// publishCrudOp publishes a CRUD operation to NATS if the publisher is
// configured. Called AFTER the handler writes to PocketBase, so failure
// to publish does NOT affect the response. The operation data captures
// the PocketBase-generated ID so the remote consumer reuses it.
func (h *TodoHandler) publishCrudOp(op nats.CrudOpType, userID string, data *nats.CrudOpData) {
	if h.crudPub == nil {
		return
	}
	h.crudPub.Publish(op, userID, data)
}

// RegisterRoutes wires the HTTP routes on a PocketBase serve event.
func (h *TodoHandler) RegisterRoutes(se *core.ServeEvent) {
	se.Router.GET("/todo", h.handleIndex)
	se.Router.GET("/api/todos", h.handleList)
	se.Router.GET("/api/todos/fragment", h.handleListFragment)
	// POST routes pass through unchanged. Replay dedup happens at the
	// collection level: the `todos` collection has an `idem_key` field
	// with a unique index, and `db/RegisterIdempotencyHook` installs an
	// OnRecordCreateRequest hook that returns the original record on a
	// matching key (within the same owner). See ARCHITECTURE.md
	// "Offline strategy" + ARCHITECTURE.md for the rationale.
	se.Router.POST("/api/todos", h.handleCreate)
	se.Router.POST("/api/todos/{id}/toggle", h.handleToggle)
	se.Router.POST("/api/todos/completed/delete", h.handleClearCompleted)
	se.Router.POST("/api/todos/{id}/delete", h.handleDelete)
	se.Router.GET("/api/todos/{id}/confirm-delete", h.handleConfirmDelete)
	se.Router.GET("/api/todos/stream", h.handleSSEStreamWithAuth)
	se.Router.POST("/api/todos/retry-demo", h.handleEnqueueRetryDemo)
	// AI Suggest is available when EITHER the real LLM (GOAI_API_KEY) or
	// the keyless simulated LLM is configured. handleSuggest prefers the
	// real client and falls back to the simulated one, so a single route
	// serves both and the button is never dead.
	if h.llmEnabled() || h.simulatedLLMEnabled() {
		se.Router.POST("/api/todos/suggest", h.handleSuggest)
	}
	if h.llmSimulated != nil && h.llmSimulated.Configured() {
		se.Router.POST("/api/todos/suggest-simulated", h.handleSuggestSimulated)
	}
}

// realtimeTransport returns the label for the active broadcast transport.
// JetStream when NATS is enabled (and the binary was built with the
// jetstream tag, which is the only way NATS is wired), otherwise the
// storeLabel returns a short label describing the active EntityStore
// strategy. Designed for the page sub-header that surfaces the
// running persistence mode so a tester can verify which strategy
// is wired (PBStore vs CRDTStore) by eye.
func storeLabel(cfg *config.Config) string {
	switch cfg.EntityStore {
	case "", "pb":
		return "PocketBase records"
	case "crdt":
		return "Loro CRDT per-user + PB snapshot"
	default:
		return cfg.EntityStore
	}
}

// offlineLabel describes the offline-sync behavior of the active
// strategy. Surfaced next to the store label in the page sub-header
// so the user knows what to expect when the network drops.
func offlineLabel(cfg *config.Config) string {
	if !cfg.OfflineSync.Enabled {
		return "OFFLINE_SYNC_ENABLED=false — actions fail when network is down; offline mutations NOT queued"
	}
	switch cfg.EntityStore {
	case "crdt":
		return "CRDT cross-instance sync via JetStream (NATS); SW queues browser mutations; mode = crdt"
	default:
		if cfg.NATS.Enabled {
			label := "PBStore + NATS_ENABLED=true — SW queues browser mutations"
			label += " + JetStream (NATS) streams CRUD ops across replicas"
			return label + "; mode = pb + nats"
		}
		return "PBStore + NATS_ENABLED=false — SW queues browser mutations; no JetStream; no cross-instance sync; mode = pb"
	}
}

// realtimeLabel returns the label for the active broadcast transport.
// in-process InMemoryBroadcaster. Used by the diagnostics panel so the
// badge reflects what is actually running rather than a hardcoded string.
func realtimeLabel(cfg *config.Config) string {
	if cfg != nil && cfg.NATS.Enabled {
		return "JetStream"
	}
	return "in-memory"
}

// handleIndex serves the demo Todo page. Wraps the TodoList signal
// patch in the auth.Navbar + Layout. Requires login: guests are
// bounced to /login by the RequireAuthOrRedirect middleware applied
// in router.Init.
//
// Supports ?skin= query parameter to override the configured skin
// (UI_SKIN env var), allowing per-request skin switching without
// restarting the server.
func (h *TodoHandler) handleIndex(c *core.RequestEvent) error {
	if c.Auth == nil {
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	// Resolve skin: ?skin= query param overrides the env config.
	skinName := h.cfg.Skin
	if q := c.Request.URL.Query().Get("skin"); q != "" {
		skinName = q
	}
	userEmail := ""
	if c.Auth != nil {
		userEmail = c.Auth.Email()
	}
	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Warn("todo: list on index failed", "error", err)
		todos = nil
	}
	signals := todo.Signals{
		Todos:            todos,
		Filter:           "all",
		ItemCount:        len(todos),
		LLMEnabled:       h.llmEnabled(),
		SimulatedLLM:     h.simulatedLLMEnabled(),
		DagNatsEnabled:   h.cfg.DagNats.Enabled,
		ConnectedClients: h.q.Hub().CountUserClients(),
		Suggestions:      []string{},
		SuggestErr:       "",
		RealtimeKind:     realtimeLabel(h.cfg),
		StoreLabel:       storeLabel(h.cfg),
		OfflineLabel:     offlineLabel(h.cfg),
		SidebarTab:       "queue",
	}
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Dispatch to skin-specific template.
	// DaisyUI uses the shared components.Layout.
	// BasecoatUI uses its own template with shadcn-style HTML.
	// Morpheus uses a web component layout (neo.*).
	if skinName == "morpheus" {
		return morpheus.TodoPage(
			"Todos — gogogo-fullstack-template",
			signals, userEmail,
			h.cfg.BuildLabel, h.cfg.BuildCommit,
			h.cfg.OfflineSync.Enabled,
			skinName,
		).Render(c.Request.Context(), c.Response)
	}
	if skinName == "basecoat" {
		return basecoat.TodoPage(
			"Todos — gogogo-fullstack-template",
			signals, userEmail,
			h.cfg.BuildLabel, h.cfg.BuildCommit,
			h.cfg.OfflineSync.Enabled,
			skinName,
		).Render(c.Request.Context(), c.Response)
	}
	return components.Layout(
		"Todos — gogogo-fullstack-template",
		signals, userEmail,
		h.cfg.BuildLabel, h.cfg.BuildCommit,
		h.cfg.OfflineSync.Enabled,
		skinName,
	).Render(c.Request.Context(), c.Response)
}

// RegisterRoutesOn registers the same routes on a raw router for tests
// that want to drive the handlers via httptest.NewServer without going
// through PocketBase's serve command.
func (h *TodoHandler) RegisterRoutesOn(r *router.Router[*core.RequestEvent]) {
	r.GET("/todo", h.handleIndex)
	r.GET("/api/todos", h.handleList)
	r.GET("/api/todos/fragment", h.handleListFragment)
	r.POST("/api/todos", h.handleCreate)
	r.POST("/api/todos/{id}/toggle", h.handleToggle)
	r.POST("/api/todos/completed/delete", h.handleClearCompleted)
	r.POST("/api/todos/{id}/delete", h.handleDelete)
	r.GET("/api/todos/{id}/confirm-delete", h.handleConfirmDelete)
	r.GET("/api/todos/stream", h.handleSSEStreamWithAuth)
	r.POST("/api/todos/retry-demo", h.handleEnqueueRetryDemo)
	// AI Suggest is available when EITHER the real LLM (GOAI_API_KEY) or
	// the keyless simulated LLM is configured. handleSuggest prefers the
	// real client and falls back to the simulated one, so a single route
	// serves both and the button is never dead.
	if h.llmEnabled() || h.simulatedLLMEnabled() {
		r.POST("/api/todos/suggest", h.handleSuggest)
	}
	if h.llmSimulated != nil && h.llmSimulated.Configured() {
		r.POST("/api/todos/suggest-simulated", h.handleSuggestSimulated)
	}
}

// RegisterHandlers wires the todo handler's background jobs into the
// queue's HandlerRegistry. Call before StartWorkers so the worker pool
// dispatches incoming jobs (retry_demo, suggest, suggest_simulated) to
// the right handler.
func (h *TodoHandler) RegisterHandlers(reg *queue.HandlerRegistry) {
	reg.Register("retry_demo", h.handleRetryDemoJob)
	reg.Register("suggest", h.handleSuggestJob)
	reg.Register("suggest_simulated", h.handleSuggestJob)
}

// handleEnqueueRetryDemo enqueues a "retry_demo" background job so the
// worker pool can exercise the queue + retry layer end-to-end. The job
// deliberately fails twice then succeeds, streaming per-attempt feedback
// to every connected client (see handleRetryDemoJob). Triggered from the
// Techstack/Diagnostics panel in the UI.
func (h *TodoHandler) handleEnqueueRetryDemo(c *core.RequestEvent) error {
	if err := h.q.Enqueue(context.Background(), mustJSON(queue.Job{Type: "retry_demo"})); err != nil {
		return c.String(statusInternal, "enqueue failed")
	}
	sse := sdk.NewSSE(c.Response, c.Request)
	return dshelpers.MergeSignals(sse, map[string]any{
		"lastRetry": "queued retry-demo job",
	})
}

// handleRetryDemoJob is the worker-side handler for "retry_demo" jobs. It
// runs a 3-attempt operation that fails on the first two attempts to make
// the retry layer (exponential backoff + SSE feedback) visible: each
// attempt's status is broadcast to every connected client, and a final
// toast reports success. This is the canonical demonstration of the
// queue-with-retry techstack slice.
// retryDemoInitialDelay spaces the retry attempts so the user can SEE
// the demo progress (the steps light one-by-one via the SSE "retry"
// feedback). A sub-second gap made all three attempts look instant; ~1.5s
// gives a perceptible beat between attempts without feeling sluggish.
const retryDemoInitialDelay = 1500 * time.Millisecond

// jobTypeToast is the queue.Job type for toast notifications so
// the literal isn't duplicated across handlers (goconst).
const jobTypeToast = "toast"

// jobTypeSuggestResult is the queue.Job type for AI suggest results.
const jobTypeSuggestResult = "suggest_result"

// phaseError is the shared "error" phase string used by both the
// onboarding stepper and the todo SSE dispatcher for error toasts.
const phaseError = "error"

func (h *TodoHandler) handleRetryDemoJob(ctx context.Context, hub *queue.SSEHub, _ queue.Job) error {
	const maxAttempts = 3
	attempt := 0
	err := retry.Do(
		func() error {
			attempt++
			// Deliberately fail the first two attempts to demonstrate
			// the retry layer; succeed on the final attempt.
			var opErr error
			if attempt < maxAttempts {
				opErr = fmt.Errorf("simulated transient failure on attempt %d", attempt)
			}
			h.broadcastRetryFeedback(hub, attempt, opErr)
			return opErr
		},
		retry.Attempts(maxAttempts),
		retry.Delay(retryDemoInitialDelay),
		retry.MaxDelay(2500*time.Millisecond), //nolint:mnd // 2.5s retry cap: visible pacing
		retry.Context(ctx),
	)
	if err != nil {
		hub.Broadcast(toastJob("Queue + retry demo failed", phaseError))
		return err
	}
	hub.Broadcast(toastJob("Queue + retry OK — 3 attempts", "success"))
	return nil
}

func (h *TodoHandler) broadcastRetryFeedback(hub *queue.SSEHub, attempt int, opErr error) {
	status := "attempt"
	if opErr == nil {
		status = "success"
	}
	payload := mustJSON(map[string]any{
		"operation": "retry-demo",
		"attempt":   attempt,
		"status":    status,
		"error":     errMsg(opErr),
	})
	hub.Broadcast(mustJSON(queue.Job{Type: "retry", Payload: payload}))
}

// toastJob builds a "toast" queue.Job envelope for hub.Broadcast.
func toastJob(message, kind string) []byte {
	p := mustJSON(map[string]string{"toastType": kind, "message": message})
	j := mustJSON(queue.Job{Type: jobTypeToast, Payload: p})
	return j
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Warn("todo: marshal job", "error", err)
		return nil
	}
	return b
}
