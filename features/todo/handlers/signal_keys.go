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
)
