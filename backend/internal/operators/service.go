// Package operators is the server's side of the regional operators: the operators themselves, the certificates they and their source
// clusters hold, their heartbeats and health, who uses them, and where an operator's own destination leads.
//
// It stands on the kernel and the store. The central operator is an operator like the others whose manager is FUSION; this package
// knows that only as an id and as a ManagedStatus it is handed, so FUSION calls operators and operators do not know FUSION.
package operators

import "ikhnos/internal/kernel"

// Service is the operators of one organisation.
type Service struct{ *kernel.Core }

// NewService returns the operators of the organisation k is scoped to.
func NewService(k *kernel.Core) *Service { return &Service{Core: k} }

// ForOrg returns the operators of another organisation of the same server.
func (s *Service) ForOrg(org string) *Service { return NewService(s.Core.ForOrg(org)) }
