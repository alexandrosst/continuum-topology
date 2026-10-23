// Package server holds the control-plane logic shared by the gRPC and HTTP front ends.
package server

import (
	"log/slog"

	"ikhnos/internal/accounts"
	"ikhnos/internal/agents"
	"ikhnos/internal/connectivity"
	"ikhnos/internal/decisionloop"
	"ikhnos/internal/estate"
	"ikhnos/internal/fusion"
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
	"ikhnos/internal/pki"
	"ikhnos/internal/store"
	"ikhnos/internal/telemetry"
)

// Core is the kernel's Core with what the modules do with it. The kernel does not know the modules, so each module that is a package
// is a field here (Accounts, ...), and what is still a file of this package is declared on Core itself. A module that becomes a
// package takes its methods with it and leaves a field.
type Core struct {
	*kernel.Core
	Accounts *accounts.Service
	// Agents is the enrolled agents' side of the Core: enrolment, consent, tiers, conditions, the stream.
	Agents *agents.Service
	// Connectivity holds the sealed credentials of the links.
	Connectivity *connectivity.Service
	// Loop is the decision loop. Its ports are set when the Hub is built.
	Loop *decisionloop.Service
	// Operators and Telemetry are the regional operators and the telemetry intents that may point at them.
	Operators *operators.Service
	Telemetry *telemetry.Service
	// Estate is the hub's side of the Core: what the Hub does with it and the saved workspace.
	Estate *estate.Service
	// Fusion is what the FUSION control stands on: the Core and the operators the central operator is one of.
	Fusion *fusion.Service
}

func wrap(k *kernel.Core) *Core {
	ops := operators.NewService(k)
	tel := telemetry.NewService(k, ops)
	ag, conn, loop := agents.NewService(k, ops, tel), connectivity.NewService(k), decisionloop.NewService(k)
	return &Core{Core: k, Accounts: accounts.NewService(k), Agents: ag, Connectivity: conn, Loop: loop, Operators: ops, Telemetry: tel, Estate: estate.NewService(k, ag, conn, loop), Fusion: fusion.NewService(k, ops)}
}

// NewCore returns a Core scoped to org (see kernel.NewCore).
func NewCore(st store.Store, ca *pki.CA, org string, log *slog.Logger) *Core {
	return wrap(kernel.NewCore(st, ca, org, log))
}

// ForOrg returns a view of the same server scoped to one organisation (see kernel.Core.ForOrg).
func (c *Core) ForOrg(org string) *Core { return wrap(c.Core.ForOrg(org)) }
