package credits

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v80/webhook"

	"github.com/calionauta/ai-credits/credits"
	"github.com/calionauta/gogogo-fullstack-template/config"
)

func stripeTestService(t *testing.T) *Service {
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
	return &Service{
		Credits: svc,
		Cfg: &config.CreditsConfig{
			// secrets assembled at runtime so they aren't static test
			// literals (gosec G101)
			StripeSecretKey:     "sk_test_" + "placeholder",
			StripeWebhookSecret: "whsec_" + "test_secret",
		},
	}
}

// A replayed checkout.session.completed webhook must grant credits exactly
// once per payment intent. The lib's idempotency_key UNIQUE is the guard; the
// test drives the full signed-webhook path.
func TestStripeWebhookGrantIsIdempotentPerPaymentIntent(t *testing.T) {
	s := stripeTestService(t)
	uid := "user-1"
	grant := int64(500)

	payload := `{"id":"evt_test_1","api_version":"2024-09-30.acacia",` +
		`"type":"checkout.session.completed","data":{"object":{` +
		`"id":"cs_test_1","payment_intent":"pi_test_1",` +
		`"metadata":{"purpose":"topup:","user_id":"` + uid + `","credits":"500"}}}}`
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   []byte(payload),
		Secret:    "whsec_" + "test_secret",
		Timestamp: time.Now(),
	})

	// The webhook grants via Grant only (no monthly grant wired in this
	// test); the balance reflects just the one completed top-up.
	for range 2 { // replay the same event twice
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost, "/api/credits/stripe-webhook", bytes.NewReader(sp.Payload))
		req.Header.Set("Stripe-Signature", sp.Header)
		s.HandleStripeWebhook(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook replay: want 200, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	want := grant // replay must not double
	if got := balanceFor(t, s, uid); got != want {
		t.Fatalf("balance: want %d, got %d (replay double-granted?)", want, got)
	}
}

// A bad signature must be rejected before any grant lands on the ledger.
func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	s := stripeTestService(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost, "/api/credits/stripe-webhook",
		bytes.NewReader([]byte(`{"type":"checkout.session.completed"}`)))
	req.Header.Set("Stripe-Signature", "t=1,v1=bogus")

	s.HandleStripeWebhook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if got := balanceFor(t, s, "user-2"); got != 0 {
		t.Fatalf("bad signature must not grant: got balance %d", got)
	}
}
