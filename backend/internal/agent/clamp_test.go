package agent

import (
	"testing"
	"time"
)

func TestClampsHoldServerSuppliedTimingsInRange(t *testing.T) {
	s := time.Second
	for _, c := range []struct {
		name      string
		f         func(time.Duration) time.Duration
		in, want  time.Duration
		floor, hi time.Duration
	}{
		{"resync below", ClampResync, 1 * s, 60 * s, 60 * s, 24 * time.Hour},
		{"resync zero", ClampResync, 0, 60 * s, 60 * s, 24 * time.Hour},
		{"resync negative", ClampResync, -5 * s, 60 * s, 60 * s, 24 * time.Hour},
		{"resync floor", ClampResync, 60 * s, 60 * s, 60 * s, 24 * time.Hour},
		{"resync normal", ClampResync, 10 * time.Minute, 10 * time.Minute, 60 * s, 24 * time.Hour},
		{"resync above", ClampResync, 1000 * time.Hour, 24 * time.Hour, 60 * s, 24 * time.Hour},
		{"beat below", ClampHeartbeat, 1 * s, 10 * s, 10 * s, 2 * time.Minute},
		{"beat zero", ClampHeartbeat, 0, 10 * s, 10 * s, 2 * time.Minute},
		{"beat normal", ClampHeartbeat, 30 * s, 30 * s, 10 * s, 2 * time.Minute},
		{"beat above", ClampHeartbeat, time.Hour, 2 * time.Minute, 10 * s, 2 * time.Minute},
		{"measure below", ClampMeasureInterval, 1 * s, 30 * s, 30 * s, time.Hour},
		{"measure negative", ClampMeasureInterval, -1, 30 * s, 30 * s, time.Hour},
		{"measure normal", ClampMeasureInterval, 5 * time.Minute, 5 * time.Minute, 30 * s, time.Hour},
		{"measure above", ClampMeasureInterval, 100 * time.Hour, time.Hour, 30 * s, time.Hour},
		{"measure max int", ClampMeasureInterval, time.Duration(1<<63 - 1), time.Hour, 30 * s, time.Hour},
	} {
		if got := c.f(c.in); got != c.want {
			t.Errorf("%s: %v -> %v, want %v", c.name, c.in, got, c.want)
		}
	}
	// Idempotent and monotonic over a sweep.
	for _, f := range []func(time.Duration) time.Duration{ClampResync, ClampHeartbeat, ClampMeasureInterval} {
		prev := f(0)
		for d := time.Duration(0); d < 200*time.Hour; d += 7 * time.Minute {
			got := f(d)
			if got < prev || f(got) != got {
				t.Fatalf("not monotonic or not idempotent at %v", d)
			}
			prev = got
		}
	}
}
