package todo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calionauta/cali-go-stack/internal/queue"
)

// sseTestTimeout caps how long any individual SSE-driven test waits for
// the worker pool to deliver. Generous enough to absorb CI noise without
// flaking local runs.
const sseTestTimeout = 5 * time.Second

// sseBufferSize is the read buffer for the SSE stream pump. Matches the
// goqite channel buffer so each Read pulls at most one full event.
const sseBufferSize = 4096

// clientIDSuffixFormat is a stable per-second suffix so the same test
// run yields stable clientIDs (useful when debugging SSE traffic dumps).
const clientIDSuffixFormat = "150405.000"

// TestIntegration_CreateEnqueuesNotification opens an SSE stream, creates
// a todo via HTTP, and asserts the "todo_created" notification arrives
// on the stream within a reasonable timeout. Exercises the full path:
//
//	HTTP POST → handler → goqite Enqueue → Hub Broadcast → SSE stream
func TestIntegration_CreateEnqueuesNotification(t *testing.T) {
	base, _, _, cleanup := testFixture(t)
	defer cleanup()

	ctx := newTestCtx(t)
	clientID := "test-client-" + time.Now().Format(clientIDSuffixFormat)

	stream := openSSE(t, base, clientID, sseTestTimeout)
	defer func() { _ = stream.Body.Close() }()

	// Give the SSE handler a beat to register the client with the hub
	// before the create handler fires its notification.
	time.Sleep(100 * time.Millisecond)

	mustPost(ctx, t, base, "/api/todos", url.Values{"title": {"eggs"}}, 200)

	full := pumpSSE(t, stream, sseTestTimeout, "todo_created")

	if !strings.Contains(full, "toast-container") {
		t.Fatalf("SSE notification missing #toast-container selector: %s", full)
	}
	if !strings.Contains(full, "eggs") {
		t.Fatalf("SSE notification missing todo title 'eggs': %s", full)
	}
	if !strings.Contains(full, "alert-success") {
		t.Fatalf("SSE notification missing alert-success class: %s", full)
	}
}

// TestIntegration_CreateEmitsToast verifies the asynchronous toast
// emitted by the worker after handleCreate enqueues a "todo_created"
// job. The HTTP response itself only patches the todo list; the toast
// arrives via the SSE stream once the worker picks up the job. This
// exercises the full HTTP → queue → worker → SSE pipeline that the
// SSE-aware retry path is designed for.
func TestIntegration_CreateEmitsToast(t *testing.T) {
	base, _, _, cleanup := testFixture(t)
	defer cleanup()

	clientID := "create-toast-client-" + time.Now().Format(clientIDSuffixFormat)

	ctx, cancel := context.WithTimeout(context.Background(), sseTestTimeout)
	defer cancel()

	stream := openSSEWithCtx(ctx, t, base, clientID)
	defer func() { _ = stream.Body.Close() }()

	// Give the SSE handler a moment to register the client with the Hub.
	time.Sleep(100 * time.Millisecond)

	// Trigger create with the matching clientID so the worker routes the
	// "todo_created" job to this specific stream. Hardcode localhost
	// for the create target so gosec's G107 (URL constructed from
	// untrusted string) doesn't trip on the dynamic clientID suffix.
	createURL := "http://127.0.0.1" + base[len("http://127.0.0.1"):] + "/api/todos?clientID=" + clientID
	resp, err := postForm(ctx, createURL, url.Values{"title": {"wash dishes"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("create status=%d", resp.StatusCode)
	}

	full := pumpSSEUntil(t, stream, 2*time.Second, func(s string) bool {
		return strings.Contains(s, "wash dishes") && strings.Contains(s, "alert-success")
	})

	if !strings.Contains(full, "toast-container") {
		t.Fatalf("SSE toast missing #toast-container selector: %s", full)
	}
	if !strings.Contains(full, "toast-timer-bar") {
		t.Fatalf("SSE toast missing progress bar: %s", full)
	}
}

// TestIntegration_RetryFeedbackExercisesSSE verifies the SSE-aware
// retry path: a handler that fails the first 2 attempts and succeeds
// on the 3rd emits per-attempt feedback to the SSE Hub. This proves
// the HTTP → queue → worker → SSE pipeline is wired correctly with
// exponential backoff + SSE feedback.
func TestIntegration_RetryFeedbackExercisesSSE(t *testing.T) {
	base, q, _, cleanup := testFixture(t)
	defer cleanup()

	var attempts atomic.Int32
	q.Registry().Register("flaky_retry", func(_ context.Context, _ *queue.SSEHub, _ queue.Job) error {
		n := attempts.Add(1)
		if n < 3 {
			return fmt.Errorf("transient failure #%d", n)
		}
		return nil
	})

	clientID := "retry-feedback-" + time.Now().Format(clientIDSuffixFormat)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := openSSEWithCtx(ctx, t, base, clientID)
	defer func() { _ = stream.Body.Close() }()

	time.Sleep(100 * time.Millisecond)

	job := queue.Job{Type: "flaky_retry", ClientID: clientID, Payload: []byte(`{}`)}
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	if err := q.Enqueue(ctx, body); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	full := pumpSSEUntil(t, stream, 10*time.Second, func(_ string) bool {
		return attempts.Load() >= 3
	})

	t.Logf("SSE stream dump (last 500 chars): %s", tailString(full, 500))
	t.Logf("attempts=%d", attempts.Load())

	if got := attempts.Load(); got < 3 {
		t.Fatalf("flaky handler ran %d times, want >= 3", got)
	}

	// Count distinct retry feedback chunks. Embedded JSON uses escaped
	// quotes inside the outer JSON string, so match on attempt counters
	// directly.
	seenAttempts := 0
	if strings.Contains(full, `"lastRetry":`) {
		if strings.Contains(full, `\\"attempt\\":1`) || strings.Contains(full, `\"attempt\":1`) {
			seenAttempts = 1
		}
		if strings.Contains(full, `\\"attempt\\":2`) || strings.Contains(full, `\"attempt\":2`) {
			seenAttempts = 2
		}
	}
	if seenAttempts < 2 {
		t.Fatalf("expected >= 2 retry feedback chunks on SSE stream, saw %d (stream: %s)",
			seenAttempts, tailString(full, 500))
	}
}

// openSSE opens the SSE stream with a fresh context derived from the
// provided timeout. Used by tests that don't need to share the context.
func openSSE(t *testing.T, base, clientID string, timeout time.Duration) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return openSSEWithCtx(ctx, t, base, clientID)
}

// openSSEWithCtx opens the SSE stream under the provided context. Used
// when the caller needs to share the context across multiple calls.
func openSSEWithCtx(ctx context.Context, t *testing.T, base, clientID string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/todos/stream?clientID="+clientID, nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		t.Fatalf("SSE status=%d", resp.StatusCode)
	}
	return resp
}

// pumpSSE reads from the SSE stream until the predicate returns true
// or the timeout expires. Returns everything accumulated.
func pumpSSE(t *testing.T, stream *http.Response, timeout time.Duration, mustContain string) string {
	t.Helper()
	return pumpSSEUntil(t, stream, timeout, func(s string) bool {
		return strings.Contains(s, mustContain)
	})
}

// pumpSSEUntil reads the SSE stream until the predicate returns true
// or the timeout expires. The accumulated bytes are returned so
// callers can run multiple substring assertions on the full transcript.
func pumpSSEUntil(t *testing.T, stream *http.Response, timeout time.Duration, stop func(string) bool) string {
	t.Helper()
	buf := make([]byte, sseBufferSize)
	full := ""
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := stream.Body.Read(buf)
		if n > 0 {
			full += string(buf[:n])
			if stop(full) {
				return full
			}
		}
		if err != nil {
			break
		}
	}
	return full
}

// tailString returns the last n bytes of s, or all of s if shorter.
// Used by Logf calls so test output doesn't drown in stream dumps.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
