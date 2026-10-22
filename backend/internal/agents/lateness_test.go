package agents

import (
	"testing"
	"time"
)

func TestAnAgentIsLateFiveEighthsOfTheWayToStale(t *testing.T) {
	const beat = 30 * time.Second
	for _, tc := range []struct {
		beats int
		want  time.Duration
	}{
		{2, 37500 * time.Millisecond},
		{4, 75 * time.Second}, // the default: 4 beats stale, 2.5 beats late
		{8, 150 * time.Second},
		{20, 375 * time.Second},
	} {
		stale := time.Duration(tc.beats) * beat
		got := LateAfter(stale)
		if got != tc.want {
			t.Errorf("%d beats: late after %v, want %v", tc.beats, got, tc.want)
		}
		if got <= beat || got >= stale {
			t.Errorf("%d beats: late after %v must be more than one heartbeat (%v) and less than stale (%v)", tc.beats, got, beat, stale)
		}
	}
}
