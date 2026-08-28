package credits

import (
	"context"
	"errors"
	"testing"

	"github.com/zendev-sh/goai/provider"

	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
)

func TestBillerNoUserFailsClosed(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.Authorize(context.Background(), "fake-1", "hello"); !errors.Is(err, llm.ErrNoUserToBill) {
		t.Fatalf("Authorize no-user err = %v, want llm.ErrNoUserToBill", err)
	}
	// Billing disabled (nil Service / nil Credits) stays free.
	var nilSvc *Service
	if s, err := nilSvc.Authorize(context.Background(), "fake-1", "hi"); err != nil || s != nil {
		t.Fatalf("nil svc Authorize = (%v,%v), want (nil,nil) free", s, err)
	}
}

func TestBillerSettleAtRealUsage(t *testing.T) {
	svc := newTestService(t)
	ctx := llm.WithUserID(context.Background(), "user-1")
	settle, err := svc.Authorize(ctx, "fake-1", "a long prompt that consumes tokens")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if settle == nil {
		t.Fatal("expected a settle fn")
	}
	// Simulate a real call consuming some tokens; settle at that usage.
	settle(provider.Usage{InputTokens: 100, OutputTokens: 50})
	bal, err := svc.Credits.Balance(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	// user-1 got 1000 (monthly) and now spent real credits on 150 tokens.
	if bal == 1000 {
		t.Fatalf("balance unchanged (%d): settle did not charge", bal)
	}
	if bal < 0 || bal >= 1000 {
		t.Fatalf("balance = %d out of range (want 0 < bal < 1000 after a real charge)", bal)
	}
}

func TestBillerZeroUsageReleases(t *testing.T) {
	svc := newTestService(t)
	ctx := llm.WithUserID(context.Background(), "user-1")
	settle, err := svc.Authorize(ctx, "fake-1", "prompt")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	settle(provider.Usage{}) // abort/error → release
	bal, _ := svc.Credits.Balance(context.Background(), "user-1")
	if bal != 1000 {
		t.Fatalf("balance = %d after zero-usage settle, want 1000 (fully released)", bal)
	}
}
