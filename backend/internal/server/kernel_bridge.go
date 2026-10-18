// Package server holds the control-plane logic shared by the gRPC and HTTP front ends.
package server

import (
	"log/slog"

	"continuum/internal/kernel"
	"continuum/internal/pki"
	"continuum/internal/store"
)

// Core is the kernel's Core with the methods the modules add to it. The kernel does not know the modules, so what a module does to a
// Core (accounts, operators, telemetry, fusion, ...) is declared here, on the type that embeds the kernel's. That stays only while the
// modules are files of this package: each module that becomes a package takes its methods with it.
type Core struct{ *kernel.Core }

// NewCore returns a Core scoped to org (see kernel.NewCore).
func NewCore(st store.Store, ca *pki.CA, org string, log *slog.Logger) *Core {
	return &Core{kernel.NewCore(st, ca, org, log)}
}

// ForOrg returns a view of the same server scoped to one organisation (see kernel.Core.ForOrg).
func (c *Core) ForOrg(org string) *Core { return &Core{c.Core.ForOrg(org)} }
