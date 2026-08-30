// SCOPE:layer=feature,removal=plugin — the credits Service implements the
// llm.Biller seam so every Chat/ChatStream call (the internal/llm choke
// point) is metered. To remove: delete this file + features/credits/.
//
// Biller contract lives in internal/llm/goai.go. The billing subject (userID)
// comes from the request context (llm.WithUserID), set by the caller that
// knows the authenticated user. Missing user or unaffordable call → fail
// closed (error) once billing is enabled, never silently free.
package credits

import (
	"context"

	"github.com/zendev-sh/goai/provider"

	"github.com/calionauta/ai-credits/credits"
	"github.com/calionauta/gogogo-fullstack-template/internal/llm"
)

// Compile-time check that *Service satisfies the internal/llm metering seam.
var _ llm.Biller = (*Service)(nil)

// Authorize implements llm.Biller: reserves credits for the call before it
// runs (fail-closed) and returns a Settle that finalizes at the real usage.
// s nil (billing disabled) keeps everything free, as before.
func (s *Service) Authorize(ctx context.Context, model, prompt string) (llm.Settle, error) {
	if s == nil || s.Credits == nil {
		return nil, nil // billing disabled → free
	}
	uid := llm.UserIDFrom(ctx)
	if uid == "" {
		return nil, llm.ErrNoUserToBill
	}

	// Monthly grant must be applied before reserving so the user's credit
	// pool is topped up for this period. EnsureMonthlyGrant also honors the
	// optional subscription gate: if a `subscriptions` row exists for the
	// user and is not 'active', the grant is refused (fail-closed). The gate
	// only bites once something calls SetSubscription — none of the apps set
	// subscription rows today (prepaid/BYOK-first), so it stays inert; wire
	// SetSubscription when a paid-tier model lands.
	if _, err := s.Credits.EnsureMonthlyGrant(ctx, uid, ""); err != nil {
		return nil, err
	}

	requestID := credits.NewRequestID()
	in := credits.EstimateTokens(prompt)
	maxOut := 2048
	reserve, err := s.Credits.EstimateMax(ctx, model, in, maxOut)
	if err != nil {
		return nil, err
	}
	rsv, err := s.Credits.Reserve(ctx, uid, requestID, reserve)
	if err != nil {
		return nil, err
	}
	_ = s.Credits.EnqueueSettlement(ctx, requestID, uid, rsv.ID, "goai", model)

	// Closure captures the reservation + model; settle(ctx, realUsage)
	// finalizes it at the real cost for this model.
	return func(usage provider.Usage) {
		settleCall(ctx, s, rsv, model, usage)
	}, nil
}

func settleCall(ctx context.Context, s *Service, rsv *credits.Reservation, model string, u provider.Usage) {
	rec := fromGoAIUsage("", "", model, u) // model + tokens drive the cost
	if !hasTokens(u) {
		_ = s.Credits.Release(ctx, rsv) // error/abort → release, bill nothing
		return
	}
	cost, err := s.Credits.Credits(ctx, rec)
	if err != nil {
		_ = s.Credits.Release(ctx, rsv)
		return
	}
	if err := s.Credits.Settle(ctx, rsv, cost); err != nil {
		// A failed settle leaves the reservation open; Reconcile expires it
		// (no double charge). The caller surfaces non-zero usage otherwise.
		_ = err
	}
}

func hasTokens(u provider.Usage) bool {
	return u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens+u.ReasoningTokens > 0
}
