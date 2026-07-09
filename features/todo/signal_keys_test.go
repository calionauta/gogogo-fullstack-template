package todo_test

// Signal field names emitted by features/todo/handlers over SSE and matched
// against by integration tests. Defined as constants here (and in
// features/todo/handlers/signal_keys.go) so goconst can collapse the
// repeated string literals while the Datastar side keeps its own
// "$signalName" form in .templ files.
const (
	signalSuggestions    = "suggestions"
	signalSuggestErr     = "suggestErr"
	signalSuggestPending = "suggestPending"
)

// buyMilk is the canonical fixture title used by every integration test
// that needs a todo to create. It is intentionally not a public symbol
// to keep tests self-contained.
const buyMilk = "buy milk"
