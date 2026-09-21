package agent

import (
	"errors"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClampPollHoldsTheServersPaceToTwoThroughSixtySeconds(t *testing.T) {
	s := time.Second
	for in, want := range map[time.Duration]time.Duration{
		0: 2 * s, -5 * s: 2 * s, 1 * s: 2 * s, 2 * s: 2 * s, 5 * s: 5 * s, 60 * s: 60 * s, 61 * s: 60 * s, 100 * time.Hour: 60 * s, time.Duration(1<<63 - 1): 60 * s,
	} {
		if got := ClampPoll(in); got != want {
			t.Errorf("ClampPoll(%v) = %v, want %v", in, got, want)
		}
	}
	if got := (Floors{Poll: 50 * time.Millisecond}).poll(time.Millisecond); got != 50*time.Millisecond {
		t.Errorf("test floor: %v", got)
	}
}

func TestNextPollDelayJittersBacksOffAndStaysInBounds(t *testing.T) {
	base := 5 * time.Second
	// Jitter is plus or minus 20% around the base.
	lo, hi := nextPollDelay(base, 0, MinPoll, 0), nextPollDelay(base, 0, MinPoll, 0.999999)
	if lo != 4*time.Second || hi < 5900*time.Millisecond || hi > 6*time.Second {
		t.Fatalf("jitter range %v..%v", lo, hi)
	}
	// Never below the floor, even for a small base.
	if got := nextPollDelay(2*time.Second, 0, MinPoll, 0); got != MinPoll {
		t.Fatalf("below the floor: %v", got)
	}
	// Never above 60 s while things are fine.
	if got := nextPollDelay(60*time.Second, 0, MinPoll, 0.999999); got != MaxPoll {
		t.Fatalf("above the cap: %v", got)
	}
	// Failures double the wait, up to two minutes.
	prev := time.Duration(0)
	for f := 1; f <= 12; f++ {
		d := nextPollDelay(base, f, MinPoll, 0.5)
		if d < prev || d > MaxPollBackoff {
			t.Fatalf("after %d failures: %v (previous %v)", f, d, prev)
		}
		prev = d
	}
	if prev != MaxPollBackoff {
		t.Fatalf("the backoff must reach its cap, got %v", prev)
	}
	if got := nextPollDelay(base, 1, MinPoll, 0.5); got < 9*time.Second || got > 11*time.Second {
		t.Fatalf("one failure should about double the base: %v", got)
	}
}

func TestHoldForIsFiveToTenMinutesUnlessConfigured(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 500; i++ {
		d := holdFor(0)
		if d < 5*time.Minute || d >= 10*time.Minute {
			t.Fatalf("hold = %v", d)
		}
		seen[d/time.Second] = true
	}
	if len(seen) < 100 {
		t.Fatalf("the hold is not spread: %d distinct seconds", len(seen))
	}
	if holdFor(-1) != -1 || holdFor(time.Second) != time.Second {
		t.Fatal("a configured hold is used as is")
	}
}

func TestRevokedDetailComesOnlyFromUnauthenticatedStatusesThatCarryIt(t *testing.T) {
	with := func(code codes.Code, reason string) error {
		st, _ := status.New(code, "x").WithDetails(&continuumv1.Revoked{Reason: reason})
		return st.Err()
	}
	if r, ok := revokedDetail(with(codes.Unauthenticated, "cluster decommissioned")); !ok || r != "cluster decommissioned" {
		t.Fatalf("%q %v", r, ok)
	}
	if r, ok := revokedDetail(with(codes.Unauthenticated, "")); !ok || r == "" {
		t.Fatalf("an empty reason still means revoked: %q %v", r, ok)
	}
	for _, err := range []error{with(codes.Internal, "x"), status.Error(codes.Unauthenticated, "certificate expired"), errors.New("boom"), nil} {
		if _, ok := revokedDetail(err); ok {
			t.Errorf("%v is not a revocation", err)
		}
	}
}

func TestNoteServerTimeWarnsOncePerHourAndOnlyBeyondTwoMinutes(t *testing.T) {
	var lines []string
	r := &runner{log: captureLog(&lines)}
	now := time.Now()
	r.noteServerTime(now.Add(-30 * time.Second).Unix()) // agent 30 s ahead: fine
	if len(lines) != 0 {
		t.Fatalf("30 s must not warn: %v", lines)
	}
	if ms := r.clockSkewMs(); ms < 29000 || ms > 32000 {
		t.Fatalf("skew = %d ms", ms)
	}
	r.noteServerTime(now.Add(-3 * time.Minute).Unix()) // ahead by three minutes
	r.noteServerTime(now.Add(-3 * time.Minute).Unix())
	if len(lines) != 1 || !contains(lines[0], "ahead of") || !contains(lines[0], "3m0s") {
		t.Fatalf("one warning expected: %v", lines)
	}
	r.ex.skewWarned = time.Now().Add(-61 * time.Minute)
	r.noteServerTime(now.Add(4 * time.Minute).Unix()) // behind by four
	if len(lines) != 2 || !contains(lines[1], "behind") {
		t.Fatalf("a second warning after an hour: %v", lines)
	}
	if ms := r.clockSkewMs(); ms > -239000 || ms < -242000 {
		t.Fatalf("a clock that is behind reports a negative skew: %d", ms)
	}
	r2 := &runner{log: captureLog(&lines)}
	r2.noteServerTime(0) // an older server
	if r2.clockSkewMs() != 0 {
		t.Fatal("no server time, no skew")
	}
}

func TestClockHint(t *testing.T) {
	if clockHint(errors.New("rpc error: x509: certificate has expired or is not yet valid: current time 2030 is after 2026")) == "" {
		t.Fatal("expected a hint")
	}
	if clockHint(errors.New("connection refused")) != "" || clockHint(nil) != "" {
		t.Fatal("no hint for other errors")
	}
}
