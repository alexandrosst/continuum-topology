package estate

import "ikhnos/internal/install"

// The bodies this package's routes read. They were anonymous structs at the handlers; naming them puts them in the TypeScript written
// from the Go types (internal/wire), so the page sends what the server reads.

// createTokenRequest is the body of POST /tokens.
type createTokenRequest struct {
	Name string `json:"name"`
	Tier int    `json:"tier"`
	// ExpectedCluster optionally binds the token to one cluster: the UID of its kube-system namespace.
	ExpectedCluster string `json:"expectedCluster"`
	// Collectors are the optional per-node collectors the install switches on, and Scope the namespaces it may report. They are
	// parts of the command and of nothing else: the token does not record them.
	Collectors install.Collectors `json:"collectors"`
	Scope      install.Scope      `json:"scope"`
	// PurgeIdentity asks the install to delete the agent's identity Secret when the release is uninstalled (a post-delete hook in the
	// chart). Helm uninstall takes no values, so this has to be chosen when the agent is installed; an upgrade keeps it.
	PurgeIdentity bool `json:"purgeIdentity"`
}

// approveAgentRequest is the body of POST /agents/{id}/approve.
type approveAgentRequest struct {
	// Code is the approval code the agent printed in its log. Confirm is the older fingerprint prefix,
	// still accepted for an agent that enrolled without a code.
	Code    string `json:"code"`
	Confirm string `json:"confirm"`
	Tier    int    `json:"tier"`
}

// setAgentTierRequest is the body of PUT /agents/{id}/tier. Tier is required: a body without it is refused.
type setAgentTierRequest struct {
	Tier *int `json:"tier"`
}

// setAgentConsentRequest is the body of PUT /agents/{id}/consent: the whole set of overrides, which replaces the last.
type setAgentConsentRequest struct {
	PausedCollectors   []string `json:"pausedCollectors"`
	ExcludedNamespaces []string `json:"excludedNamespaces"`
}

// addressTestsRequest is the body of POST /agents/{id}/address-tests.
type addressTestsRequest struct {
	Addresses []string `json:"addresses"`
}

// serverClusterRequest is the body of PUT /operators/central/server-cluster. An empty ClusterID takes the choice back.
type serverClusterRequest struct {
	ClusterID string `json:"clusterId"`
}
