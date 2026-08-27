// SCOPE:layer=feature,removal=plugin — Stripe top-up for AI credits.
// To remove: delete features/credits/ + router/credits.go + config fields.
//
// Gated by STRIPE_SECRET_KEY/STRIPE_WEBHOOK_SECRET (config StripeSecretKey /
// StripeWebhookSecret): when either is empty, no Stripe routes are registered
// and the app runs billing-free. A checkout session is created for
// "topup:<userID>:<amount>" and, on checkout.session.completed, the webhook
// grants credits keyed by the payment intent id — so a replayed webhook can
// never double-credit (the lib's idempotency_key UNIQUE is the guard).
package credits

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	stripe "github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/checkout/session"
	"github.com/stripe/stripe-go/v80/webhook"

	"github.com/calionauta/ai-credits/credits"
)

// stripeTopupPrefix is the metadata purpose used to identify AI-credits
// top-ups inside Stripe session/event metadata.
const stripeTopupPrefix = "topup:"

// stripeCreditsPerCent is how many credits one cent buys.
// ponytail: hard-coded; make configurable if rates vary per plan.
const stripeCreditsPerCent = 10

// enabled reports whether the Stripe integration is live.
func (s *Service) stripeEnabled() bool {
	return s.Cfg.StripeSecretKey != "" && s.Cfg.StripeWebhookSecret != ""
}

// CheckoutRequest is the body of POST /api/credits/checkout.
type CheckoutRequest struct {
	AmountCents int64 `json:"amountCents"`
}

// HandleCheckout creates a Stripe Checkout Session for a credit top-up and
// returns its hosted payment URL. Called from the authed route handler.
func (s *Service) HandleCheckout(userID string, req CheckoutRequest) (string, error) {
	if !s.stripeEnabled() {
		return "", errors.New("credits: stripe not enabled")
	}
	if req.AmountCents <= 0 {
		return "", errors.New("credits: amount must be positive")
	}
	stripe.Key = s.Cfg.StripeSecretKey

	grant := req.AmountCents * stripeCreditsPerCent
	amount := req.AmountCents
	mode := string(stripe.CheckoutSessionModePayment)
	success := "http://localhost:3000/credits?status=success"
	cancel := "http://localhost:3000/credits?status=cancelled"
	topupName := "AI Credits top-up"
	usd := "usd"

	params := &stripe.CheckoutSessionParams{
		Mode:              &mode,
		ClientReferenceID: &userID,
		SuccessURL:        &success,
		CancelURL:         &cancel,
		LineItems: []*stripe.CheckoutSessionLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   &usd,
				UnitAmount: &amount,
				ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: &topupName,
				},
			},
		}},
		Metadata: map[string]string{
			"purpose": stripeTopupPrefix,
			"user_id": userID,
			"credits": strconv.FormatInt(grant, 10),
		},
	}

	sess, err := session.New(params)
	if err != nil {
		return "", err
	}
	return sess.URL, nil
}

// HandleStripeWebhook validates the signature and, on
// checkout.session.completed, grants the purchased credits to the user,
// keyed by payment intent id (idempotent).
func (s *Service) HandleStripeWebhook(w http.ResponseWriter, req *http.Request) {
	if !s.stripeEnabled() {
		http.Error(w, "stripe not enabled", http.StatusServiceUnavailable)
		return
	}
	const maxPayload = 1 << 20 // 1 MiB
	payload, err := io.ReadAll(io.LimitReader(req.Body, maxPayload))
	if err != nil {
		http.Error(w, "read payload", http.StatusBadRequest)
		return
	}

	evt, err := webhook.ConstructEvent(payload, req.Header.Get("Stripe-Signature"), s.Cfg.StripeWebhookSecret)
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}

	if evt.Type != "checkout.session.completed" {
		// Unhandled events are acknowledged (not an error for Stripe).
		http.Error(w, "unhandled event", http.StatusBadRequest)
		return
	}

	if err := s.applyCheckoutCompletion(req, evt.Data.Raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// applyCheckoutCompletion grants the purchased credits for a completed
// checkout session, keyed by payment intent id. Idempotent: a replayed
// webhook returns ErrDuplicateGrant from the lib, which we treat as success.
func (s *Service) applyCheckoutCompletion(req *http.Request, raw json.RawMessage) error {
	var cs stripe.CheckoutSession
	if err := json.Unmarshal(raw, &cs); err != nil {
		return errors.New("bad session object")
	}
	if cs.Metadata["purpose"] != stripeTopupPrefix {
		return errors.New("unexpected payment purpose")
	}
	userID := cs.Metadata["user_id"]
	if userID == "" {
		return errors.New("missing user_id")
	}
	if cs.PaymentIntent == nil || cs.PaymentIntent.ID == "" {
		return errors.New("missing payment intent")
	}
	grant, err := strconv.ParseInt(cs.Metadata["credits"], 10, 64)
	if err != nil || grant <= 0 {
		return errors.New("missing credits")
	}
	// Idempotency key: grant exactly once per completed payment intent.
	// A replayed webhook hits ErrDuplicateGrant, which is a success for
	// Stripe (already credited) — ack 200, never re-grant.
	if ierr := s.Credits.Grant(req.Context(), userID, grant,
		"stripe", "topup", "stripe:topup:"+cs.PaymentIntent.ID); ierr != nil {
		if errors.Is(ierr, credits.ErrDuplicateGrant) {
			return nil
		}
		return errors.New("grant failed")
	}
	return nil
}
