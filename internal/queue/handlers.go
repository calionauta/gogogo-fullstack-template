package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Job is the envelope every queued message must follow so the worker
// pool can route it to the right handler and stream progress to the
// right SSE client. Producers populate Type, ClientID, and Payload;
// the worker pool handles the rest.
//
// The Type field selects the registered handler. ClientID tells the
// SSE Hub which browser tab to stream chunks to (empty = broadcast,
// which is what the current "notification" path does).
type Job struct {
	Type     string          `json:"type"`
	ClientID string          `json:"clientID"`
	Payload  json.RawMessage `json:"payload"`
}

// Handler processes a single Job. It must be safe for concurrent calls
// (the worker pool invokes it from N goroutines) and must respect ctx
// cancellation. To stream progress to the SSE client, call hub.Send
// (or hub.Broadcast when ClientID is empty). Returning a non-nil error
// triggers retry per the worker's RetryConfig.
//
// A handler that wants to chunk its output should call hub.Send
// repeatedly from inside its loop; the SSE Hub delivers each chunk as
// a separate Datastar patch-elements event.
type Handler func(ctx context.Context, hub *SSEHub, job Job) error

// HandlerRegistry maps job types to handlers. Producers can register
// handlers at startup; the worker pool consults the registry each time
// it dequeues a message. NotFoundHandler is invoked when a message
// has an unknown type — defaults to a logger-only handler so unknown
// jobs surface in observability rather than being silently dropped.
type HandlerRegistry struct {
	mu              sync.RWMutex
	handlers        map[string]Handler
	notFoundHandler Handler
}

// NewHandlerRegistry returns an empty registry. Register at least one
// handler before starting the worker pool, otherwise every job hits
// the not-found handler.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: make(map[string]Handler),
		notFoundHandler: func(_ context.Context, _ *SSEHub, job Job) error {
			return fmt.Errorf("queue: no handler registered for type %q", job.Type)
		},
	}
}

// Register associates a handler with a job type. Panics on duplicate
// registration because that's a programmer error, not a runtime one.
func (r *HandlerRegistry) Register(jobType string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[jobType]; exists {
		panic(fmt.Sprintf("queue: handler already registered for type %q", jobType))
	}
	r.handlers[jobType] = h
}

// Lookup returns the handler for jobType, or the not-found handler.
func (r *HandlerRegistry) Lookup(jobType string) Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if h, ok := r.handlers[jobType]; ok {
		return h
	}
	return r.notFoundHandler
}

// SetNotFoundHandler overrides the default not-found handler. Useful
// in tests that want to verify unknown-job behavior.
func (r *HandlerRegistry) SetNotFoundHandler(h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notFoundHandler = h
}

// DecodeJob parses raw bytes (the body of a queued message) into a Job.
// Returns an error when the bytes aren't valid JSON or don't match the
// envelope shape — the caller should treat this as a non-retryable
// failure (drop the message, log loudly) rather than retrying forever.
func DecodeJob(body []byte) (Job, error) {
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		return Job{}, fmt.Errorf("queue: decode job: %w", err)
	}
	return job, nil
}
