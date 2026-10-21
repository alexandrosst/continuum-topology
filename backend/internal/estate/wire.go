package estate

import (
	"continuum/internal/agents"
	"continuum/internal/twin"
	"continuum/internal/wire"
)

// WireTypes are the shapes this package puts on the wire (or reads from it), for the TypeScript the server writes of them
// (internal/wire). A document the package encodes and that is not listed here is not in that file: the page would then have to write its
// type by hand again.
func WireTypes() []wire.Root {
	return []wire.Root{
		{Value: tokenDoc{}},
		{Value: agentUpgradeRequest{}},
		{Value: credentialSecretDoc{}},
		{Value: agentUpgradeDoc{}},
		{Value: eventDoc{}},
		{Value: SelfTelemetryEntity{}},
		{Value: applicationDoc{}},
		{Value: AgentDoc{}},
		{Value: StateDoc{}},
		{Value: TeardownDoc{}},
		{Value: AddressCandidates{}},
		{Value: ServerClusterDoc{}},
		{Value: ExposeDoc{}},
		{Value: TombstoneDoc{}},
		{Value: agents.ConsentDoc{}},
		{Name: "TwinModel", Value: twin.Model{}},
	}
}
