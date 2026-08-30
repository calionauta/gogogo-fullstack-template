// SCOPE:layer=feature,removal=plugin — Stripe top-up wiring.
package credits

import (
	"context"
	"errors"
	"net/http"
)

type CheckoutRequest struct {
	SKU string `json:"sku"`
}

func (s *Service) HandleCheckout(ctx context.Context, userID string, req CheckoutRequest) (string, error) {
	if s.Stripe == nil {
		return "", errors.New("credits: stripe not enabled")
	}
	checkout, err := s.Stripe.CreateCheckout(ctx, userID, req.SKU)
	if err != nil {
		return "", err
	}
	return checkout.URL, nil
}

func (s *Service) HandleStripeWebhook(w http.ResponseWriter, req *http.Request) {
	if s.Stripe == nil {
		http.Error(w, "stripe not enabled", http.StatusServiceUnavailable)
		return
	}
	s.Stripe.HandleWebhook(w, req)
}
