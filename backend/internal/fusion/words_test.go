package fusion

import (
	"testing"

	"ikhnos/internal/wire"
)

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfFusionAreItsConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", Component(""), FusionState(""), FusionReason(""), UsedSource(""), RetentionReason(""))
}

// Every reason the switch can give is one the retention can give too, because the retention passes it on when FUSION is not available.
func TestEveryReasonOfTheSwitchIsAReasonOfTheRetention(t *testing.T) {
	have := map[string]bool{}
	for _, w := range RetentionReason("").WireWords() {
		have[w] = true
	}
	for _, w := range FusionReason("").WireWords() {
		if !have[w] {
			t.Errorf("%q is a reason of the switch but not of the retention", w)
		}
	}
}
