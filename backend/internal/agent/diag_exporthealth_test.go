package agent

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// Export health changes a message when a route changes state (or the collectors become unreadable), not on
// every reading: the counters and times behind a state move all the time.
func TestDiagSignatureIgnoresExportCountersButNotStates(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	mk := func(sent uint64, state continuumv1.ExportRouteHealth_State, reached uint32, when time.Time) *continuumv1.Diagnostics {
		return &continuumv1.Diagnostics{ExportHealth: &continuumv1.ExportHealth{
			ScrapedAt: timestamppb.New(when), PodsReached: reached,
			Routes: []*continuumv1.ExportRouteHealth{{Exporter: "otlp", Signal: "logs", State: state, Sent: sent, LastSentAt: timestamppb.New(when)}},
		}}
	}
	base := string(diagSignature(mk(10, continuumv1.ExportRouteHealth_EXPORTING, 2, at)))
	if got := string(diagSignature(mk(99, continuumv1.ExportRouteHealth_EXPORTING, 2, at.Add(time.Minute)))); got != base {
		t.Error("counters and times moving alone must not make a new message")
	}
	if got := string(diagSignature(mk(10, continuumv1.ExportRouteHealth_FAILING, 2, at))); got == base {
		t.Error("a route changing state must make a new message")
	}
	if got := string(diagSignature(mk(10, continuumv1.ExportRouteHealth_EXPORTING, 0, at))); got == base {
		t.Error("the collectors becoming unreadable must make a new message")
	}
	d := mk(10, continuumv1.ExportRouteHealth_EXPORTING, 2, at)
	_ = diagSignature(d)
	if d.ExportHealth.Routes[0].Sent != 10 || d.ExportHealth.ScrapedAt == nil {
		t.Error("diagSignature must not change the message it describes")
	}
}
