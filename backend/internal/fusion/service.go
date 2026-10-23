// Package fusion is the server's control of the FUSION stack: the switch that turns it on and off, its status, the retention of its
// stores, the certificates and public address of the gateway, and the central operator the gateway is.
//
// The central operator is an operator that FUSION manages: this package calls operators, and operators know nothing of FUSION (they are
// handed a ManagedStatus). The routes of the switch (its state, on and off, retention) are API; the data API, its access tokens and FUSION's own pages are the shell's, and call through the FusionControl.
package fusion

import (
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
)

// Service is the FUSION control's view of one organisation: its Core and the operators the central operator is one of.
type Service struct {
	*kernel.Core
	Operators *operators.Service
}

// NewService returns the FUSION view of the organisation k is scoped to, over the operators of the same organisation.
func NewService(k *kernel.Core, ops *operators.Service) *Service {
	return &Service{Core: k, Operators: ops}
}

// ForOrg returns the same server's view of another organisation.
func (s *Service) ForOrg(org string) *Service {
	return NewService(s.Core.ForOrg(org), s.Operators.ForOrg(org))
}
