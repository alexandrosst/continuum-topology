// Package estate is what the server knows about the world: the hub that receives the agents' streams and keeps what each one reports,
// the per-agent views, the observed flows, the twin built from them, the history recorded as it changes, and the saved workspace the twin
// is read against.
//
// It is the top of the hub cluster. It stands on observed (the flows table, pure), on the agents (consent, tier, conditions, the stream),
// and on connectivity (the paths, links and addresses, and the config an agent is told), and none of them knows it. The view is theirs
// three ways: EstView here, agents.View and connectivity.NetView. The HTTP handlers that read the hub are the shell's, and the
// shell asks the hub through its exported methods.
package estate

import (
	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/decisionloop"
	"continuum/internal/kernel"
)

// Service is what the estate does with the Core of one organisation: it stands on the agents, on connectivity, and on the decision loop
// whose ports (what an agent is allowed, what it carries) the hub supplies.
type Service struct {
	*kernel.Core
	Agents       *agents.Service
	Connectivity *connectivity.Service
	Loop         *decisionloop.Service
}

// NewService returns the estate's view of the organisation k is scoped to, standing on the modules it is given.
func NewService(k *kernel.Core, ag *agents.Service, conn *connectivity.Service, loop *decisionloop.Service) *Service {
	return &Service{Core: k, Agents: ag, Connectivity: conn, Loop: loop}
}
