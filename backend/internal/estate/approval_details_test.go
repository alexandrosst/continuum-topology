package estate

import (
	"testing"

	"continuum/internal/testkit"
)

func TestStateShowsApprovalDetailsForPendingAgents(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, testkit.FP)
	hub := NewHub(e.core)
	_ = e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, "ZZZZ-ZZZZ", 2)
	d, _ := hub.State(e.ctx)
	a := d.Agents[0]
	if a.ApprovalAttemptsLeft == nil || *a.ApprovalAttemptsLeft != 4 || a.PendingExpiresAt == "" {
		t.Fatalf("%+v", a)
	}
}
