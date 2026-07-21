// SCOPE:layer=feature,removal=feature — Todo MVC example (reference implementation)
package handlers

// Signal field names shared between the suggest dispatcher, the SSE
// stream helper, and the partial-result worker emission. Mirrored in
// features/todo/signal_keys_test.go so test code can reference the same
// keys (goconst collapses the repeated string literals on both sides).
const (
	signalSuggestions    = "suggestions"
	signalSuggestErr     = "suggestErr"
	signalSuggestPending = "suggestPending"
	signalItemCount      = "itemCount"

	// AI Suggest stepper signals — kept independent from the Queue +
	// Retry demo stepper (techStep/suggestPending/techPhase) so running
	// one never lights the other.
	signalAiStep    = "aiStep"
	signalAiPending = "aiPending"
	signalAiPhase   = "aiPhase"

	// Job type strings emitted by the worker's retry layer. The AI
	// Suggest jobs stream their own stepper via these operations.
	signalJobSuggest          = "suggest"
	signalJobSuggestSimulated = "suggest_simulated"
	signalJobRetryDemo        = "retry-demo"
)
