package twin

import (
	"testing"
	"time"
)

func TestAssessStatesAndTransitionsWithAnInjectedClock(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	window := 2 * time.Minute
	seen := t0
	cases := []struct {
		name string
		in   AssessInput
		want State
		text string
	}{
		{"connected and heard from just now", AssessInput{Now: t0.Add(10 * time.Second), LastObserved: seen, Connected: true, StaleAfter: window}, Live, ""},
		{"connected, exactly at the window", AssessInput{Now: t0.Add(window), LastObserved: seen, Connected: true, StaleAfter: window}, Live, ""},
		{"connected but silent past the window", AssessInput{Now: t0.Add(2 * time.Hour), LastObserved: seen, Connected: true, StaleAfter: window}, Stale, "stale for 2 h"},
		{"stream closed a moment ago", AssessInput{Now: t0.Add(40 * time.Second), LastObserved: seen, StaleAfter: window}, Disconnected, "agent disconnected; last heard 40 s ago"},
		{"stream closed long ago", AssessInput{Now: t0.Add(3 * time.Hour), LastObserved: seen, StaleAfter: window}, Stale, "stale for 3 h"},
		{"revoked beats everything", AssessInput{Now: t0.Add(time.Minute), LastObserved: seen, Connected: true, Revoked: true, RevokedAt: t0.Add(-3 * time.Hour), StaleAfter: window}, Revoked, "agent revoked 3 h ago"},
		{"revoked without a time", AssessInput{Now: t0, LastObserved: seen, Revoked: true, StaleAfter: window}, Revoked, "agent revoked"},
		{"never heard from", AssessInput{Now: t0, Connected: true, StaleAfter: window}, Stale, "never heard from its agent"},
		{"clock behind the agent (negative age) is not stale", AssessInput{Now: t0.Add(-time.Minute), LastObserved: seen, Connected: true, StaleAfter: window}, Live, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Assess(c.in)
			if got.State != c.want || got.Reason != c.text {
				t.Errorf("got %s %q, want %s %q", got.State, got.Reason, c.want, c.text)
			}
			if got.State.Actionable() != (c.want == Live) {
				t.Errorf("only live is actionable, %s said %v", got.State, got.State.Actionable())
			}
		})
	}
	// One agent walking through its life.
	in := AssessInput{LastObserved: seen, Connected: true, StaleAfter: window}
	for _, step := range []struct {
		at        time.Duration
		connected bool
		want      State
	}{{0, true, Live}, {60 * time.Second, true, Live}, {61 * time.Second, false, Disconnected}, {119 * time.Second, false, Disconnected}, {121 * time.Second, false, Stale}, {time.Hour, true, Stale}} {
		in.Now, in.Connected = t0.Add(step.at), step.connected
		if got := Assess(in).State; got != step.want {
			t.Errorf("at %s connected=%v: %s, want %s", step.at, step.connected, got, step.want)
		}
	}
}

func TestHumanAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0 s", 40 * time.Second: "40 s", 89 * time.Second: "89 s", 90 * time.Second: "2 min", 12 * time.Minute: "12 min",
		59 * time.Minute: "59 min", 89 * time.Minute: "1 h", 91 * time.Minute: "2 h", 2 * time.Hour: "2 h", 47 * time.Hour: "47 h", 72 * time.Hour: "3 d", -time.Second: "0 s",
	} {
		if got := HumanAge(d); got != want {
			t.Errorf("HumanAge(%s) = %q, want %q", d, got, want)
		}
	}
}
