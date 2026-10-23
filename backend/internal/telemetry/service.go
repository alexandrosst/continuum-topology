// Package telemetry is the server's side of what an agent is allowed to send out: the telemetry intents (which signals, from which
// namespaces, to which destination, and the commands that follow), and the quick-start backends an intent can point at.
//
// An intent's destination may be a regional operator, so this package asks the operators two things (Operators) and knows nothing else
// of them; operators know nothing of it.
package telemetry

import (
	"context"

	"ikhnos/internal/kernel"
	"ikhnos/internal/store"
)

// Operators is what the intents ask of the regional operators of the organisation: the operator a destination names, and whether a
// destination is acceptable, with the address it carries written the way a recorded one is.
type Operators interface {
	OperatorInOrg(ctx context.Context, id string) (store.Operator, error)
	CheckDestination(ctx context.Context, dest store.Destination) (store.Destination, error)
}

// Service is the telemetry intents of one organisation.
type Service struct {
	*kernel.Core
	Operators Operators
}

// NewService returns the telemetry intents of the organisation k is scoped to, over the operators of the same organisation.
func NewService(k *kernel.Core, ops Operators) *Service {
	return &Service{Core: k, Operators: ops}
}
