package credits

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stripe/stripe-go/v80/webhook"

	corecredits "github.com/calionauta/ai-credits/credits"
	paymentcore "github.com/calionauta/ai-credits/payments"
	stripecredits "github.com/calionauta/ai-credits/stripe"
	"github.com/calionauta/gogogo-fullstack-template/config"
)

func stripeTestService(t *testing.T) (*Service, *paymentcore.Purchase) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ledger, err := corecredits.New(db, corecredits.Config{})
	if err != nil {
		t.Fatal(err)
	}
	payments, err := paymentcore.New(db, ledger, map[string]paymentcore.CatalogItem{
		"topup-small": {Credits: 500, Currency: "usd", AmountMinor: 500},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := stripecredits.New(payments, stripecredits.Config{WebhookSecret: "whsec_test"})
	if err != nil {
		t.Fatal(err)
	}
	purchase, err := payments.CreatePurchase(context.Background(), "stripe", "user-1", "topup-small")
	if err != nil {
		t.Fatal(err)
	}
	return &Service{Credits: ledger, Payments: payments, Stripe: adapter, Cfg: &config.CreditsConfig{}}, purchase
}

func TestStripeWebhookGrantIsIdempotentPerPaymentIntent(t *testing.T) {
	s, p := stripeTestService(t)
	payload := []byte(`{"id":"evt_test_1","api_version":"2024-09-30.acacia",` +
		`"type":"checkout.session.completed","data":{"object":{` +
		`"id":"cs_test_1","payment_intent":"pi_test_1","payment_status":"paid",` +
		`"metadata":{"purchase_id":"` + p.ID + `"}}}}`)
	signed := webhook.GenerateTestSignedPayload(
		&webhook.UnsignedPayload{Payload: payload, Secret: "whsec_test", Timestamp: time.Now()})
	for range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", bytes.NewReader(signed.Payload))
		req.Header.Set("Stripe-Signature", signed.Header)
		s.HandleStripeWebhook(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
		}
	}
	balance, err := s.Credits.Balance(context.Background(), "user-1")
	if err != nil || balance != 500 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
}

func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	s, _ := stripeTestService(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Stripe-Signature", "bad")
	s.HandleStripeWebhook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}
