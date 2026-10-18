// Package telemetry is the server's side of what an agent is allowed to send out: the telemetry intents (which signals, from which
// namespaces, to which destination, and the commands that follow), and the quick-start backends an intent can point at.
//
// An intent's destination may be a regional operator, so this package stands on operators, and operators know nothing of it.
package telemetry

import (
	"continuum/internal/kernel"
	"continuum/internal/operators"
)

// Service is the telemetry intents of one organisation.
type Service struct {
	*kernel.Core
	Operators *operators.Service
}

// NewService returns the telemetry intents of the organisation k is scoped to, over the operators of the same organisation.
func NewService(k *kernel.Core, ops *operators.Service) *Service {
	return &Service{Core: k, Operators: ops}
}
