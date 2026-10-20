// Package agents is an enrolled agent as the server knows it: the tokens and enrolment that make one, the stream it speaks over, the
// consent and access tier that bound what it may share, the conditions that say how it is doing, its diagnostics and self-telemetry,
// and the commands that take it away again.
//
// The live picture of all the agents together (the hub, its sessions and the views it keeps) is the estate's and stands on this
// package: the part of the view that is about the agent itself is View here, and the hub embeds it. This package knows no hub and no
// other agent. Its HTTP handlers are the shell's.
package agents

import (
	"context"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/kernel"
	"continuum/internal/telemetry"
)

// TelemetryCerts renews the certificate of an agent's telemetry on the agent's stream. The operators do it; this package asks and knows
// nothing else of them.
type TelemetryCerts interface {
	RenewTelemetryCert(ctx context.Context, ip string, req *continuumv1.RenewTelemetryCertRequest) (*continuumv1.RenewTelemetryCertResponse, error)
}

// Service is what the agents do with the Core of one organisation. Telemetry is told when an agent is revoked, so the intents that were
// its own end with it; Certs renews the certificate of an agent's telemetry on its stream.
type Service struct {
	*kernel.Core
	Certs     TelemetryCerts
	Telemetry *telemetry.Service
}

// NewService returns the agents' view of the organisation k is scoped to.
func NewService(k *kernel.Core, certs TelemetryCerts, tel *telemetry.Service) *Service {
	return &Service{Core: k, Certs: certs, Telemetry: tel}
}
