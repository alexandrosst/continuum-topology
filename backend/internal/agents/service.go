// Package agents is an enrolled agent as the server knows it: the tokens and enrolment that make one, the stream it speaks over, the
// consent and access tier that bound what it may share, the conditions that say how it is doing, its diagnostics and self-telemetry,
// and the commands that take it away again.
//
// The live picture of all the agents together (the hub, its sessions and the views it keeps) is the estate's and stands on this
// package: the part of the view that is about the agent itself is View here, and the hub embeds it. This package knows no hub and no
// other agent. Its HTTP handlers are the shell's.
package agents

import (
	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/telemetry"
)

// Service is what the agents do with the Core of one organisation. Telemetry is told when an agent is revoked, so the intents that were
// its own end with it; Operators renews the certificate of an agent's telemetry on its stream.
type Service struct {
	*kernel.Core
	Operators *operators.Service
	Telemetry *telemetry.Service
}

// NewService returns the agents' view of the organisation k is scoped to.
func NewService(k *kernel.Core, ops *operators.Service, tel *telemetry.Service) *Service {
	return &Service{Core: k, Operators: ops, Telemetry: tel}
}
