package todo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/calionauta/cali-go-stack/internal/queue"
)

// --- SSE Hub Tests ---

func TestSSEHubRegisterAndSend(t *testing.T) {
	hub := queue.NewSSEHub()
	ch := make(chan []byte, 10)

	hub.Register("client-1", ch)
	hub.Send("client-1", []byte("hello"))

	select {
	case msg := <-ch:
		if string(msg) != "hello" {
			t.Fatalf("expected 'hello', got %q", string(msg))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestSSEHubBufferThenReplay(t *testing.T) {
	hub := queue.NewSSEHub()

	// Send before register (goes to buffer)
	hub.Send("client-late", []byte("buffered-msg"))

	// Now register — should replay
	ch := make(chan []byte, 10)
	hub.Register("client-late", ch)

	select {
	case msg := <-ch:
		if string(msg) != "buffered-msg" {
			t.Fatalf("expected 'buffered-msg', got %q", string(msg))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: buffered message not replayed")
	}
}

func TestSSEHubBroadcast(t *testing.T) {
	hub := queue.NewSSEHub()
	ch1 := make(chan []byte, 10)
	ch2 := make(chan []byte, 10)
	hub.Register("a", ch1)
	hub.Register("b", ch2)

	hub.Broadcast([]byte("broadcast!"))

	for name, ch := range map[string]chan []byte{"a": ch1, "b": ch2} {
		select {
		case msg := <-ch:
			if string(msg) != "broadcast!" {
				t.Fatalf("client %s: expected 'broadcast!', got %q", name, string(msg))
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout: client %s didn't receive broadcast", name)
		}
	}
}

func TestSSEHubBackpressure(t *testing.T) {
	t.Helper()
	hub := queue.NewSSEHub()
	// Full channel (capacity 1, already has message)
	ch := make(chan []byte, 1)
	ch <- []byte("existing")
	hub.Register("slow", ch)

	// This should not block
	hub.Send("slow", []byte("overflow"))

	// Should still have the original message
	got := <-ch
	if string(got) != "existing" {
		t.Fatalf("expected original 'existing' message, got %q", string(got))
	}
}

func TestSSEHubUnregister(t *testing.T) {
	t.Helper()
	hub := queue.NewSSEHub()
	ch := make(chan []byte, 10)
	hub.Register("gone", ch)
	hub.Unregister("gone")

	// Should not panic
	hub.Send("gone", []byte("ghost"))
	hub.Broadcast([]byte("nothing"))
}

// --- Retry-Go Configuration Tests ---

func TestDefaultRetryConfig(t *testing.T) {
	cfg := queue.DefaultRetryConfig
	if cfg.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", cfg.Attempts)
	}
	if cfg.Delay != 2*time.Second {
		t.Fatalf("expected 2s delay, got %v", cfg.Delay)
	}
	if cfg.MaxDelay != 30*time.Second {
		t.Fatalf("expected 30s max delay, got %v", cfg.MaxDelay)
	}
}

func TestRetrySucceedsOnFirstAttempt(t *testing.T) {
	cfg := queue.DefaultRetryConfig
	attempts := 0
	err := cfg.DoSilent(context.Background(), func() error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestRetryFailsAfterAllAttempts(t *testing.T) {
	cfg := queue.RetryConfig{
		Attempts: 2,
		Delay:    10 * time.Millisecond,
		MaxDelay: 100 * time.Millisecond,
	}
	attempts := 0
	err := cfg.DoSilent(context.Background(), func() error {
		attempts++
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestRetryRespectsContextCancellation(t *testing.T) {
	cfg := queue.RetryConfig{
		Attempts: 5,
		Delay:    100 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate cancellation

	err := cfg.DoSilent(ctx, func() error {
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestRetrySupportsSSEFeedback(t *testing.T) {
	hub := queue.NewSSEHub()
	ch := make(chan []byte, 10)
	hub.Register("test", ch)

	cfg := queue.RetryConfig{
		Attempts:     2,
		Delay:        10 * time.Millisecond,
		MaxDelay:     50 * time.Millisecond,
		JitterFactor: 0.0,
	}

	err := cfg.Do(context.Background(), hub, "test", "test-op", func() error {
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error")
	}

	// Should have received 2 SSE messages (one per attempt)
	received := 0
	for {
		select {
		case <-ch:
			received++
			if received == 2 {
				return // success
			}
		case <-time.After(500 * time.Millisecond):
			if received < 2 {
				t.Fatalf("expected 2 SSE retry messages, got %d", received)
			}
			return
		}
	}
}

// --- Todo Model Tests ---

func TestTodoModelDefaults(t *testing.T) {
	todo := Todo{
		Title: "Write tests",
	}
	if todo.ID != "" {
		t.Fatalf("expected empty ID by default, got %q", todo.ID)
	}
	if todo.Completed {
		t.Fatal("new todo should not be completed")
	}
	if todo.Title != "Write tests" {
		t.Fatalf("expected 'Write tests', got %q", todo.Title)
	}
}

func TestSignals(t *testing.T) {
	signals := Signals{
		Todos:     []Todo{{ID: "1", Title: "Test", Completed: false}},
		Filter:    "all",
		ItemCount: 1,
	}
	if signals.ItemCount != 1 {
		t.Fatalf("expected 1 item, got %d", signals.ItemCount)
	}
	if len(signals.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(signals.Todos))
	}
	if signals.Filter != "all" {
		t.Fatalf("expected filter 'all', got %q", signals.Filter)
	}
}

// --- HandlerRegistry Tests ---

func TestHandlerRegistryDispatch(t *testing.T) {
	reg := queue.NewHandlerRegistry()
	called := 0
	reg.Register("ping", func(_ context.Context, _ *queue.SSEHub, _ queue.Job) error {
		called++
		return nil
	})

	hub := queue.NewSSEHub()
	job := queue.Job{Type: "ping", Payload: []byte(`{}`)}
	if err := reg.Lookup("ping")(context.Background(), hub, job); err != nil {
		t.Fatalf("ping handler error: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected ping handler to fire once, got %d", called)
	}

	if err := reg.Lookup("nope")(context.Background(), hub, job); err == nil {
		t.Fatal("expected error from not-found handler")
	}
}

func TestHandlerRegistryDuplicatePanics(t *testing.T) {
	reg := queue.NewHandlerRegistry()
	reg.Register("dup", func(_ context.Context, _ *queue.SSEHub, _ queue.Job) error { return nil })
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate Register")
		}
	}()
	reg.Register("dup", func(_ context.Context, _ *queue.SSEHub, _ queue.Job) error { return nil })
}

func TestDecodeJobRoundTrip(t *testing.T) {
	in := queue.Job{Type: "todo_created", ClientID: "client-42", Payload: []byte(`{"title":"eggs"}`)}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := queue.DecodeJob(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Type != in.Type || out.ClientID != in.ClientID || string(out.Payload) != string(in.Payload) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", out, in)
	}
}

func TestDecodeJobRejectsBadJSON(t *testing.T) {
	if _, err := queue.DecodeJob([]byte("not json")); err == nil {
		t.Fatal("expected error on bad JSON")
	}
}
