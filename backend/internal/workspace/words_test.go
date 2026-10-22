package workspace

import (
	"testing"

	"continuum/internal/wire"
)

func TestEveryWordOfTheDeclaredRecordsIsListed(t *testing.T) {
	wire.RequireWordsComplete(t, ".", SiteKind(""), TrustZone(""), DeviceKind(""), SiteLinkSource(""))
}
