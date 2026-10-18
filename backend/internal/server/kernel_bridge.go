// Package server holds the control-plane logic shared by the gRPC and HTTP front ends.
package server

import (
	"log/slog"

	"continuum/internal/accounts"
	"continuum/internal/kernel"
	"continuum/internal/pki"
	"continuum/internal/store"
)

// Core is the kernel's Core with what the modules do with it. The kernel does not know the modules, so each module that is a package
// is a field here (Accounts, ...), and what is still a file of this package is declared on Core itself. A module that becomes a
// package takes its methods with it and leaves a field.
type Core struct {
	*kernel.Core
	Accounts *accounts.Service
}

func wrap(k *kernel.Core) *Core { return &Core{Core: k, Accounts: accounts.NewService(k)} }

// NewCore returns a Core scoped to org (see kernel.NewCore).
func NewCore(st store.Store, ca *pki.CA, org string, log *slog.Logger) *Core {
	return wrap(kernel.NewCore(st, ca, org, log))
}

// ForOrg returns a view of the same server scoped to one organisation (see kernel.Core.ForOrg).
func (c *Core) ForOrg(org string) *Core { return wrap(c.Core.ForOrg(org)) }
