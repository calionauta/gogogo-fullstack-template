package todo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// collectRetryFeedback scans an SSE transcript for Datastar `lastRetry`
// signals and returns the distinct retry events (de-duplicated by
// attempt number) plus the highest attempt seen. The retry feedback is
// emitted as a signals patch: `data: signals {"lastRetry":"<escaped json>"}`.
// We extract each `data:` payload, locate the lastRetry signal, and decode
// the embedded JSON. This avoids brittle substring matching on escaped
// quotes. Kept as a free function (not a closure) so the calling test
// stays below the gocyclo limit.
func collectRetryFeedback(transcript string) (events []map[string]any, seenAttempts int) {
	for _, ev := range parseSSEData(transcript) {
		idx := strings.Index(ev, "lastRetry")
		if idx < 0 {
			continue
		}
		// The signal value is `"<escaped json>"`; skip the `:"` delimiter
		// and parse the JSON string that follows.
		delim := strings.Index(ev[idx:], ":\"")
		if delim < 0 {
			continue
		}
		val, ok := extractJSONString(ev[idx+delim:])
		if !ok {
			continue
		}
		var p struct {
			Operation string `json:"operation"`
			Attempt   int    `json:"attempt"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal([]byte(val), &p); err != nil {
			continue
		}
		if p.Attempt > seenAttempts {
			seenAttempts = p.Attempt
			events = append(events, map[string]any{
				"operation": p.Operation, "attempt": p.Attempt, "status": p.Status,
			})
		}
	}
	return events, seenAttempts
}

// hasRetrySuccess reports whether any event carries status "success".
func hasRetrySuccess(events []map[string]any) bool {
	for _, e := range events {
		if e["status"] == "success" {
			return true
		}
	}
	return false
}

// TestIntegration_RetryFeedbackExercisesSSE verifies the SSE-aware
// retry path: a handler that fails the first 2 attempts and succeeds
// on the 3rd emits per-attempt feedback to the SSE Hub. This proves
// the HTTP → queue → worker → SSE pipeline is wired correctly with
// exponential backoff + SSE feedback.
func TestIntegration_RetryFeedbackExercisesSSE(t *testing.T) {
	base, q, _, _, cleanup := testFixture(t)
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	// Poll the SSE stream until the success feedback event arrives. That
	// proves the pipeline delivered both transient failures AND the
	// eventual success. Reading past it would race the next event.
	var retryEvents []map[string]any
	var seenAttempts int
	full := pumpSSEUntil(t, stream, 15*time.Second, func(transcript string) bool {
		retryEvents, seenAttempts = collectRetryFeedback(transcript)
		return hasRetrySuccess(retryEvents)
	})

	t.Logf("attempts=%d retry_feedback_events=%d", attempts.Load(), seenAttempts)
	for _, e := range retryEvents {
		t.Logf("retry feedback: %+v", e)
	}

	if got := attempts.Load(); got < 3 {
		t.Fatalf("flaky handler ran %d times, want >= 3", got)
	}
	if seenAttempts < 2 {
		t.Fatalf("expected >= 2 retry feedback events on SSE stream, saw %d (stream tail: %s)",
			seenAttempts, tailString(full, 600))
	}
	// The final retry event must report success, proving the pipeline
	// surfaces both the transient failures AND the eventual success.
	if len(retryEvents) == 0 || retryEvents[len(retryEvents)-1]["status"] != "success" {
		t.Fatalf("last retry event should be status=success, got %+v", retryEvents)
	}
}
