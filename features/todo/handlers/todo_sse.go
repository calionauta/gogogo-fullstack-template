// SCOPE:layer=feature,removal=feature — Todo MVC example (reference implementation)
package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
	sdk "github.com/starfederation/datastar-go/datastar"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/components"
	ic "github.com/calionauta/gogogo-fullstack-template/internal/components"
	dshelpers "github.com/calionauta/gogogo-fullstack-template/internal/datastar"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

//nolint:gocyclo // SSE lifecycle is inherently sequential.
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
	ch := make(chan []byte, config.DefaultClientQueueSize)
	h.q.Hub().Register(clientID, ownerOf(c), ch)
	slog.Info("todo: sse registered",
		"clientID", clientID, "userID", ownerOf(c), "total_users",
		h.q.Hub().CountUserClients())
	defer func() {
		// Use UnregisterIfCurrent to prevent a stale deferred cleanup from
		// a previous EventSource connection from wiping out the new handler's
		// registration (race on reconnect).
		h.q.Hub().UnregisterIfCurrent(clientID, ch)
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
		AIStep:           0,
		AIPending:        false,
		AIPhase:          "",
	}); err != nil {
		return err
	}
	// Tell every connected client how many are online now.
	h.broadcastClientCount()

	// Heartbeat ticker: SSE handlers only detect client disconnection when
	// they try to write to the response; a handler blocked on <-ch would
	// never learn the client disconnected and would leak a goroutine and a
	// registered hub client indefinitely. The heartbeat write forces Go's
	// HTTP server to detect the closed connection and cancel the context.
	// We write a comment line (: heartbeat) which the SSE spec defines as
	// a no-op that clients must ignore, so it is invisible to Datastar.
	flusher, canFlush := c.Response.(http.Flusher)
	heartbeat := time.NewTicker(config.DefaultSSEHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return nil
		case <-heartbeat.C:
			// Skip heartbeat if the response writer doesn't support flushing;
			// writing without flushing would leave data buffered and could
			// interleave with subsequent Datastar SSE events, corrupting the
			// stream. In practice Go's HTTP/1.1 Response always implements
			// Flusher, so this is a defensive guard for edge cases.
			if !canFlush {
				continue
			}
			if _, err := fmt.Fprintf(c.Response, ": heartbeat\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
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

// retryEvent is the parsed payload of a retry / AI-Suggest broadcast.
type retryEvent struct {
	Operation string `json:"operation"`
	Attempt   int    `json:"attempt"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Raw       []byte `json:"-"`
}

// retrySignalFields builds the Datastar signal map for a retry / AI-Suggest
// event. AI Suggest jobs drive a dedicated stepper (aiStep/aiPending); the
// Queue + Retry demo drives demoStep — running one never lights the other.
func retrySignalFields(p retryEvent) map[string]any {
	fields := map[string]any{
		"lastRetry":          string(p.Raw),
		"lastRetryOperation": p.Operation,
		"lastRetryStatus":    p.Status,
		"lastRetryAttempt":   p.Attempt,
	}
	isSuggest := p.Operation == signalJobSuggest || p.Operation == signalJobSuggestSimulated
	if isSuggest {
		if p.Status == retryStatusSuccess {
			fields["aiStep"] = 3
			fields["aiPending"] = false
		} else {
			fields["aiStep"] = 2
			fields["aiPending"] = true
		}
		return fields
	}
	fields["demoStep"] = 2
	if p.Status == retryStatusSuccess {
		fields["demoStep"] = 3
	}
	return fields
}

// retryToastMessage returns the user-facing toast text and kind for a
// retry / AI-Suggest event.
func retryToastMessage(p retryEvent) (msg, kind string) {
	verb := p.Operation
	if verb == signalJobSuggestSimulated {
		verb = "suggest (simulated)"
	}
	switch p.Status {
	case retryStatusSuccess:
		return fmt.Sprintf("%s: completed", verb), retryStatusSuccess
	default:
		msg = fmt.Sprintf("%s: attempt %d failed", verb, p.Attempt)
		if p.Error != "" {
			msg += " (" + p.Error + ")"
		}
		msg += ", retrying…"
		return msg, "warning"
	}
}

func (h *TodoHandler) streamRetry(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	var p retryEvent
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode retry payload: %w", err)
	}
	p.Raw = payload
	isSuggest := p.Operation == signalJobSuggest || p.Operation == signalJobSuggestSimulated
	if err := dshelpers.MergeSignals(sse, retrySignalFields(p)); err != nil {
		return err
	}
	if p.Status == retryStatusSuccess && !isSuggest {
		if err := h.applyTechStep(sse, signalJobRetryDemo, true, ""); err != nil {
			return err
		}
	}
	msg, kind := retryToastMessage(p)
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
		if err := h.applyOnboarding(sse, 0, 0, onbStatusCompleted, "Workflow completed", true); err != nil {
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
		// We call sse.PatchElements directly with an empty element body +
		// WithModeRemove + WithSelector: RenderAndPatch(nil, ...) would call
		// component.Render() on a nil component and error out before emitting
		// the patch, so the element would never be removed.
		return sse.PatchElements("", sdk.WithModeRemove(), sdk.WithSelector("#todo-"+evt.ID))
	default:
		// For "created" and "toggled" events re-render the entire list.
		// This is safe because listTodos scopes by c.Auth, which is
		// loaded from the cookie on every SSE connection (see
		// handleSSEStream). The skin is read from the same SSE
		// connection's URL — every client opening an SSE stream is
		// expected to forward their current `?skin=` query param so the
		// broadcast HTML matches the chrome they're rendering
		// (morpheus.TodoListRegion for morpheus clients, basecoat for
		// basecoat, components for DaisyUI). The morpheus and basecoat
		// page templates already do this; without it the broadcast
		// would replace the client's rows with mismatched HTML on
		// every remote mutation (CAL-14).
		todos, err := h.listTodos(c, "all")
		if err != nil {
			return fmt.Errorf("list todos for broadcast: %w", err)
		}
		return dshelpers.RenderAndPatch(sse, h.renderTodoList(todos, h.resolveSkin(c)),
			sdk.WithSelector("#todo-list"))
	}
}

// streamDocVersionBumped handles the cross-store "doc version
// bumped" envelope produced by crdtstore.HubPublisher. Merges the
// $docVersion signal so client-side scripts can react (re-fetch
// the fragment, update the presence badge, etc.) without a full
// page reload.
//
// This is the Phase 3 callback that closes the loop:
//
//	Create (peer) → transport → ApplyRemoteOp → bumpVersion
//	  → publisher.PublishDocEvent → Hub.BroadcastToUser
//	  → SSE handler (this method) → client signal → resync.
//
// Payload: {"version": <uint64>, "owner": <ownerID>}. The owner is
// informational — the SSR subscriber is already scoped to one
// user — but logs it so drift across tenants is visible.
func (h *TodoHandler) streamDocVersionBumped(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	var p struct {
		Version uint64 `json:"version"`
		Owner   string `json:"owner"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode doc-version-bumped payload: %w", err)
	}
	if p.Version == 0 {
		return nil
	}
	if err := dshelpers.MergeSignals(sse, map[string]any{
		"docVersion":     p.Version,
		"docVersionSeen": time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	slog.Debug("todo: doc-version-bumped", "version", p.Version, "owner", p.Owner)
	return nil
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
	phase := ""
	if p.SuggestErr != "" {
		phase = "error"
	}
	// AI Suggest stepper lives on its own signals (aiStep/aiPending/
	// aiPhase) so running the Queue + Retry demo never lights this tab's
	// "Suggestions ready" status. We deliberately do NOT call applyTechStep
	// (which would set the shared techStep/suggestPending/techDone used by
	// the Queue + Retry demo).
	merge := map[string]any{
		signalSuggestions: p.Suggestions,
		signalSuggestErr:  p.SuggestErr,
		signalAiStep:      3,
		signalAiPending:   false,
		signalAiPhase:     phase,
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
	if p.SuggestErr != "" {
		return emitToast(sse, "Suggest failed: "+p.SuggestErr, phaseError)
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
	completed := p.Phase == onbPhaseFinalize
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
		"techStep":         onbPhaseWorkflow,
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
	case jobTypeSuggestResult:
		return h.streamSuggestResult(sse, job.Payload)
	case "progress":
		return h.streamProgress(sse, job.Payload)
	case "doc-version-bumped":
		return h.streamDocVersionBumped(sse, job.Payload)
	default:
		return dshelpers.MergeSignals(sse, map[string]any{
			"lastJob": string(msg),
		})
	}
}

// broadcastClientCount tells every connected client how many todo
// users are currently online. Uses CountUserClients (not Stats().Clients)
// so whiteboard SSE connections (which register on the same hub with an
// empty userID) do not inflate the count. Called on connect + disconnect
// so the UI's presence badge stays live.
func (h *TodoHandler) broadcastClientCount() {
	payload := mustJSON(map[string]any{"count": h.q.Hub().CountUserClients()})
	h.q.Hub().Broadcast(mustJSON(queue.Job{Type: "clients", Payload: payload}))
} // EmitToast renders a toast component and appends it to the

// toast-container. The toast's open state, dismiss timer, and progress
// bar are all driven by Datastar attributes on the rendered template.
func emitToast(sse *sdk.ServerSentEventGenerator, message, toastType string) error {
	return dshelpers.RenderAndPatch(
		sse,
		ic.Toast(message, toastType, ic.NewToastID()),
		sdk.WithSelectorID("toast-container"),
		sdk.WithModeAppend(),
	)
}
