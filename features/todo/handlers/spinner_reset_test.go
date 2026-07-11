package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/starfederation/datastar-go/datastar"
)

// mustCtxReq builds an httptest request with a background context (noctx
// forbids httptest.NewRequest).
func mustCtxReq(method, path string) *http.Request {
	req, err := http.NewRequestWithContext(context.Background(), method, path, nil)
	if err != nil {
		panic(err)
	}
	return req
}

// TestApplyTechStep_ResetsSpinnerOnDone is the regression guard for the
// "Running demo button spins forever" bug.
//
// Root cause: the queue+retry demo (and AI suggest) set $suggestPending =
// true on click to show a spinner, but the success path relied on a
// data-init delay to reset $techStep — it never reset $suggestPending.
// So after the action completed, the button stayed in its loading
// state indefinitely, and (because the spinner signal is shared) every
// tab's action button looked stuck.
//
// The fix centralises the terminal state in applyTechStep: when done is
// true it ALSO merges suggestPending=false. This test proves that
// contract directly.
func TestApplyTechStep_ResetsSpinnerOnDone(t *testing.T) {
	h := &TodoHandler{}
	rec := httptest.NewRecorder()
	sse := sdk.NewSSE(rec, mustCtxReq(http.MethodPost, "/api/todos/retry-demo"))

	if err := h.applyTechStep(sse, "retry-demo", true, ""); err != nil {
		t.Fatalf("applyTechStep: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"suggestPending":false`) {
		t.Fatalf("done step must reset suggestPending=false; got body:\n%s", body)
	}
	if !strings.Contains(body, `"techDone":true`) {
		t.Fatalf("done step must set techDone=true; got body:\n%s", body)
	}
}

// TestApplyTechStep_KeepsSpinnerWhileRunning proves the inverse: a
// non-terminal step (done=false) must NOT release the spinner, so the
// button keeps spinning until the action truly finishes.
func TestApplyTechStep_KeepsSpinnerWhileRunning(t *testing.T) {
	h := &TodoHandler{}
	rec := httptest.NewRecorder()
	sse := sdk.NewSSE(rec, mustCtxReq(http.MethodPost, "/api/todos/retry-demo"))

	if err := h.applyTechStep(sse, "retry-demo", false, ""); err != nil {
		t.Fatalf("applyTechStep: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, `"suggestPending":false`) {
		t.Fatalf("running step must NOT reset suggestPending; got body:\n%s", body)
	}
}
