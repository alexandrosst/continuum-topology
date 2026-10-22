package store

import (
	"testing"

	"continuum/internal/wire"
)

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfAnEventAreItsConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", EventSeverity(""), EventTarget(""), OperatorStatus(""), DestinationKind(""), Modality(""), ReceiverAuth(""), CAScope(""), TelemetryIntentStatus(""), ProposalStatus(""), EvidenceClass(""), OutcomeResult(""))
}
