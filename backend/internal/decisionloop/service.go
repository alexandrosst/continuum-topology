// Package decisionloop is the server's side of the decision loop: what a decision-maker is asked, what it answered, what it proposed,
// what happened once a proposal was accepted, and how well each advisor has done.
//
// It owns the loop and nothing the loop acts on. It proposes a change to an agent's access and asks the agents' side whether the
// change is well formed and whether it widens what the agent shares (Access); it reads an agent's own load and which Config the agent
// has confirmed from the estate's side (Load). It knows neither what a tier is nor what a view is.
package decisionloop

import (
	"context"
	"time"

	"continuum/internal/kernel"
	"continuum/internal/store"
)

// Service is the decision loop of one organisation.
type Service struct {
	*kernel.Core

	// Access and Load are what the loop asks of the rest of the server. The server sets them when it builds its Hub.
	Access Access
	Load   Load

	// OutcomeWindow and OutcomeGiveUp, when set, replace the defaults of the Outcome recorder (tests use seconds).
	OutcomeWindow, OutcomeGiveUp time.Duration
}

// NewService returns the decision loop of the organisation k is scoped to. Its ports are set by whoever can answer them.
func NewService(k *kernel.Core) *Service { return &Service{Core: k} }

// Access is what a change to an agent's access does to the agent, as the agents' side says it.
type Access interface {
	// PlanChange checks ch against a as it is now. It is used when a change is proposed and again when it is accepted.
	PlanChange(a store.Agent, ch AccessChange) (AccessPlan, error)
}

// AccessPlan is what an access change would do to an agent as it is now.
type AccessPlan struct {
	// Widens is whether the change leaves the agent sharing more than it does: a higher tier, or a pause or exclusion lifted.
	Widens bool
	// Apply makes the change the way a person making it by hand would: validated, delivered to the agent and audited.
	Apply func(ctx context.Context, actor string) error
}

// Load is what the loop reads of an agent.
type Load interface {
	AgentLoad(agentID string) AgentLoad
}

// AgentLoad is the server's record of one agent's own load and of the Config it was told.
type AgentLoad struct {
	// Known is whether the server holds anything of the agent (a restart empties the in-memory views).
	Known bool
	// Samples are the agent's own self-telemetry, oldest first.
	Samples []LoadSample
	// ConfigHash is the Config the server last told the agent to run, ConfigApplied the one the agent last said it runs, and
	// InForceAt when it first said so.
	ConfigHash, ConfigApplied string
	InForceAt                 time.Time
}
