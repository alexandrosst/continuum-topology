package link

import (
	"context"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// A peer list that is empty for a moment (the agent answers with one around a restart, or while its flow collector is paused)
// must not make the prober forget the schedule, the bytes already charged to a pair, or the counters of a peer.
func TestABlipInThePeerListKeepsTheScheduleAndTheBudgetLedger(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 })
	list := func() []*continuumv1.LinkPeer {
		lp := peer("cl-a", "A", "10.0.0.1")
		lp.Throughput, lp.ThroughputBudgetBytes = true, 7<<30
		return []*continuumv1.LinkPeer{lp}
	}
	c.Meter.Peers.Set("cl-me", list())
	settle(c)
	if f.count() != 1 {
		t.Fatalf("the first cycle ran %d tests, want 1", f.count())
	}
	c.mu.Lock()
	st, next := c.peers["cl-a"], c.peers["cl-a"].next
	c.mu.Unlock()
	used := c.pairs["cl-a"].Used(ck.now())
	if used == 0 || next.Before(ck.now().Add(time.Hour)) {
		t.Fatalf("the cycle left no ledger (%d) or no distant next start (%s)", used, next)
	}

	c.Meter.Peers.Set("cl-me", nil)
	c.Meter.Forget()
	c.step(context.Background())
	ck.add(10 * time.Second) // the prober polls once a minute: the empty list can be seen more than once
	c.Meter.Forget()
	c.step(context.Background())
	c.Meter.Peers.Set("cl-me", list())
	ck.add(time.Minute)
	settle(c)

	c.mu.Lock()
	same, nextNow := c.peers["cl-a"] == st, c.peers["cl-a"].next
	c.mu.Unlock()
	if !same || !nextNow.Equal(next) {
		t.Errorf("the schedule was reset: same state %v, next %s -> %s", same, next, nextNow)
	}
	if got := c.pairs["cl-a"].Used(ck.now()); got != used {
		t.Errorf("the pair ledger went from %d to %d bytes", used, got)
	}
	if f.count() != 1 {
		t.Errorf("%d tests ran, want the one of the first cycle: the blip started another", f.count())
	}
}

// A list that stays empty is still honoured: after the grace everything kept for the peer goes.
func TestAnEmptyPeerListStillWipesTheStateAfterTheGrace(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 })
	lp := peer("cl-a", "A", "10.0.0.1")
	lp.Throughput, lp.ThroughputBudgetBytes = true, 7<<30
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{lp})
	settle(c)

	c.Meter.Peers.Set("cl-me", nil)
	c.step(context.Background())
	ck.add(peerGrace - time.Minute)
	c.step(context.Background())
	c.mu.Lock()
	kept := len(c.peers) == 1 && len(c.pairs) == 1 && len(c.lastBytes) > 0
	c.mu.Unlock()
	if !kept {
		t.Fatal("state was dropped inside the grace")
	}
	ck.add(2 * time.Minute)
	c.step(context.Background())
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.peers) != 0 || len(c.pairs) != 0 || len(c.lastBytes) != 0 || len(c.lastBits) != 0 || len(c.absent) != 0 {
		t.Errorf("left after the grace: %v %v %v %v %v", c.peers, c.pairs, c.lastBytes, c.lastBits, c.absent)
	}
}

// The meter's side: probe and connect state, and the counters, survive the blip and go after the grace.
func TestABlipKeepsTheMetersProbeAndConnectState(t *testing.T) {
	m, ck := clockedMeter(peer("cl-a", "A", "10.0.0.1"))
	m.EnableProbes()
	m.EnableConnects()
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 100, Lost: 2})
	m.ProbeFailed("cl-a", Reverse, "timeout")
	m.ObserveConnect("cl-a", 2*time.Millisecond)
	m.Observe([]*continuumv1.RawFlow{flow("10.0.0.1", 4, 1000)})
	before := render(m)

	m.Peers.Set("cl-me", nil)
	m.Forget()
	ck.add(10 * time.Second)
	m.Forget()
	m.Peers.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	m.Forget() // the peer is back: the clock for its absence starts again
	after := render(m)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s: %s -> %s", k, v, after[k])
		}
	}
	if len(after) != len(before) {
		t.Errorf("%d series, was %d", len(after), len(before))
	}

	// Gone for good: dropped after the grace, but only once the grace has passed since the absence began, and a return in
	// between starts it over.
	m.Peers.Set("cl-me", nil)
	m.Forget()
	ck.add(peerGrace - time.Minute)
	m.Peers.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	m.Forget()
	m.Peers.Set("cl-me", nil)
	ck.add(2 * time.Minute)
	m.Forget() // absent again for two minutes only
	m.mu.Lock()
	n := len(m.probes) + len(m.connects) + len(m.per)
	m.mu.Unlock()
	if n == 0 {
		t.Fatal("a return inside the grace did not restart it")
	}
	ck.add(peerGrace)
	m.Forget()
	m.mu.Lock()
	n = len(m.probes) + len(m.connects) + len(m.per) + len(m.absent)
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("%d records survived the grace", n)
	}
}
