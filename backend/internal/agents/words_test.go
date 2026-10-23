package agents

import (
	"testing"

	"ikhnos/internal/wire"
)

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfAnExportRouteAreTheirConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", ExportState(""))
}
