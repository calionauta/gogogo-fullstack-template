package todo_test

import (
	"context"
	"net/url"
	"testing"
	"time"
)

// TestIntegration_Suggest_NotConfiguredReturns404 verifies the route
// is NOT registered when the LLM client has no API key, so the
// request 404s (vs returning 503 with a friendly error). This
// confirms the feature is genuinely off in dev with no key set.
func TestIntegration_Suggest_NotConfiguredReturns404(t *testing.T) {
	base, _, _, cleanup := testFixture(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := postForm(ctx, base+"/api/todos/suggest", url.Values{"partial": {"write"}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404 (route not registered), got %d", resp.StatusCode)
	}
}

// TestIntegration_AdminUnlock_NotConfiguredReturns404 mirrors the
// same defensive design: the admin route is only registered when
// AdminToken is set, so 404 (not 403) when the secret file is
// missing.
func TestIntegration_AdminUnlock_NotConfiguredReturns404(t *testing.T) {
	base, _, _, cleanup := testFixture(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := postForm(ctx, base+"/api/admin/unlock", url.Values{"token": {"anything"}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404 (admin route not registered), got %d", resp.StatusCode)
	}
}
