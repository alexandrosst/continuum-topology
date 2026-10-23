package workspace

import (
	"testing"

	"ikhnos/internal/wire"
)

func TestEveryWordOfTheDeclaredRecordsIsListed(t *testing.T) {
	wire.RequireWordsComplete(t, ".", SiteKind(""), TrustZone(""), DeviceKind(""), SiteLinkSource(""))
}
