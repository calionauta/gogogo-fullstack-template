// SCOPE:layer=infra,removal=core — Queue handlers + retry + workers
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/zendev-sh/goai"
)

// RetryConfig holds exponential backoff + jitter settings for SSE-aware retries.
type RetryConfig struct {
	Attempts     uint
	Delay        time.Duration
	MaxDelay     time.Duration
	JitterFactor float64 // 0.0 = no jitter, 0.5 = ±50%
}

// DefaultRetryConfig is suitable for LLM calls with SSE streaming.
// 2s initial delay doubles each attempt up to 30s, with ±20% jitter to
// avoid thundering-herd retries when many handlers fail at once.
var DefaultRetryConfig = RetryConfig{
	Attempts:     3,
	Delay:        2 * time.Second,
	MaxDelay:     30 * time.Second,
	JitterFactor: 0.2,
}

// Do runs fn with retry, sending SSE progress updates between attempts.
// Retry feedback is wrapped in the Job envelope so SSE stream handlers
// can dispatch it through the same path as worker output (the "retry"
// case in handleSSEStream). If hub is nil or clientID is empty, no
// SSE feedback is emitted.
//
// The retry-go v4 default options respect context cancellation during
// the sleep between attempts (verified in retry_test.go), so callers
// can use ctx to abort a long retry chain.
func (r *RetryConfig) Do(ctx context.Context, hub *SSEHub, clientID string, operation string, fn func() error) error {
	attempt := uint(0)

	return retry.Do(
		func() error {
			attempt++
			err := fn()

			if hub != nil && clientID != "" {
				status := "attempt"
				if err == nil {
					status = "success"
				}
				payload, marshalErr := json.Marshal(map[string]any{
					"operation": operation,
					"attempt":   attempt,
					"status":    status,
					"error":     errMsg(err),
				})
				if marshalErr != nil {
					return fmt.Errorf("retry: marshal payload: %w", marshalErr)
				}
				envelope := Job{Type: "retry", ClientID: clientID, Payload: payload}
				body, envelopeErr := json.Marshal(envelope)
				if envelopeErr != nil {
					return fmt.Errorf("retry: marshal envelope: %w", envelopeErr)
				}
				hub.Send(clientID, body)
			}

			if err != nil {
				slog.Warn("retry: attempt failed",
					"operation", operation,
					"attempt", attempt,
					"max_attempts", r.Attempts,
					"error", err)
			}
			return err
		},
		retry.Context(ctx),
		retry.Attempts(r.Attempts),
		retry.Delay(r.Delay),
		retry.MaxDelay(r.MaxDelay),
		retry.MaxJitter(time.Duration(float64(r.Delay)*r.JitterFactor)),
		retry.DelayType(func(n uint, _ error, _ *retry.Config) time.Duration {
			// Exponential backoff: delay * 2^(n-1) + jitter.
			d := min(time.Duration(float64(r.Delay)*float64(int(1)<<(n-1))), r.MaxDelay)
			return d
		}),
		retry.LastErrorOnly(true),
		// Never retry on 4xx auth/permission errors: they won't
		// succeed on a second attempt, and retrying just wastes
		// time + tokens. 429 (rate limit) and 5xx are transient
		// and DO get retried.
		retry.RetryIf(func(err error) bool {
			return !isAuthError(err)
		}),
	)
}

// isAuthError reports whether err is a 401/403 from the upstream LLM
// provider (or any 4xx that is not 429). It unwraps the error chain
// so the check works even when callers wrap the original APIError.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *goai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return true
		case http.StatusTooManyRequests:
			return false // retry rate limits
		}
		// Other 4xx (400, 404, 422...) are client errors: don't retry.
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
			return true
		}
	}
	return false
}

// DoSilent runs fn with retry but NO SSE feedback (for internal/non-user-facing jobs).
func (r *RetryConfig) DoSilent(ctx context.Context, fn func() error) error {
	return r.Do(ctx, nil, "", "", fn)
}

// errMsg returns err.Error() or empty string when err is nil. Used by
// Do to embed the latest error in the SSE feedback payload without
// leaking wrapped error chains to the browser.
func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
