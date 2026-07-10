package todo_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

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

// TestIntegration_CreateEmitsToast verifies the asynchronous toast
// emitted by the worker after handleCreate enqueues a "todo_created"
// job. The HTTP response itself only patches the todo list; the toast
// arrives via the SSE stream once the worker picks up the job. This
// exercises the full HTTP → queue → worker → SSE pipeline that the
// SSE-aware retry path is designed for.

// TestIntegration_CreateRendersInList verifies the create path is
// synchronous: the HTTP response patches the todo list with the new
// todo immediately (no queue round-trip). Realtime fan-out to other
// clients is handled by the broadcaster separately.
func TestIntegration_CreateRendersInList(t *testing.T) {
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()
	ctx := newTestCtx(t)

	resp, err := postForm(ctx, base+"/api/todos", url.Values{titleField: {buyMilk}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, buyMilk) {
		t.Fatalf("created todo not present in list patch: %s", tailString(body, 400))
	}
}

// parseSSEData extracts the `data:` payloads from a raw SSE transcript.
// Datastar emits `event: datastar\ndata: <payload>\n\n` blocks; we return
// the payload strings so callers can scan them for the lastRetry signal.
func parseSSEData(transcript string) []string {
	var out []string
	for _, block := range strings.Split(transcript, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	}
	return out
}

// extractJSONString reads a JSON string literal starting at s. The
// caller passes s positioned just BEFORE the opening quote of the value
// (i.e. s begins with `":"<json>"`). It returns the unescaped
// contents and whether parsing succeeded. Handles escaped quotes so the
// embedded retry JSON (`"{\"attempt\":1}"`) is decoded correctly.
func extractJSONString(s string) (string, bool) {
	i := strings.Index(s, "\"")
	if i < 0 {
		return "", false
	}
	s = s[i+1:]
	var sb strings.Builder
	for j := 0; j < len(s); j++ {
		c := s[j]
		if c == '\\' && j+1 < len(s) {
			sb.WriteByte(s[j+1])
			j++
			continue
		}
		if c == '"' {
			return sb.String(), true
		}
		sb.WriteByte(c)
	}
	return "", false
}

// openSSE opens the SSE stream with a fresh context derived from the
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
