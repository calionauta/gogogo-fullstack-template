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
	// ClientID is the SSE client id assigned when the stream opens. The
	// UI uses it to route queued-job results and retry feedback back to
	// the correct browser tab, so a Suggest triggered here lands here
	// rather than broadcasting to every connected client.
	ClientID string `json:"clientID"`
	// AdminEnabled reflects whether the server was started with a
	// non-empty ADMIN_UNLOCK_TOKEN (loaded from the age-encrypted
	// secrets file). When true, the UI renders the "Admin unlock"
	// form. When false, the entire admin pathway is hidden — there is
	// no client-side check to bypass.
	AdminEnabled bool `json:"adminEnabled"`

	// LLMEnabled reflects whether the server was started with a
	// non-empty GOAI_API_KEY. When true, the UI renders the "Suggest"
	// button. When false, the entire AI pathway is hidden.
	LLMEnabled bool `json:"llmEnabled"`

	// SimulatedLLM reflects whether the server was started with
	// SIMULATE_LLM=true. When true, the UI renders the "Suggest
	// (simulated)" button, which exercises the same queue + retry path
	// as Suggest but against an in-process fake LLM (no API key needed).
	SimulatedLLM bool `json:"simulatedLLM"`

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

	// DagNatsEnabled reflects whether the binary was built with
	// -tags dagnats AND started with DAGNATS_ENABLED=true. When true,
	// the UI renders the "Run durable workflow" button.
	DagNatsEnabled bool `json:"dagNatsEnabled"`

	// RealtimeKind describes the active broadcast transport so the
	// diagnostics panel can label it accurately: "JetStream" when the
	// binary was built with -tags jetstream and NATS is enabled, else
	// "in-memory". The default build uses the InMemoryBroadcaster.
	RealtimeKind string `json:"realtimeKind"`

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
