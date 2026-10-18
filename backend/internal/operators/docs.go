package operators

import "continuum/internal/store"

// DestinationDoc is a destination as the API carries it, for the operators' routes and for the telemetry intents that point at an
// operator or at an external backend.
type DestinationDoc struct {
	Kind           string `json:"kind"`
	Endpoint       string `json:"endpoint"`
	Insecure       bool   `json:"insecure,omitempty"`
	CAFile         string `json:"caFile,omitempty"`
	AuthHeaderName string `json:"authHeaderName,omitempty"`
	AuthSecretName string `json:"authSecretName,omitempty"`
	AuthSecretKey  string `json:"authSecretKey,omitempty"`
	// TargetOperatorID is only meaningful for destination kind "operator" - see store.Destination's own
	// field.
	TargetOperatorID string `json:"targetOperatorId,omitempty"`
	// Address is only meaningful for destination kind "operator" - see store.Destination's own field.
	Address string `json:"address,omitempty"`
	// FusionRelease and FusionNamespace are only meaningful for destination kind "fusion" - see
	// store.Destination's own fields. Both default (to "fusion" and "continuum-system") when left empty.
	FusionRelease   string `json:"fusionRelease,omitempty"`
	FusionNamespace string `json:"fusionNamespace,omitempty"`
}

// ToDestinationDoc is the API form of d.
func ToDestinationDoc(d store.Destination) DestinationDoc {
	return DestinationDoc{Kind: string(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, Address: d.Address, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
}

// ToStore is the destination the API form names, not yet validated (Core.validateDestination does that).
func (d DestinationDoc) ToStore() store.Destination {
	return store.Destination{Kind: store.DestinationKind(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, Address: d.Address, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
}
