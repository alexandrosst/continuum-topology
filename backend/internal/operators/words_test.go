package operators

import (
	"testing"

	"continuum/internal/wire"
)

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfAnOperatorsHealthAndCertificatesAreTheirConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", HealthState(""), CertState(""))
}
