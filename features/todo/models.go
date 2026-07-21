// SCOPE:layer=feature,removal=feature — Todo MVC example (reference implementation)
// To remove: delete features/todo/ and the todo block in router/router.go Init().
package todo

import "time"

type Todo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Completed bool      `json:"completed"`
	CreatedAt time.Time `json:"created"`
	UpdatedAt time.Time `json:"updated"`
}

type Signals struct {
	Todos     []Todo `json:"todos"`
	NewTitle  string `json:"newTitle"`
	Filter    string `json:"filter"` // "all", "active", "completed"
	EditingID string `json:"editingId"`
	EditTitle string `json:"editTitle"`
	Loading   bool   `json:"loading"`
	ItemCount int    `json:"itemCount"`

	// ConfirmDelete is the id of the todo whose delete-confirm modal is
	// currently open. Empty string means no modal is shown. The title
	// is mirrored into ConfirmDeleteTitle so the modal body can name the
	// row without re-fetching it.
	ConfirmDelete      string `json:"confirmingDeleteId"`
	ConfirmDeleteTitle string `json:"confirmingDeleteTitle"`
	Deleting           bool   `json:"deleting"`

	// TechStep + TechDone + TechPhase drive the techstack diagnostics
	// <ul class="steps"> component. TechStep is the slug of the most
	// recent action ("suggest", "retry-demo", "workflow"); TechDone
	// flips true once success arrives; TechPhase surfaces "error".
	TechStep  string `json:"techStep"`
	TechDone  bool   `json:"techDone"`
	TechPhase string `json:"techPhase"`
	// DemoStep drives the progressive lighting of the Queue + Retry demo
	// steps (1 = enqueued, 2 = running/retrying, 3 = completed). It is
	// advanced by the per-attempt SSE "retry" feedback so the steps
	// light one-by-one with a visible gap between attempts instead of
	// all flipping at once when the job finishes.
	DemoStep int `json:"demoStep"`

	// AIStep, AIPending, AIPhase drive the *AI Suggest* stepper, kept
	// deliberately separate from the Queue + Retry demo stepper
	// (TechStep/SuggestPending/TechPhase). They were previously shared,
	// which made running the Queue + Retry demo light up the AI Suggest
	// tab's "Suggestions ready" status erroneously. Seeded so the
	// stepper is inert on first paint.
	AIStep    int    `json:"aiStep"`
	AIPending bool   `json:"aiPending"`
	AIPhase   string `json:"aiPhase"`
	// ClientID is the SSE client id assigned when the stream opens. The
	// UI uses it to route queued-job results and retry feedback back to
	// the correct browser tab, so a Suggest triggered here lands here
	// rather than broadcasting to every connected client.
	//
	// NOTE: the JSON key is intentionally "clientID" (not the
	// tagliatelle-preferred "clientId") — it is a public Datastar signal
	// name bound by the frontend, so renaming would break the wire contract.
	ClientID string `json:"clientID"` //nolint:tagliatelle // public Datastar signal name, do not rename
	// LLMEnabled reflects whether the server was started with a
	// non-empty GOAI_API_KEY. When true, the UI renders the "Suggest"
	// button. When false, the entire AI pathway is hidden.
	LLMEnabled bool `json:"llmEnabled"`

	// SimulatedLLM reflects whether the server was started with
	// SIMULATE_LLM=true. When true, the UI renders the "Suggest
	// (simulated)" button, which exercises the same queue + retry path
	// as Suggest but against an in-process fake LLM (no API key needed).
	//
	// NOTE: the JSON key is intentionally "simulatedLLM" (not the
	// tagliatelle-preferred "simulatedLlm") — it is a public Datastar
	// signal name bound by the frontend.
	SimulatedLLM bool `json:"simulatedLLM"` //nolint:tagliatelle // public Datastar signal name, do not rename

	// Suggestions is the latest AI-suggested completions, populated
	// by POST /api/todos/suggest. Empty when no suggestions or
	// LLMEnabled is false.
	Suggestions []string `json:"suggestions"`
	// SuggestErr surfaces a human-readable error from the LLM
	// provider (without leaking internals). Empty on success.
	SuggestErr string `json:"suggestErr"`

	// SuggestPending is true while a suggest job (simulated or real)
	// is in flight. Drives the button spinner + disabled state so the
	// user can't double-fire. Seeded false so the button is enabled
	// on first paint (Datastar's data-attr:disabled treats an
	// undefined signal as falsy, but seeding it explicitly avoids a
	// one-frame flash of the disabled state).
	SuggestPending bool `json:"suggestPending"`

	// ConnectedClients is the number of browser tabs/clients currently
	// connected to the SSE stream. Streamed on connect and on every
	// connect/disconnect so the UI can show realtime presence.
	ConnectedClients int `json:"connectedClients"`

	// LastItemSource tags the most recent todo mutation as either
	// "self" (the user who triggered the HTTP request) or "remote"
	// (a broadcast from another client). The TodoItem template uses
	// this to pick an entry animation (top-down for self, left-slide +
	// pulse for remote) and a highlight tint, so the user can tell at
	// a glance which todos came from them vs. from others. Empty before
	// the first mutation.
	LastItemSource string `json:"lastItemSource"`

	// DagNatsEnabled reflects whether the DagNats engine is compiled in
	// (always, in the unified build) AND started with DAGNATS_ENABLED=true.
	// When true, the UI renders the "Run durable workflow" button.
	DagNatsEnabled bool `json:"dagNatsEnabled"`

	// RealtimeKind describes the active broadcast transport so the
	// diagnostics panel can label it accurately: "JetStream" when the
	// NATS is enabled and a JetStream context is wired, else "in-memory".
	// The default build uses the InMemoryBroadcaster.
	RealtimeKind string `json:"realtimeKind"`

	// StoreLabel describes the active EntityStore strategy (e.g.
	// "PocketBase records"). Surfaced in the page sub-header so a
	// tester can verify which persistence mode is running without
	// grep-ing logs.
	StoreLabel string `json:"storeLabel"`

	// OfflineLabel describes the offline-sync behavior of the active
	// strategy (e.g. whether SW queues mutations, whether cross-
	// instance sync is on). Surfaced in the page sub-header.
	OfflineLabel string `json:"offlineLabel"`

	// SidebarTab selects the active async-demo tab in the right sidebar:
	// "queue" (Queue + Retry) or "workflow" (Durable Workflow). Seeded
	// "queue" so the first tab is active on paint; the Durable Workflow
	// tab is only shown when DagNatsEnabled is true.
	SidebarTab string `json:"sidebarTab"`

	// Onboarding progress (DagNats durable workflow). Streamed as the
	// workflow advances so the UI can render a live stepper. Each event
	// carries the current step, total steps, phase, and a human-readable
	// detail. OnboardingActive flips true on the first progress event and
	// stays true so the stepper remains visible until the next run resets
	// it — this is what makes the durable, restart-tolerant nature of the
	// workflow observable in the UI.
	OnboardingActive bool   `json:"onboardingActive"`
	OnboardingStep   int    `json:"onboardingStep"`
	OnboardingTotal  int    `json:"onboardingTotal"`
	OnboardingPhase  string `json:"onboardingPhase"`
	OnboardingDetail string `json:"onboardingDetail"`
	// WorkflowCompleted is set true when the durable onboarding
	// workflow finishes all five steps. The UI renders a final alert
	// so the user gets an explicit completion signal (not just the
	// stepper reaching 5/5).
	WorkflowCompleted bool `json:"workflowCompleted"`
}
