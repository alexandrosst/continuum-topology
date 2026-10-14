package link

import (
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// The most series one peer may cost with the default settings. A fully active peer is the pair's first cluster, which runs
// everything: all probes, the path probe, connect timing and the throughput tests with the UDP ramp, in both directions. The
// other cluster of the pair only exports its passive series and the route trace. The figures measured today are in
// docs-site/docs/architecture/cluster-links.md ("series per peer"); the bounds sit a few series above them, so a label that
// follows the data (a rate, an address, a port) or a metric added without a thought fails, and the docs get updated with it.
const (
	maxPassiveSeriesPerPeer = 32
	maxSeriesPerPeer        = 145
	maxServingSeriesPerPeer = 40
)

func seriesLines(m *Meter) int {
	n := 0
	for k := range render(m) {
		if strings.HasPrefix(k, "continuum_link_") {
			n++
		}
	}
	return n
}

func TestOnePeerCostsABoundedNumberOfSeriesWhateverTheRatesAndAddresses(t *testing.T) {
	// The passive series alone (what the agent serves), with traffic that fills every histogram.
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	passive := NewMeter(p)
	passive.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 2, BytesOut: 1000, BytesIn: 50, SegsOut: 100, Retransmits: 3, RttUs: 12000, JitterUs: 800, FailedAttempts: 5, FailedRefused: 1, FailedTimeout: 1, FailedReset: 1, FailedUnreachable: 1}})
	nPassive := seriesLines(passive)

	// Everything the prober adds, for one peer with a throughput budget of its own.
	f := &fakeIperf{capacity: 100e6}
	c, _ := capFor(t, f, func(c *Capacity) { c.UDP = UDPRamp{Enabled: true}; c.Budget = 100 << 30 })
	a := peer("cl-a", "A", "10.0.0.1", "10.0.0.2", "10.0.0.3")
	a.Throughput, a.ThroughputBudgetBytes = true, 50<<30
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	m := c.Meter
	m.EnableConnects()
	m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 2, BytesOut: 1000, BytesIn: 50, SegsOut: 100, Retransmits: 3, RttUs: 12000, JitterUs: 800, FailedAttempts: 5, FailedRefused: 1, FailedTimeout: 1, FailedReset: 1, FailedUnreachable: 1}})
	for _, d := range []Direction{Forward, Reverse} {
		m.ObserveProbe("cl-a", d, OwampResult{Sent: 100, Lost: 2, HasDelay: true, Min: 0.01, Median: 0.011, Max: 0.02, Synchronized: true, ClockError: 1e-4, HasClockError: true, Hops: 9})
		for _, r := range probeFailReasons {
			m.ProbeFailed("cl-a", d, r)
		}
		m.ObserveProbe("cl-a", d, OwampResult{Sent: 100, Lost: 2, HasDelay: true, Min: 0.01, Median: 0.011, Max: 0.02, Synchronized: true, ClockError: 1e-4, HasClockError: true, Hops: 9})
	}
	m.ObservePath("cl-a", PathResult{MTU: 1500, Hops: 4, Reached: true, Route: "r"})
	m.PathFailed("cl-a")
	m.ObserveConnect("cl-a", 3*time.Millisecond)
	for _, r := range failReasons {
		m.ConnectFailed("cl-a", r)
	}
	for _, r := range tpFailReasons {
		m.ThroughputFailed("cl-a", r)
	}
	for _, r := range tpSkipReasons {
		m.ThroughputSkipped("cl-a", r)
	}
	settle(c)
	got := render(m)
	if _, ok := got[`continuum_link_throughput_udp_point_bits_per_second{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0",point="first_loss"}`]; !ok {
		t.Fatal("the fixture did not run the UDP ramp to its first loss; the count below would be short")
	}
	nAll := seriesLines(m)
	nThroughput := 0
	for k := range got {
		if strings.HasPrefix(k, "continuum_link_throughput_") {
			nThroughput++
		}
	}

	// The pair's other cluster: the same traffic and route trace, but the probe and the connect timing are measured by the
	// first one, so it exports neither (nor throughput series, which only the first cluster has).
	sp := NewPeers()
	skip := peer("cl-a", "A", "10.0.0.1")
	skip.SkipProbe = true
	sp.Set("cl-me", []*continuumv1.LinkPeer{skip})
	sm := NewMeter(sp)
	sm.EnableProbes()
	sm.EnableConnects()
	sm.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 2, BytesOut: 1000, BytesIn: 50, SegsOut: 100, Retransmits: 3, RttUs: 12000, FailedAttempts: 5, FailedRefused: 1, FailedTimeout: 1, FailedReset: 1, FailedUnreachable: 1}})
	for _, d := range []Direction{Forward, Reverse} { // what an older prober, or a stray result, would record
		sm.ObserveProbe("cl-a", d, OwampResult{Sent: 100})
		sm.ProbeFailed("cl-a", d, "timeout")
	}
	sm.ObservePath("cl-a", PathResult{MTU: 1500, Hops: 4, Reached: true, Route: "r"})
	sm.PathFailed("cl-a")
	sm.ObserveConnect("cl-a", 3*time.Millisecond)
	sm.ConnectFailed("cl-a", "timeout")
	nServing := seriesLines(sm)
	for k := range render(sm) {
		if strings.Contains(k, "continuum_link_probe_") || strings.Contains(k, "tcp_connect") || strings.Contains(k, "continuum_link_throughput_") {
			t.Errorf("the cluster that only serves exports %s", k)
		}
	}
	if testing.Verbose() {
		fam := map[string]int{}
		for k := range got {
			fam[strings.SplitN(k, "{", 2)[0]]++
		}
		t.Logf("by family: %v", fam)
	}
	t.Logf("series for one fully active peer: %d (%d of them passive, served by the agent; %d of throughput); the same peer on the serving side: %d; one pair: %d, against %d when both clusters probed", nAll, nPassive, nThroughput, nServing, nAll+nServing, 2*nAll-nThroughput)
	if nServing > maxServingSeriesPerPeer {
		t.Errorf("the serving side of a pair costs %d series, more than %d", nServing, maxServingSeriesPerPeer)
	}
	if nPassive > maxPassiveSeriesPerPeer {
		t.Errorf("the passive series of one peer number %d, more than %d", nPassive, maxPassiveSeriesPerPeer)
	}
	if nAll > maxSeriesPerPeer {
		t.Errorf("one fully active peer costs %d series, more than %d: a label follows the data", nAll, maxSeriesPerPeer)
	}
	// Nothing in a series' labels may follow the data: no rate, address or port.
	for k := range got {
		for _, bad := range []string{"rate_bits_per_second", "10.0.0.", "addr", "port="} {
			if strings.Contains(k, bad) {
				t.Errorf("a data-dependent label in %s", k)
			}
		}
	}
}
