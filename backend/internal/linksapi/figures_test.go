package linksapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/kernel"
)

func lr(cluster, peer, dir string, v float64) fusionapi.LinkReading {
	return fusionapi.LinkReading{Cluster: cluster, Peer: peer, Direction: dir, Value: v}
}

func TestPairsGetTheirFiguresFromWhicheverClusterMeasuredThem(t *testing.T) {
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-c"}, {A: "cl-d", B: "cl-e"}}
	rd := fusionapi.LinkReadings{
		// a measured towards b; b measured towards a and says something else: a is believed
		Loss:       []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.02), lr("cl-a", "cl-b", "reverse", 0), lr("cl-b", "cl-a", "forward", 0.5), lr("cl-b", "cl-a", "reverse", 0.5)},
		Jitter:     []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.0004), lr("cl-b", "cl-a", "forward", 0.006)},
		RTT:        []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 0.012)},
		MTU:        []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 1500), lr("cl-b", "cl-a", "", 1420)},
		Throughput: []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 9.2e8), lr("cl-a", "cl-b", "reverse", 8.7e8)},
		Probed:     []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 1791200000), lr("cl-b", "cl-a", "reverse", 1791200100)},
		Tested:     []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 1791100000)},
		// c only reports from its side, where "forward" is c to a
	}
	rd.Loss = append(rd.Loss, lr("cl-c", "cl-a", "forward", 0.1), lr("cl-c", "cl-a", "reverse", 0.2))
	got := latestPairs(links, rd, time.Unix(1791200200, 0))
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	ab := got[0]
	f := func(p *float64) float64 {
		if p == nil {
			return -1
		}
		return *p
	}
	if f(ab.LossAToB) != 0.02 || f(ab.LossBToA) != 0 || f(ab.RTTSeconds) != 0.012 || f(ab.JitterSeconds) != 0.006 || f(ab.ThroughputAToB) != 9.2e8 || f(ab.ThroughputBToA) != 8.7e8 {
		t.Errorf("a-b = %+v", ab)
	}
	if ab.MTUBytes == nil || *ab.MTUBytes != 1420 {
		t.Errorf("the smaller MTU of the two clusters is the pair's: %v", ab.MTUBytes)
	}
	if ab.ProbedAt == nil || ab.ProbedAt.Unix() != 1791200100 || ab.TestedAt == nil || ab.TestedAt.Unix() != 1791100000 {
		t.Errorf("times: %v %v", ab.ProbedAt, ab.TestedAt)
	}
	ac := got[1]
	if f(ac.LossAToB) != 0.2 || f(ac.LossBToA) != 0.1 { // a to c is c's reverse; c to a is c's forward
		t.Errorf("a-c measured from c's side: %+v / %+v", f(ac.LossAToB), f(ac.LossBToA))
	}
	de := got[2]
	if de.LossAToB != nil || de.RTTSeconds != nil || de.MTUBytes != nil || de.ProbedAt != nil || de.TestedAt != nil || de.A != "cl-d" {
		t.Errorf("a pair nobody reported on has no figures: %+v", de)
	}
}

func lrr(cluster, peer, dir, reason string, v float64) fusionapi.LinkReading {
	return fusionapi.LinkReading{Cluster: cluster, Peer: peer, Direction: dir, Reason: reason, Value: v}
}

// The store answers in no defined order, and a rolled pod can leave two series for one key. The figure shown must not depend on
// which comes last: the worse loss, the larger delay variation and the smaller MTU win.
func TestTwoSeriesOfOneKeyResolveTheSameWhicheverComesLast(t *testing.T) {
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}
	now := time.Unix(1791200200, 0)
	loss := []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 1), lr("cl-a", "cl-b", "forward", 0.1)}
	jit := []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.02), lr("cl-a", "cl-b", "forward", 0.001)}
	mtu := []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 1500), lr("cl-a", "cl-b", "", 1420)}
	for _, rev := range []bool{false, true} {
		l, j, m := append([]fusionapi.LinkReading(nil), loss...), append([]fusionapi.LinkReading(nil), jit...), append([]fusionapi.LinkReading(nil), mtu...)
		if rev {
			l[0], l[1], j[0], j[1], m[0], m[1] = l[1], l[0], j[1], j[0], m[1], m[0]
		}
		got := latestPairs(links, fusionapi.LinkReadings{Loss: l, Jitter: j, MTU: m}, now)[0]
		if got.LossAToB == nil || *got.LossAToB != 1 || got.JitterSeconds == nil || *got.JitterSeconds != 0.02 || got.MTUBytes == nil || *got.MTUBytes != 1420 {
			t.Errorf("reversed=%v: loss %v jitter %v mtu %v", rev, got.LossAToB, got.JitterSeconds, got.MTUBytes)
		}
	}
}

func probeStateOf(p LinkPairLatest) (string, string, string, string) {
	f := func(s *LinkProbeLatest) string {
		if s == nil {
			return "-"
		}
		return string(s.State) + "/" + string(s.Reason)
	}
	d := func(s *LinkProbeLatest) string {
		if s == nil {
			return ""
		}
		return string(s.Diagnosis)
	}
	return f(p.ProbeAToB), f(p.ProbeBToA), d(p.ProbeAToB), d(p.ProbeBToA)
}

func TestProbeStateAndDiagnosisRules(t *testing.T) {
	now := time.Unix(1791200000, 0)
	fresh, old := float64(now.Unix()-60), float64(now.Unix()-3600)
	ab, ba := func(v float64) fusionapi.LinkReading { return lr("cl-a", "cl-b", "forward", v) }, func(v float64) fusionapi.LinkReading { return lr("cl-a", "cl-b", "reverse", v) }
	rttOK := []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 0.012)}
	connFail := func(why string) []fusionapi.LinkReading {
		return []fusionapi.LinkReading{lrr("cl-a", "cl-b", "", why, 4)}
	}
	for _, c := range []struct {
		name         string
		rd           fusionapi.LinkReadings
		noProbe      bool
		wantA, wantB string // state/reason of A to B and B to A
		diagA, diagB string
	}{
		{name: "nothing reported", wantA: "-", wantB: "-"},
		{name: "up in both directions, packets arrive", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(0.01), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "ok/"},
		{name: "every packet lost although the run completed", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(1), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "ok/", diagA: "udp-lost"},
		{name: "99 percent counts as lost, 98 does not", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(0.99), ba(0.98)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "ok/", diagA: "udp-lost"},
		{name: "up but no run completed for long", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(0), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(old)}},
			wantA: "ok/", wantB: "ok/", diagB: "stale"},
		{name: "the field case: forward lossy, reverse refused by the responder",
			rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(0)}, Loss: []fusionapi.LinkReading{ab(1)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(old)}, RTT: rttOK,
				ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "reverse", "other", 9)}},
			wantA: "ok/", wantB: "failing/other", diagA: "udp-lost", diagB: "denied"},
		{name: "authentication refused, connections fine", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, Probed: []fusionapi.LinkReading{ab(old), ba(old)}, RTT: rttOK,
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "auth", 2), lrr("cl-a", "cl-b", "reverse", "auth", 2)}},
			wantA: "failing/auth", wantB: "failing/auth", diagA: "denied", diagB: "denied"},
		{name: "nothing listens on the control port", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, ConnectFailure: connFail("refused"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "refused", 5), lrr("cl-a", "cl-b", "reverse", "refused", 5)}},
			wantA: "never/refused", wantB: "never/refused", diagA: "no-responder", diagB: "no-responder"},
		{name: "the control port does not answer", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, ConnectFailure: connFail("timeout"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "timeout", 5)}},
			wantA: "never/timeout", wantB: "never/", diagA: "filtered", diagB: "filtered"},
		{name: "connections failed lately but were also timed: the evidence is mixed", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, RTT: rttOK, ConnectFailure: connFail("refused"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "other", 5)}},
			wantA: "never/other", wantB: "never/"},
		{name: "one direction down with nothing else known", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "failing/", diagB: "one-way"},
		{name: "pinned to a kind of address the peer lacks says nothing more", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, ConnectFailure: connFail("timeout"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "no_such_kind", 5)}},
			wantA: "never/no_such_kind", wantB: "never/", diagB: "filtered"},
		{name: "reported from the second cluster's side", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{lr("cl-b", "cl-a", "forward", 0), lr("cl-b", "cl-a", "reverse", 1)}, Probed: []fusionapi.LinkReading{lr("cl-b", "cl-a", "reverse", fresh)}},
			wantA: "ok/", wantB: "never/", diagB: "one-way"},
		{name: "probing off for the pair", noProbe: true, rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}}, wantA: "-", wantB: "-"},
	} {
		got := latestPairs([]kernel.LinkSetting{{A: "cl-a", B: "cl-b", NoProbe: c.noProbe}}, c.rd, now)[0]
		a, b, da, db := probeStateOf(got)
		if a != c.wantA || b != c.wantB || da != c.diagA || db != c.diagB {
			t.Errorf("%s: states %s %s, diagnoses %q %q; want %s %s, %q %q", c.name, a, b, da, db, c.wantA, c.wantB, c.diagA, c.diagB)
		}
	}
	// the time of the last completed run is kept per direction
	got := latestPairs([]kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(old)}}, now)[0]
	if got.ProbeAToB.At == nil || got.ProbeAToB.At.Unix() != int64(fresh) || got.ProbeBToA.At == nil || got.ProbeBToA.At.Unix() != int64(old) || got.ProbeBToA.State != "failing" {
		t.Errorf("per-direction times: %+v %+v", got.ProbeAToB, got.ProbeBToA)
	}
}

// A reason label is whatever the cluster's metrics said, and an address in it is not for a viewer: only the known words go out.
func TestDiagnosisNeverCarriesAnAddressOrWhatTheStoreSaid(t *testing.T) {
	now := time.Unix(1791200000, 0)
	rd := fusionapi.LinkReadings{
		Up:           []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0), lr("cl-a", "cl-b", "reverse", 0)},
		RTT:          []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 0.01)},
		ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "dial tcp 10.2.3.4:8861: i/o timeout", 3), lrr("cl-a", "cl-b", "reverse", "auth", 1)},
	}
	got := latestPairs([]kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}, rd, now)[0]
	j, _ := json.Marshal(got)
	if strings.Contains(string(j), "10.2.3.4") || strings.Contains(string(j), "dial") {
		t.Fatalf("the store's text reached the answer: %s", j)
	}
	if got.ProbeAToB.Reason != "other" || got.ProbeAToB.Diagnosis != "denied" || got.ProbeBToA.Reason != "auth" {
		t.Errorf("%+v %+v", got.ProbeAToB, got.ProbeBToA)
	}
	known := map[string]bool{"": true}
	for _, w := range ProbeDiagnosis("").WireWords() {
		known[w] = true
	}
	for _, s := range []*LinkProbeLatest{got.ProbeAToB, got.ProbeBToA} {
		if !known[string(s.Diagnosis)] {
			t.Errorf("a diagnosis outside the bounded words: %q", s.Diagnosis)
		}
	}
	// on two reasons of equal count the same one wins in either order
	two := []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "timeout", 2), lrr("cl-a", "cl-b", "forward", "refused", 2)}
	one := linkReasons(two)[[3]string{"cl-a", "cl-b", "forward"}]
	if rev := linkReasons([]fusionapi.LinkReading{two[1], two[0]})[[3]string{"cl-a", "cl-b", "forward"}]; one != rev || one != "refused" {
		t.Errorf("tie: %q / %q", one, rev)
	}
}

func readOf(r fusionapi.LinkReadings, err error, calls *atomic.Int32) func(context.Context) (fusionapi.LinkReadings, error) {
	return func(ctx context.Context) (fusionapi.LinkReadings, error) {
		calls.Add(1)
		return r, err
	}
}

func TestACancelledFirstCallerStillCachesTheSharedRead(t *testing.T) {
	var c readingsCache
	t0 := time.Unix(1791200000, 0)
	now := func() time.Time { return t0 }
	var reads atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	good := fusionapi.LinkReadings{Loss: []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.5)}}
	read := func(rc context.Context) (fusionapi.LinkReadings, error) {
		reads.Add(1)
		cancel() // the first caller's request ends while FUSION is being read
		if rc.Err() != nil {
			return fusionapi.LinkReadings{}, rc.Err()
		}
		if _, ok := rc.Deadline(); !ok {
			t.Error("the shared read has no deadline of its own")
		}
		return good, nil
	}
	if _, _, err := c.get(ctx, now, read); err != nil {
		t.Fatalf("the read took the caller's cancellation: %v", err)
	}
	r, s, err := c.get(context.Background(), now, readOf(fusionapi.LinkReadings{}, errors.New("must not be asked"), &reads))
	if err != nil || len(r.Loss) != 1 || reads.Load() != 1 || s.Stale {
		t.Fatalf("the second caller did not get the cached read: %v %v reads=%d", r, err, reads.Load())
	}
}

func TestACallerWaitingForTheSharedReadStopsWaitingWithItsRequest(t *testing.T) {
	var c readingsCache
	now := func() time.Time { return time.Unix(1791200000, 0) }
	release, started := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.get(context.Background(), now, func(context.Context) (fusionapi.LinkReadings, error) {
			close(started)
			<-release
			return fusionapi.LinkReadings{}, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if _, _, err := c.get(ctx, now, nil); !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > 2*time.Second {
		t.Fatalf("a waiter was held past its own deadline: %v after %v", err, time.Since(t0))
	}
	close(release)
	<-done
}

func TestAFailedReadServesTheLastGoodOneMarkedStaleAndThenGivesUp(t *testing.T) {
	var c readingsCache
	t0 := time.Unix(1791200000, 0)
	now := t0
	clock := func() time.Time { return now }
	var reads atomic.Int32
	good := fusionapi.LinkReadings{Loss: []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.5)}}
	down := errors.New("store down")
	if _, s, err := c.get(context.Background(), clock, readOf(good, nil, &reads)); err != nil || s.Stale || !s.At.Equal(t0) {
		t.Fatalf("first read: %v %+v", err, s)
	}
	// the next read fails: the last good one is served, with the time it was read and the stale flag
	now = t0.Add(LatestEvery + time.Second)
	r, s, err := c.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads))
	if err != nil || len(r.Loss) != 1 || !s.Stale || !s.At.Equal(t0) || reads.Load() != 2 {
		t.Fatalf("after a failed read: %v %+v reads=%d", err, s, reads.Load())
	}
	// the failure is kept for the window, like a success: nobody asks the store again
	now = now.Add(5 * time.Second)
	if _, s, err := c.get(context.Background(), clock, readOf(good, nil, &reads)); err != nil || !s.Stale || reads.Load() != 2 {
		t.Fatalf("inside the window: %v %+v reads=%d", err, s, reads.Load())
	}
	// FUSION is back: fresh again
	now = now.Add(LatestEvery)
	if _, s, err := c.get(context.Background(), clock, readOf(good, nil, &reads)); err != nil || s.Stale || !s.At.Equal(now) {
		t.Fatalf("recovered: %v %+v", err, s)
	}
	// a store that stays down is given up on after the bound: the figures would pass for current ones
	good2 := now
	now = good2.Add(LatestEvery + time.Second)
	if _, s, err := c.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads)); err != nil || !s.Stale {
		t.Fatalf("still within the bound: %v %+v", err, s)
	}
	now = good2.Add(LatestStaleFor + time.Second)
	if r, s, err := c.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads)); !errors.Is(err, down) || s.Stale || len(r.Loss) != 0 {
		t.Fatalf("past the bound: %v %+v %v", err, s, r)
	}
	// and with no good read ever, the failure is what is served
	var fresh readingsCache
	if _, _, err := fresh.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads)); !errors.Is(err, down) {
		t.Fatalf("no good read: %v", err)
	}
}
