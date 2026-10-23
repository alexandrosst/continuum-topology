// Package connectivity is how the clusters reach one another and the world: the paths an agent measures, the links between two
// clusters and the throughput logins they use, the tunnels found between nodes, the addresses a cluster dials and whether they
// answer, and the config an agent is told (its compile and its digest).
//
// It stands on the agents (the consent that limits what is measured) and on what the flows say. The hub, which keeps the live picture
// and decides when to push, is the estate's and stands on this package: the network part of the view is NetView here.
package connectivity

import "ikhnos/internal/kernel"

// Service is what connectivity does with the Core of one organisation: the sealed credentials of the links.
type Service struct {
	*kernel.Core
}

// NewService returns connectivity's view of the organisation k is scoped to.
func NewService(k *kernel.Core) *Service { return &Service{Core: k} }
