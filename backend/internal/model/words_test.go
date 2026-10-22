package model

import (
	"testing"

	"continuum/internal/wire"
)

func TestEveryWordOfTheRecordsIsListed(t *testing.T) {
	wire.RequireWordsComplete(t, ".",
		Confidence(""),
		Source(""),
		ObservationState(""),
		Tier(""),
		Status(""),
		MtlsMode(""),
		NodeRole(""),
		MachineKind(""),
		ServiceKind(""),
		ManagedBy(""),
		EndpointKind(""),
		DependencySource(""),
		ExternalKind(""),
		PeerKind(""),
		PodDirection(""),
		PathSource(""),
		LinkKind(""),
		TunnelEncryption(""),
		PairStatus(""),
		Noise(""),
		ObservedVia(""),
		TLSHandshake(""),
		SuggestionKind(""),
		SuggestionStatus(""),
		MeshProxy(""),
		IdentityBasis(""),
		Connectivity(""),
		ServiceExposure(""),
	)
}
