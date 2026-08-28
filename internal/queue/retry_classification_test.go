package queue

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
)

// isAuthError delegates to goai's classification: 4xx non-retryable → true
// (don't retry), 429/5xx/network → false (retry). These pin the contract the
// worker RetryConfig.RetryIf relies on.
func TestIsAuthErrorClassification(t *testing.T) {
	apiErr := func(status int) error {
		// Use a realistic JSON body so 4xx parses as a normal APIError
		// (not the ContextOverflowError special-case that an empty/nil
		// body can trigger for 400).
		body := []byte(`{"error":{"message":"provider rejected request"}}`)
		return goai.ParseHTTPErrorWithHeaders("", status, body, nil)
	}

	cases := []struct {
		name string
		err  error
		want bool // true = auth/non-retryable 4xx
	}{
		{"nil", nil, false},
		{"network error (not APIError) → retry", errors.New("boom"), false},
		{"401 unauthorized → no retry", apiErr(http.StatusUnauthorized), true},
		{"403 forbidden → no retry", apiErr(http.StatusForbidden), true},
		{"400 bad request → no retry", apiErr(http.StatusBadRequest), true},
		{"404 → no retry", apiErr(http.StatusNotFound), true},
		{"429 rate limit → retry", apiErr(http.StatusTooManyRequests), false},
		{"503 → retry", apiErr(http.StatusServiceUnavailable), false},
	}
	for _, c := range cases {
		if got := isAuthError(c.err); got != c.want {
			t.Errorf("%s: isAuthError() = %v, want %v", c.name, got, c.want)
		}
	}
}

// RetryIf(429): a rate-limited call must actually be retried by Do, proving
// the worker doesn't give up on 429.
func TestRetryDoRetriesOn429(t *testing.T) {
	cfg := RetryConfig{Attempts: 3, Delay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	calls := 0
	err := cfg.DoSilent(context.Background(), func() error {
		calls++
		return goai.ParseHTTPErrorWithHeaders("", http.StatusTooManyRequests, nil, nil)
	})
	if err == nil {
		t.Fatal("expected final error")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (429 should be retried)", calls)
	}
}
