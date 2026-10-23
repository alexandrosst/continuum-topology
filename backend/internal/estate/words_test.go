package estate

import (
	"testing"

	"ikhnos/internal/wire"
)

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfAnAddressAndOfTheServersClusterAreTheirConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", CandidateKind(""), AddressNetwork(""), ServerClusterState(""), ServerClusterSource(""), ServerClusterNodes(""))
}
