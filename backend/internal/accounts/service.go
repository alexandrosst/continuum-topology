// Package accounts is who is calling and what they may do: people, organisations, roles, sessions, API tokens, passkeys and two-factor
// sign-in, the one decision of what a caller may do (Decide), and the words for who did a thing (Actor). It stands on the kernel and on
// nothing else of the server.
package accounts

import "ikhnos/internal/kernel"

// Service is the kernel's Core with what accounts does with it. The server's Core holds one (Core.Accounts), made for the same
// organisation view as the Core it sits in.
type Service struct{ *kernel.Core }

// NewService is the accounts service for k.
func NewService(k *kernel.Core) *Service { return &Service{k} }
