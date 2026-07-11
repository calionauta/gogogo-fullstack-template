package fakeserver_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
	"github.com/calionauta/gogogo-fullstack-template/internal/llm/fakeserver"
)

// TestFakeserver_HappyPath proves the canned response round-trips.
// This is the bare minimum: real HTTP call to a real httptest server
// via GoAI, exercising wire format, auth, and response parsing.
func TestFakeserver_HappyPath(t *testing.T) {
	srv := fakeserver.New(t, fakeserver.WithResponse("hello world"))
	defer srv.Close()

	t.Setenv("GOAI_BASE_URL", srv.URL)
	c := llm.New("test-key")
	out, err := c.Chat(context.Background(), "ignored prompt")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "hello world" {
		t.Errorf("got %q, want %q", out, "hello world")
	}
	if got := srv.CallCount(); got != 1 {
		t.Errorf("CallCount = %d, want 1", got)
	}
}

// TestFakeserver_BadKey proves auth is enforced when WithAPIKey
// restricts accepted keys. A wrong key gets 401 from the fake; the
// real GoAI code should propagate that as an error.
func TestFakeserver_BadKey(t *testing.T) {
	srv := fakeserver.New(
		t,
		fakeserver.WithAPIKey("only-this-key-works"),
		fakeserver.WithResponse("ok"),
	)
	defer srv.Close()

	t.Setenv("GOAI_BASE_URL", srv.URL)
	c := llm.New("wrong-key")
	_, err := c.Chat(context.Background(), "ignored")
	if err == nil {
		t.Fatal("expected error with wrong API key, got nil")
	}
	// We don't assert on the exact error message (GoAI's wording may
	// change); just that we got an error.
}

// TestFakeserver_StreamingChunkSequence proves the streaming
// protocol works: each chunk arrives as a separate SSE event, in
// order, terminated by data: [DONE]. We exercise it via Client.ChatStream
// which is the path the todo AI-suggest handler uses.
func TestFakeserver_StreamingChunkSequence(t *testing.T) {
	srv := fakeserver.New(t, fakeserver.WithStreamChunks("hello ", "world", "!"))
	defer srv.Close()

	t.Setenv("GOAI_BASE_URL", srv.URL)
	c := llm.New("test-key")
	var got strings.Builder
	if err := c.ChatStream(context.Background(), "ignored", func(chunk string) error {
		got.WriteString(chunk)
		return nil
	}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got.String() != "hello world!" {
		t.Errorf("streamed = %q, want %q", got.String(), "hello world!")
	}
}

// TestFakeserver_RetryOnTransientErrors proves the retry layer
// actually retries 500s. The fake returns 500, 500, 200; the
// internal retry config (Attempts=3 by default) should drive the
// call 3 times total. We don't expose SetRetry publicly; the test
// relies on the documented default of 3 attempts.
func TestFakeserver_RetryOnTransientErrors(t *testing.T) {
	srv := fakeserver.New(
		t,
		fakeserver.WithStatusSequence(500, 500, 200),
		fakeserver.WithResponse("recovered"),
	)
	defer srv.Close()

	t.Setenv("GOAI_BASE_URL", srv.URL)
	c := llm.New("test-key")
	out, err := c.Chat(context.Background(), "ignored")
	if err != nil {
		t.Fatalf("Chat: %v (should have recovered after retries)", err)
	}
	if out != "recovered" {
		t.Errorf("got %q, want %q", out, "recovered")
	}
	if got := srv.CallCount(); got != 3 {
		t.Errorf("CallCount = %d, want 3 (500, 500, 200)", got)
	}
}

// TestFakeserver_ContextDeadlineExceeded proves the call honors
// context cancellation. The fake sleeps longer than the deadline;
// the client should give up promptly and surface the error.
func TestFakeserver_ContextDeadlineExceeded(t *testing.T) {
	srv := fakeserver.New(
		t,
		fakeserver.WithResponseDelay(2*time.Second),
		fakeserver.WithResponse("too late"),
	)
	defer srv.Close()

	t.Setenv("GOAI_BASE_URL", srv.URL)
	c := llm.New("test-key")

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Chat(ctx, "ignored")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// The error message must mention context deadline so operators
	// can debug.
	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("error %q does not mention context/deadline", err.Error())
	}
	// Must have given up well before the fake's 2s sleep.
	if elapsed > 1*time.Second {
		t.Errorf("took %v; should have given up after the 200ms deadline", elapsed)
	}
}

// TestFakeserver_ConcurrentRequests proves the fake is goroutine-safe.
// We hammer it with N concurrent Chat calls and assert the count is N.
func TestFakeserver_ConcurrentRequests(t *testing.T) {
	srv := fakeserver.New(t, fakeserver.WithResponse("ok"))
	defer srv.Close()

	t.Setenv("GOAI_BASE_URL", srv.URL)
	c := llm.New("test-key")
	const n = 20
	var done atomic.Int32
	for range n {
		go func() {
			_, err := c.Chat(context.Background(), "x")
			if err != nil {
				t.Errorf("concurrent Chat: %v", err)
			}
			done.Add(1)
		}()
	}
	// Simple polling wait; tests aren't expected to be perfect.
	deadline := time.Now().Add(2 * time.Second)
	for done.Load() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := srv.CallCount(); got != int64(n) {
		t.Errorf("CallCount = %d, want %d", got, n)
	}
}
