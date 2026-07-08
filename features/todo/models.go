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

	// Suggestions is the latest AI-suggested completions, populated
	// by POST /api/todos/suggest. Empty when no suggestions or
	// LLMEnabled is false.
	Suggestions []string `json:"suggestions"`
	// SuggestErr surfaces a human-readable error from the LLM
	// provider (without leaking internals). Empty on success.
	SuggestErr string `json:"suggestErr"`
}
