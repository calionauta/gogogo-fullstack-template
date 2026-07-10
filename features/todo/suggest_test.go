package todo_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestIntegration_SuggestSimulatedEnqueuesAndStreamsResult drives the
// full async path keyless: POST /api/todos/suggest-simulated enqueues a
// job, the worker runs it against the in-process fake LLM (which scripts
// 500 → 200 + delay), and the suggestions stream back over SSE. This
// exercises the queue + retry + SSE pipeline end to end without a token.
func TestIntegration_SuggestSimulatedEnqueuesAndStreamsResult(t *testing.T) {
	base, _, _, _, cleanup := testFixtureSimulated(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	clientID := "suggest-sim-client-" + time.Now().Format(clientIDSuffixFormat)
	stream := openSSEWithCtx(ctx, t, base, clientID)
	defer func() { _ = stream.Body.Close() }()

	// Give the SSE handler a beat to register the client before we POST.
	time.Sleep(100 * time.Millisecond)

	createURL := "http://127.0.0.1" + base[len("http://127.0.0.1"):] +
		"/api/todos/suggest-simulated?clientID=" + clientID
	resp, err := postForm(ctx, createURL, url.Values{titleField: {buyMilk}})
	if err != nil {
		t.Fatalf("suggest-simulated: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("suggest-simulated status=%d", resp.StatusCode)
	}

	// Wait for the "suggestions" signal to arrive with 3 items. The
	// fake's 500→200 + delay means this lands after a retry + a slow
	// response, so we allow a generous timeout.
	full := pumpSSEUntil(t, stream, 14*time.Second, func(s string) bool {
		if !strings.Contains(s, "\""+signalSuggestions+"\"") {
			return false
		}
		var patch map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &patch); err != nil {
			return false
		}
		raw, ok := patch[signalSuggestions]
		if !ok {
			return false
		}
		var sugg []string
		if err := json.Unmarshal(raw, &sugg); err != nil {
			return false
		}
		return len(sugg) == 3
	})
	if !strings.Contains(full, "\""+signalSuggestions+"\"") {
		t.Fatalf("suggest-simulated: suggestions never arrived: %s", tailString(full, 600))
	}
	if !strings.Contains(full, "Got 3 suggestions") {
		t.Fatalf("suggest-simulated: success toast missing: %s", tailString(full, 600))
	}
	// Regression guard for the Techstack diagnostic panel: the goqite +
	// retry-go + fake-LLM run must flip the UI step signal to
	// "retry-demo" (the single Queue + retry affordance) and mark it done
	// once the result lands. If the streamSuggestResult merge stops
	// emitting techStep, the browser stepper stays frozen on the wrong
	// node even though the pipeline succeeded.
	if !strings.Contains(full, "\"techStep\":\"retry-demo\"") {
		t.Fatalf("suggest-simulated: techStep=retry-demo UI signal missing: %s", tailString(full, 600))
	}
	if !strings.Contains(full, "\"techDone\":true") {
		t.Fatalf("suggest-simulated: techDone not flipped true on success: %s", tailString(full, 600))
	}
}

// TestIntegration_SuggestSimulatedShowsRetryFeedback asserts the worker's
// retry layer streams per-attempt feedback as the fake LLM returns 500 on
// the first call. This is the narration the user sees in the UI toasts.
func TestIntegration_SuggestSimulatedShowsRetryFeedback(t *testing.T) {
	base, _, _, _, cleanup := testFixtureSimulated(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	clientID := "suggest-retry-client-" + time.Now().Format(clientIDSuffixFormat)
	stream := openSSEWithCtx(ctx, t, base, clientID)
	defer func() { _ = stream.Body.Close() }()

	time.Sleep(100 * time.Millisecond)

	createURL := "http://127.0.0.1" + base[len("http://127.0.0.1"):] +
		"/api/todos/suggest-simulated?clientID=" + clientID
	resp, err := postForm(ctx, createURL, url.Values{titleField: {"write tests"}})
	if err != nil {
		t.Fatalf("suggest-simulated: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The fake returns 500 on the first call, so we expect a "retry"
	// SSE event with attempt 1 before the eventual success.
	full := pumpSSEUntil(t, stream, 14*time.Second, func(s string) bool {
		return strings.Contains(s, "Got 3 suggestions")
	})
	if !strings.Contains(full, "suggest (simulated): attempt 1 failed") {
		t.Fatalf("suggest-simulated: retry feedback not seen: %s", tailString(full, 400))
	}
}
