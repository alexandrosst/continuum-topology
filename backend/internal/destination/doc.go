// Package destination is the API form of a telemetry destination, the one shape the operators' routes and the telemetry intents that point
// at an operator or an external backend both carry. It is a document and nothing more: what makes a destination acceptable is the operators'.
package destination

import "ikhnos/internal/store"

// Doc is a destination as the API carries it, for the operators' routes and for the telemetry intents that point at an
// operator or at an external backend.
type Doc struct {
	Kind           store.DestinationKind `json:"kind"`
	Endpoint       string                `json:"endpoint"`
	Insecure       bool                  `json:"insecure,omitempty"`
	CAFile         string                `json:"caFile,omitempty"`
	AuthHeaderName string                `json:"authHeaderName,omitempty"`
	AuthSecretName string                `json:"authSecretName,omitempty"`
	AuthSecretKey  string                `json:"authSecretKey,omitempty"`
	// TargetOperatorID is only meaningful for destination kind "operator" - see store.Destination's own
	// field.
	TargetOperatorID string `json:"targetOperatorId,omitempty"`
	// Address is only meaningful for destination kind "operator" - see store.Destination's own field.
	Address string `json:"address,omitempty"`
	// FusionRelease and FusionNamespace are only meaningful for destination kind "fusion" - see
	// store.Destination's own fields. Both default (to "fusion" and "ikhnos-system") when left empty.
	FusionRelease   string `json:"fusionRelease,omitempty"`
	FusionNamespace string `json:"fusionNamespace,omitempty"`
}

// Of is the API form of d.
func Of(d store.Destination) Doc {
	return Doc{Kind: d.Kind, Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, Address: d.Address, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
}

// ToStore is the destination the API form names, not yet validated (operators.Service.CheckDestination does that).
func (d Doc) ToStore() store.Destination {
	return store.Destination{Kind: d.Kind, Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey, TargetOperatorID: d.TargetOperatorID, Address: d.Address, FusionRelease: d.FusionRelease, FusionNamespace: d.FusionNamespace}
}
