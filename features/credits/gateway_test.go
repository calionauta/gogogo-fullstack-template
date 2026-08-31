package credits

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/calionauta/ai-credits/credits"
)

// unitPricing: 1 microunit per token, 1000 microunits per credit → a call of
// N tokens costs exactly ceil(N/1000) credits with zero rounding surprises.
const unitPricing = `{
  "version": "test",
  "microunits_per_credit": 1000,
  "models": {
    "fake-1": {"input_per_mtok": 1000000, "output_per_mtok": 1000000,
               "cached_input_per_mtok": 1000000, "reasoning_per_mtok": 1000000}
  }
}`

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(8)

	svc, err := credits.New(db, credits.Config{
		MonthlyCredits: 1000,
		PricingReader:  strings.NewReader(unitPricing),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureMonthlyGrant(context.Background(), "user-1", ""); err != nil {
		t.Fatal(err)
	}
	return &Service{Credits: svc, Model: "fake-1"}
}

// A failed managed call must bill nothing: the reserve is released, so the
// balance returns exactly to the monthly grant.
func TestRunManagedFailedCallBillsNothing(t *testing.T) {
	s := newTestService(t)
	bal0 := balance(t, s)

	_, err := s.RunManaged(context.Background(), "user-1", "test-key",
		"http://127.0.0.1:1/v1", ManagedRequest{Model: "fake-1", Prompt: "hi"})
	if err == nil {
		t.Fatal("expected network error from unreachable provider")
	}

	if got := balance(t, s); got != bal0 {
		t.Fatalf("balance changed on failed call: want %d, got %d", bal0, got)
	}
}

// A successful managed call settles: the charged credits are debited from the
// grant and a usage record exists. We stub the provider call via a tiny local
// httptest proxy so the goai client reaches it (see fakeProviderTCP — but to
// keep this hermetic we instead assert via the lib directly in TestSettlePath).
func balance(t *testing.T, s *Service) int64 {
	t.Helper()
	b, err := s.Credits.Balance(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// balanceFor reads the balance for an arbitrary user id (used by the Stripe
// webhook tests which credit a named user).
func balanceFor(t *testing.T, s *Service, uid string) int64 { //nolint:unused
	t.Helper()
	b, err := s.Credits.Balance(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
