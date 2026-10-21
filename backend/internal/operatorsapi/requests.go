package operatorsapi

import (
	"continuum/internal/destination"
	"continuum/internal/install"
)

// The bodies this package's routes read. They were anonymous structs at the handlers; naming them puts them in the TypeScript written
// from the Go types (internal/wire), so the page sends what the server reads.

// createOperatorRequest is the body of POST .../operators.
type createOperatorRequest struct {
	Name               string          `json:"name"`
	SourceClusterIDs   []string        `json:"sourceClusterIds"`
	Destination        destination.Doc `json:"destination"`
	AcceptedModalities []string        `json:"acceptedModalities,omitempty"`
	// Labels are fixed at creation: they live in the operator's own install (see store.OperatorLabel).
	Labels []labelDoc `json:"labels,omitempty"`
	// Heartbeat opts the new operator in to reporting that it is alive (see EnableOperatorHeartbeat).
	// Absent means false: an older client that has never heard of the field must not quietly make an
	// operator start calling this server (and an install whose heartbeat.url is plain HTTP is refused by the
	// chart). The UI sends true by default and says what it does.
	Heartbeat bool `json:"heartbeat,omitempty"`
	// Exposure is how the operator's Service is made reachable from other clusters: "" or "cluster" (it is
	// not: in-cluster only), "loadbalancer" or "nodeport". It sets the install command's service.type and is
	// kept (store.Operator.Exposure) so the page can say an address is still to be recorded; the address that
	// results is learned afterwards (POST .../address).
	Exposure string `json:"exposure,omitempty"`
	// ExtraProcessors are the OTel Collector processors to add to the operator's pipelines. They are chart values and nothing more: the
	// operator record does not keep them, so they are in this command and in no other.
	ExtraProcessors []install.Processor `json:"extraProcessors,omitempty"`
}

// setOperatorAddressRequest is the body of POST .../operators/{id}/address: the host:port other clusters reach the operator at ("" clears it).
type setOperatorAddressRequest struct {
	Address string `json:"address"`
}

// updateOperatorScopeRequest is the body of PUT .../operators/{id}/scope.
type updateOperatorScopeRequest struct {
	SourceClusterIDs   []string        `json:"sourceClusterIds"`
	Destination        destination.Doc `json:"destination"`
	AcceptedModalities []string        `json:"acceptedModalities,omitempty"`
}

// revokeOperatorRequest is the body of POST .../operators/{id}/revoke.
type revokeOperatorRequest struct {
	Reason string `json:"reason"`
	// Force confirms revoking an operator that others still send to (see Core.RevokeOperatorForce).
	Force bool `json:"force,omitempty"`
}
