// SCOPE:layer=feature,removal=plugin — Managed LLM gateway with per-call
// credits (lib ai-credits). To remove: delete features/credits/ + router.
//
// RunManaged wraps a managed LLM call: ensure monthly grant, predict max cost,
// reserve, call the provider via goai (same lib the internal/llm client wraps,
// but here we capture the full TotalUsage so we can price the actual call),
// then settle at the real cost and record usage. On error the reservation is
// released, not settled — an aborted call bills nothing.
package credits

import (
	"context"
	"errors"
	"fmt"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/compat"

	"github.com/calionauta/ai-credits/credits"
)

// ManagedRequest is the body of POST /api/ai/request with billing_mode=managed.
type ManagedRequest struct {
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
}

// ManagedResult carries the generated text plus what was charged, for the
// response and for tests.
type ManagedResult struct {
	Text           string `json:"text"`
	CreditsCharged int64  `json:"creditsCharged"`
	RequestID      string `json:"requestId"`
}

// RunManaged runs a managed (app-keyed) LLM call against the real provider
// using the app's goai client, priced and billed through the credits ledger.
// baseURL is the provider base (from the app's GOAI_BASE_URL); apiKey is the
// app-level key (NOT the user's BYOK key). userID is the billing subject.
func (s *Service) RunManaged(
	ctx context.Context,
	userID string,
	apiKey string,
	baseURL string,
	req ManagedRequest,
) (*ManagedResult, error) {
	if s.Credits == nil {
		return nil, errors.New("credits: plugin disabled")
	}
	if apiKey == "" {
		return nil, errors.New("credits: no GOAI_API_KEY; managed mode unavailable")
	}
	model := req.Model
	if model == "" {
		model = s.Model
	}

	// Grant monthly credits lazily (idempotent per period).
	if _, err := s.Credits.EnsureMonthlyGrant(ctx, userID, ""); err != nil {
		return nil, fmt.Errorf("credits: monthly grant: %w", err)
	}

	// Predict a conservative max for an unknown-output call.
	max, err := s.Credits.EstimateMax(ctx, model, tokenEstimate(req.Prompt), req.MaxOutputTokens)
	if err != nil {
		return nil, fmt.Errorf("credits: estimate: %w", err)
	}
	requestID := newRequestID()
	rsv, err := s.Credits.Reserve(ctx, userID, requestID, max)
	if err != nil {
		return nil, fmt.Errorf("credits: reserve: %w", err)
	}

	// Call the provider directly (not internal/llm.Client.Chat, which discards
	// usage); capture TotalUsage for the final price.
	m := compat.Chat(model,
		compat.WithBaseURL(baseURL),
		compat.WithAPIKey(apiKey),
	)
	res, genErr := goai.GenerateText(ctx, m,
		goai.WithPrompt(req.Prompt),
		goai.WithMaxOutputTokens(req.MaxOutputTokens),
		goai.WithMaxRetries(0),
	)
	if genErr != nil {
		_ = s.Credits.Release(ctx, rsv)
		return nil, fmt.Errorf("credits: generate: %w", genErr)
	}

	u := fromGoAIUsage(requestID, userID, s.Model, res.TotalUsage)
	creditsCharged, err := s.Credits.Credits(ctx, u) // micro-units → whole credits
	if err != nil {
		_ = s.Credits.Release(ctx, rsv)
		return nil, fmt.Errorf("credits: cost: %w", err)
	}
	if err := s.Credits.Settle(ctx, rsv, creditsCharged); err != nil {
		return nil, fmt.Errorf("credits: settle: %w", err)
	}
	mu, _ := s.Credits.Cost(ctx, u)
	u.CostMicrounits = mu
	u.CreditsCharged = creditsCharged
	if err := s.Credits.RecordUsage(ctx, u); err != nil {
		return nil, fmt.Errorf("credits: record: %w", err)
	}

	return &ManagedResult{Text: res.Text, CreditsCharged: creditsCharged, RequestID: requestID}, nil
}

// fromGoAIUsage maps a goai provider.Usage into the lib's credits.Usage,
// filling the shared fields and leaving CostMicrounits/CreditsCharged to the
// caller (they depend on outcomes the mapper doesn't know).
func fromGoAIUsage(requestID, userID, model string, u provider.Usage) credits.Usage {
	return credits.Usage{
		RequestID:       requestID,
		UserID:          userID,
		Provider:        "goai",
		Model:           model,
		BillingMode:     "managed",
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CachedTokens:    u.CacheReadTokens,
		ReasoningTokens: u.ReasoningTokens,
	}
}

// tokenEstimate is a coarse input-token estimate (4 chars/token) used only to
// size the reserve; the final price uses real usage.
// ponytail: the exact constant is immaterial — it only inflates the reserve
// ceiling, which the real usage corrects on settle.
func tokenEstimate(prompt string) int {
	const charsPerToken = 4
	return len(prompt) / charsPerToken
}
