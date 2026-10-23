package connectivity

import (
	"testing"
	"time"

	"ikhnos/internal/measure"
)

func TestPathSummaryMergesRounds(t *testing.T) {
	p := &PathTrack{}
	at := time.Now()
	p.Add(measure.Result{Samples: 5, Failed: 0, Min: 10, P50: 12, P95: 20}, at)
	p.Add(measure.Result{Samples: 5, Failed: 5}, at) // an outage round must not pull times to zero
	p.Add(measure.Result{Samples: 5, Failed: 1, Min: 9, P50: 14, P95: 30}, at)
	min, p50, p95, loss, n := p.Summary()
	if min != 9 || p50 != 14 || p95 != 30 || n != 15 {
		t.Fatalf("summary = %v %v %v %v %v", min, p50, p95, loss, n)
	}
	if loss < 39 || loss > 41 { // 6 of 15
		t.Fatalf("loss = %v", loss)
	}
	for i := 0; i < 10; i++ {
		p.Add(measure.Result{Samples: 5, Min: 1, P50: 2, P95: 3}, at)
	}
	if len(p.Rounds) != roundsKept {
		t.Fatalf("kept %d rounds", len(p.Rounds))
	}
	if _, _, _, loss, _ := p.Summary(); loss != 0 {
		t.Fatal("old failures were not forgotten")
	}
}
