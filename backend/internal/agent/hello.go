package agent

import (
	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agentcaps"
)

// hello is the first message on a stream: what this agent is, what its install allows, the protocol it speaks and the things it
// does. The capabilities are every name in the registry, because the registry is this build's own list and a name is added to it
// only with the code that does the thing (hello_internal_test.go checks each one). An agent does not choose its names per
// install: what an install does not allow is the tier's business, and is said in Diagnostics.
func (r *runner) hello() *ikhnosv1.Hello {
	return &ikhnosv1.Hello{
		AgentVersion: r.cfg.Version, InstalledAccessTier: uint32(r.cfg.Tier), Diagnostics: r.diagnostics(),
		Namespace: r.cfg.Namespace, ReleaseName: r.cfg.ReleaseName,
		ProtocolVersion: agentcaps.ProtocolVersion, Capabilities: agentcaps.Names(),
	}
}
