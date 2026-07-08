package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/calionauta/gogogo-template/config"
)

func TestQueueEnqueueAndReceive(t *testing.T) {
	dir := t.TempDir()
	q, err := New(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer q.Close()

	job := Job{Type: "test", Payload: json.RawMessage(`{"hello":"world"}`)}
	body := mustMarshal(t, job)
	if err = q.Enqueue(context.Background(), body); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	msg, err := q.ReceiveAndWait(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("ReceiveAndWait: %v", err)
	}
	var got Job
	if err := json.Unmarshal(msg.Body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != "test" {
		t.Fatalf("expected type test, got %q", got.Type)
	}
	if string(got.Payload) != `{"hello":"world"}` {
		t.Fatalf("unexpected payload: %s", got.Payload)
	}
}

func TestQueueWorkerDispatchesToRegistry(t *testing.T) {
	dir := t.TempDir()
	q, err := New(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer q.Close()

	gotCh := make(chan Job, 1)
	q.Registry().Register("ping", func(_ context.Context, _ *SSEHub, job Job) error {
		gotCh <- job
		return nil
	})

	job := Job{Type: "ping", Payload: json.RawMessage(`{}`)}
	body := mustMarshal(t, job)
	if err = q.Enqueue(context.Background(), body); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	wp := q.StartWorkers()
	defer wp.Stop()

	select {
	case got := <-gotCh:
		if got.Type != "ping" {
			t.Fatalf("handler got wrong type: %q", got.Type)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not dispatch job within timeout")
	}
}

func TestQueueWorkerRetriesOnError(t *testing.T) {
	dir := t.TempDir()
	q, err := New(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer q.Close()

	var attempts int
	done := make(chan struct{})
	q.Registry().Register("flaky", func(_ context.Context, _ *SSEHub, _ Job) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient")
		}
		close(done)
		return nil
	})

	body := mustMarshal(t, Job{Type: "flaky"})
	if err = q.Enqueue(context.Background(), body); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Build the pool directly so we can install a fast retry config
	// BEFORE Start (StartWorkers would snapshot the default 2s backoff,
	// which is too slow for a unit test).
	wp := NewWorkerPool(q.q, q.hub, q.reg, workerCount)
	wp.SetRetry(RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond})
	wp.Start()
	defer wp.Stop()

	select {
	case <-done:
		if attempts != 3 {
			t.Fatalf("expected 3 attempts, got %d", attempts)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("flaky handler never succeeded (attempts=%d)", attempts)
	}
}

func TestQueueUnknownTypeHitsNotFoundHandler(t *testing.T) {
	dir := t.TempDir()
	q, err := New(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer q.Close()

	called := make(chan struct{}, 1)
	q.Registry().SetNotFoundHandler(func(_ context.Context, _ *SSEHub, _ Job) error {
		select {
		case called <- struct{}{}:
		default:
		}
		return nil
	})

	body := mustMarshal(t, Job{Type: "nope"})
	if err = q.Enqueue(context.Background(), body); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	wp := q.StartWorkers()
	defer wp.Stop()

	select {
	case <-called:
		// good
	case <-time.After(3 * time.Second):
		t.Fatal("not-found handler was not invoked for unknown type")
	}
}

func TestQueueCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	q, err := New(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q.Close()
	q.Close() // must not panic
}

// mustMarshal is a test helper that fails the test on marshal error.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
