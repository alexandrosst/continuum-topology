package operators

import (
	"context"

	"ikhnos/internal/kernel"
)

// Central is the central operator as the server itself runs it: the gateway of the FUSION bundled with the server's chart. The operators
// know it only as this port, so whoever runs it calls operators and the operators do not know them (see ManagedStatus). A server without
// FUSION has no Central, and the code that asks for one gets a nil interface, not an interface holding a nil pointer.
type Central interface {
	// Org is the organisation the gateway is managed from, or empty when any organisation may.
	Org() string
	// Managed is the state of whoever runs the gateway, which stands in for the heartbeat the central operator does not send.
	Managed(ctx context.Context) ManagedStatus
	// Exposed says whether the gateway has an address other clusters can reach, and Endpoint is where an exporter dials it (its
	// in-cluster name while it is not exposed).
	Exposed() bool
	Endpoint() string
	// WithGatewayPort completes an address a person gave with the port the gateway listens on, which a node port is not.
	// SetPublicAddress records the address from now on; no certificate changes with it.
	WithGatewayPort(ctx context.Context, addr string) string
	SetPublicAddress(addr string)
	// GatewayService is the gateway's own Service, read by the server; nil when it cannot be read.
	GatewayService(ctx context.Context) *kernel.KubeService
	// ServiceName and Namespace say where the gateway runs, and ClusterUID is the UID of the server's own cluster as the install told it.
	ServiceName() string
	Namespace() string
	ClusterUID() string
}
