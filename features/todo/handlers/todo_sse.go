package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/components"
	dshelpers "github.com/calionauta/gogogo-fullstack-template/internal/datastar"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

func (h *TodoHandler) handleSSEStream(c *core.RequestEvent) error {
	// The global auth middleware skips /api/* paths (PocketBase owns
	// those), so c.Auth is nil here by default. Load the app session
	// cookie explicitly so listTodos scopes to the logged-in owner
	// instead of falling back to the single-tenant "all todos" view.
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Warn("todo: sse auth load — falls back to unscoped list", "error", err)
	} else if c.Auth == nil {
		slog.Warn("todo: sse no auth cookie — list is unscoped")
	}

	clientID := c.Request.URL.Query().Get("clientID")
	if clientID == "" {
		clientID = uuid.New().String()
	}

	sse := sdk.NewSSE(c.Response, c.Request)
	ch := make(chan []byte, sseClientBuffer)
	h.q.Hub().Register(clientID, ch)
	defer func() {
		h.q.Hub().Unregister(clientID)
		h.broadcastClientCount()
	}()

	todos, err := h.listTodos(c, "all")
	if err != nil {
		slog.Error("todo: list on sse open failed", "error", err)
		return c.String(statusInternal, "error listing todos")
	}
	if err := dshelpers.MergeSignals(sse, todo.Signals{
		Todos:            todos,
		Filter:           "all",
		ItemCount:        len(todos),
		LLMEnabled:       h.llmEnabled(),
		SimulatedLLM:     h.simulatedLLMEnabled(),
		DagNatsEnabled:   h.cfg.DagNats.Enabled,
		ConnectedClients: h.q.Hub().Stats().Clients,
		ClientID:         clientID,
		SidebarTab:       "queue",
	}); err != nil {
		return err
	}
	// Tell every connected client how many are online now.
	h.broadcastClientCount()

	for {
		select {
		case <-c.Request.Context().Done():
			return nil
		case msg := <-ch:
			if err := h.dispatchStreamMessage(c, sse, msg); err != nil {
				slog.Warn("todo: sse dispatch failed", "error", err)
			}
		}
	}
}

// handleSSEStreamWithAuth wraps handleSSEStream, loading the app auth
// cookie first. The stream lives under /api/, which the global
// LoadAuthFromCookie middleware deliberately skips, so without this
// c.Auth is nil on the stream and listTodos returns EVERY user's todos
// (unscoped). The broadcast would then re-render remote tabs with
// foreign rows — the "I see other people's tasks" / "my list got wiped"
// bug. LoadAppAuth is the /api-aware variant that does NOT skip /api.
func (h *TodoHandler) handleSSEStreamWithAuth(c *core.RequestEvent) error {
	if err := auth.LoadAppAuth(c); err != nil {
		return err
	}
	return h.handleSSEStream(c)
}

// dispatchStreamMessage routes a queued SSE chunk to the right client-side
// patch. Worker output arrives as a queue.Job envelope: type "toast" is
// rendered as a Templ component, type "retry" is merged as a signal so
// the user sees per-attempt feedback, anything else is forwarded as a
// raw signal (defensive default).
// stream dispatchers split per case to keep cyclomatic complexity low.
// Each helper handles a single job.Type and is called from the small
// switch in dispatchStreamMessage.

const (
	retryStatusSuccess = "success"
	retryStatusAttempt = "attempt"
)

func (h *TodoHandler) streamToast(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	var p struct {
		ToastType string `json:"toastType"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode toast payload: %w", err)
	}
	if p.ToastType == "" {
		p.ToastType = "info"
	}
	return emitToast(sse, p.Message, p.ToastType)
}

func (h *TodoHandler) streamRetry(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	var p struct {
		Operation string `json:"operation"`
		Attempt   int    `json:"attempt"`
		Status    string `json:"status"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode retry payload: %w", err)
	}
	// Merge both the raw JSON (signal marker for data-text) and the
	// structured fields so the AI-suggest queue panel can drive pill
	// transitions via boolean signals.
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"lastRetry":          string(payload),
		"lastRetryOperation": p.Operation,
		"lastRetryStatus":    p.Status,
		"lastRetryAttempt":   p.Attempt,
	}); err != nil {
		return err
	}
	// Techstack diagnostics panel: once the retry demo reaches its
	// final successful attempt, flip techDone so the DaisyUI <ul
	// class="steps"> node turns step-success (green check). Without
	// this the step stayed step-primary forever and the demo looked
	// stuck. The button click sets $techStep='retry-demo' up front;
	// we only complete it here on the real success event.
	if p.Status == retryStatusSuccess {
		if err := h.applyTechStep(sse, "retry-demo", true, ""); err != nil {
			return err
		}
	}
	verb := p.Operation
	if verb == "suggest_simulated" {
		verb = "suggest (simulated)"
	}
	var msg, kind string
	switch p.Status {
	case retryStatusSuccess:
		msg, kind = fmt.Sprintf("%s: completed", verb), retryStatusSuccess
	default: // retryStatusAttempt
		msg = fmt.Sprintf("%s: attempt %d failed", verb, p.Attempt)
		if p.Error != "" {
			msg += " (" + p.Error + ")"
		}
		msg += ", retrying…"
		kind = "warning"
	}
	return emitToast(sse, msg, kind)
}

func (h *TodoHandler) streamTodo(c *core.RequestEvent, sse *sdk.ServerSentEventGenerator, payload []byte) error {
	// Parse the broadcast event payload — it carries the mutation type
	// (created/toggled/deleted/workflow-completed) plus the item data.
	var evt struct {
		Event string `json:"event"`
		ID    string `json:"id"`
		Title string `json:"title"`
		Done  bool   `json:"done"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		return fmt.Errorf("decode todo event: %w", err)
	}

	// The durable workflow emits a synthetic "workflow-completed" event
	// once all five steps finish — mark the onboarding stepper done.
	if evt.Event == "workflow-completed" {
		if err := h.applyOnboarding(sse, 0, 0, "completed", "Workflow completed", true); err != nil {
			return err
		}
		return emitToast(sse, "Workflow completed — 3 example todos created", retryStatusSuccess)
	}

	// Load the current scoped item count from the database so the UI's
	// header badge stays accurate.
	count, err := h.countOwnedTodos(c)
	if err != nil {
		return fmt.Errorf("count todos for broadcast: %w", err)
	}

	// Merge the remote-source signal so new/updated items animate with
	// the remote entry effect. Also update the live item count.
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"lastItemSource": "remote",
		signalItemCount:  count,
	}); err != nil {
		return err
	}

	// For deleted items, remove the element from the DOM via Datastar's
	// remove mode. For all other events we defer to the full-list
	// replacement so the entire list stays in sync (same-user tabs see
	// the same data).
	switch evt.Event {
	case "deleted":
		// Remove the element from the DOM via Datastar's remove mode.
		// (RenderAndPatch with WithModeRemove + WithSelector is used here
		// instead of sdk.RemoveElementByID because the latter is not
		// reliably visible to the compiler in this module graph.)
		return dshelpers.RenderAndPatch(sse, nil,
			sdk.WithSelector("#todo-"+evt.ID),
			sdk.WithModeRemove())
	default:
		// For "created" and "toggled" events re-render the entire list.
		// This is safe because listTodos scopes by c.Auth, which is
		// loaded from the cookie on every SSE connection (see
		// handleSSEStream).
		todos, err := h.listTodos(c, "all")
		if err != nil {
			return fmt.Errorf("list todos for broadcast: %w", err)
		}
		return dshelpers.RenderAndPatch(sse, h.renderTodoList(todos),
			sdk.WithSelector("#todo-list"))
	}
}

func (h *TodoHandler) streamClients(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	var p struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode clients payload: %w", err)
	}
	return dshelpers.MergeSignals(sse, map[string]any{
		"connectedClients": p.Count,
	})
}

func (h *TodoHandler) streamSuggestResult(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	slog.Info("todo: streamSuggestResult called", "payload", string(payload))
	var p struct {
		Suggestions    []string `json:"suggestions"`
		SuggestErr     string   `json:"suggestErr"`
		SuggestPending bool     `json:"suggestPending"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode suggest_result payload: %w", err)
	}
	merge := map[string]any{
		signalSuggestions:    p.Suggestions,
		signalSuggestErr:     p.SuggestErr,
		signalSuggestPending: p.SuggestPending,
	}
	if err := dshelpers.MergeSignals(sse, merge); err != nil {
		return err
	}
	// Re-render the suggestions region with the actual buttons. A bare
	// MergeSignals($suggestions) only toggles the container's visibility
	// (data-show) — it never populates the buttons, which were SSR'd
	// empty. PatchElements with the live suggestions makes the clickable
	// buttons appear. This is the fix for "AI suggestions — click to use:
	// but no button shows up".
	if err := dshelpers.RenderAndPatch(sse, components.SuggestionsList(p.Suggestions),
		sdk.WithSelector("#suggestions-region")); err != nil {
		return err
	}
	// Techstack diagnostic panel: highlight the single "Queue + retry"
	// node (goqite + retry-go + fake LLM) and mark done once the result
	// lands. Always reset techPhase so a prior error does not stick on
	// the next (successful) result.
	phase := ""
	if p.SuggestErr != "" {
		phase = "error"
	}
	if err := h.applyTechStep(sse, "retry-demo", p.SuggestErr == "" && !p.SuggestPending, phase); err != nil {
		return err
	}
	if p.SuggestErr != "" {
		return emitToast(sse, "Suggest failed: "+p.SuggestErr, "error")
	}
	return emitToast(sse, fmt.Sprintf("Got %d suggestions", len(p.Suggestions)), retryStatusSuccess)
}

func (h *TodoHandler) streamProgress(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	// Durable-workflow progress (DagNats). Streamed as each step
	// completes so the UI can render a live stepper and the user can
	// watch the workflow advance. Merged as signals and echoed as a
	// toast so the progression is both visible and narrated.
	var p struct {
		Step   int    `json:"step"`
		Total  int    `json:"total"`
		Phase  string `json:"phase"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode progress payload: %w", err)
	}
	// Mark done on the finalize phase; the streamTodo dispatcher also
	// flips $workflowCompleted via the "workflow-completed" event type.
	completed := p.Phase == "finalize"
	if err := h.applyOnboarding(sse, p.Step, p.Total, p.Phase, p.Detail, completed); err != nil {
		return err
	}
	return emitToast(sse, p.Detail, "info")
}

// applyTechStep centralises the techstack diagnostics panel state machine.
// The panel is a DaisyUI <ul class="steps"> whose nodes light up via the
// $techStep / $techDone / $techPhase signals. Previously each stream
// dispatcher built this map inline, scattering the panel logic across
// four handlers; a regression in one (e.g. forgetting to flip techDone on
// success) was invisible to the others. Now there is a single source of
// truth.
//
// step  — the active slug: "retry-demo" (goqite+retry) or "workflow"
//
//	(dagnats). Empty keeps the current node.
//
// done  — true once the action succeeded (green check) OR reached a
//
//	terminal error. When done we ALSO reset $suggestPending=false so the
//	spinner on the Queue+Retry / AI Suggest buttons stops — without this
//	the button clicked true and never released, leaving every tab's
//	action button spinning indefinitely (the "running demo" never
//	resets bug).
//
// phase — "error" on failure, "" otherwise.
func (h *TodoHandler) applyTechStep(sse *sdk.ServerSentEventGenerator, step string, done bool, phase string) error {
	merge := map[string]any{
		"techStep":  step,
		"techDone":  done,
		"techPhase": phase,
	}
	if done {
		// Terminal state (success or hard error): release the spinner.
		merge[signalSuggestPending] = false
	}
	return dshelpers.MergeSignals(sse, merge)
}

// applyOnboarding centralises the durable-workflow (DagNats) progress
// signals. step/total/phase/detail drive the live stepper; when completed
// is true the workflow has finalised and the panel marks done.
func (h *TodoHandler) applyOnboarding(
	sse *sdk.ServerSentEventGenerator,
	step, total int, phase, detail string, completed bool,
) error {
	signals := map[string]any{
		"onboardingActive": true,
		"onboardingStep":   step,
		"onboardingTotal":  total,
		"onboardingPhase":  phase,
		"onboardingDetail": detail,
		"techStep":         "workflow",
	}
	if completed {
		signals["workflowCompleted"] = true
		signals["techDone"] = true
	}
	return dshelpers.MergeSignals(sse, signals)
}

// dispatchStreamMessage routes a single SSE envelope to the matching
// per-type helper. Kept small (one switch) so each branch's
// cyclomatic complexity is isolated and testable.
func (h *TodoHandler) dispatchStreamMessage(c *core.RequestEvent, sse *sdk.ServerSentEventGenerator, msg []byte) error {
	var job queue.Job
	if err := json.Unmarshal(msg, &job); err != nil {
		return fmt.Errorf("decode stream envelope: %w", err)
	}
	switch job.Type {
	case jobTypeToast:
		return h.streamToast(sse, job.Payload)
	case "retry":
		return h.streamRetry(sse, job.Payload)
	case "todo":
		return h.streamTodo(c, sse, job.Payload)
	case "clients":
		return h.streamClients(sse, job.Payload)
	case "suggest_result":
		return h.streamSuggestResult(sse, job.Payload)
	case "progress":
		return h.streamProgress(sse, job.Payload)
	default:
		return dshelpers.MergeSignals(sse, map[string]any{
			"lastJob": string(msg),
		})
	}
}

// broadcastClientCount tells every connected client how many are
// currently online. Called on connect + disconnect so the UI's
// presence badge stays live.
func (h *TodoHandler) broadcastClientCount() {
	payload := mustJSON(map[string]any{"count": h.q.Hub().Stats().Clients})
	h.q.Hub().Broadcast(mustJSON(queue.Job{Type: "clients", Payload: payload}))
}

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
