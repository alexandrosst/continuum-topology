package estate

import (
	"context"

	"ikhnos/internal/agents"
	"ikhnos/internal/decisionloop"
	"ikhnos/internal/store"
)

// hubAccess is the agents' answer to the decision loop (decisionloop.Access).
type hubAccess struct{ h *Hub }

// PlanChange checks ch against a as it is now. It is used when a change is proposed and again when it is accepted.
func (p hubAccess) PlanChange(a store.Agent, ch decisionloop.AccessChange) (decisionloop.AccessPlan, error) {
	h := p.h
	tier, next, widens, err := agents.ResolveChange(a, h.ConsentOf(a.ID), agentsChange(ch))
	if err != nil {
		return decisionloop.AccessPlan{}, err
	}
	return decisionloop.AccessPlan{Widens: widens, Apply: func(ctx context.Context, actor string) error {
		if ch.Tier != nil {
			_, err := h.SetAgentTier(ctx, actor, a.ID, tier, nil)
			return err
		}
		_, err := h.SetConsent(ctx, actor, a.ID, next)
		return err
	}}, nil
}

// agentsChange is the loop's record of a change as the agents judge it. The agent it is for travels beside it, as the row.
func agentsChange(ch decisionloop.AccessChange) agents.AccessChange {
	return agents.AccessChange{Tier: ch.Tier, Pause: ch.Pause, Resume: ch.Resume, Exclude: ch.Exclude, Include: ch.Include}
}
