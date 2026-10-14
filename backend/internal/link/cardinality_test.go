package link

import (
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// maxSeriesPerPeer is the most series one fully active peer may cost with the default settings: all probes, the path probe,
// connect timing and the throughput tests with the UDP ramp, in both directions. The figure measured today is in
// docs-site/docs/architecture/cluster-links.md ("series per peer"); this bound is generous so a metric may be added, but a
// label that follows the data (a rate, an address, a port) fails it.
const (
	maxPassiveSeriesPerPeer = 80
	maxSeriesPerPeer        = 300
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
	if testing.Verbose() {
		fam := map[string]int{}
		for k := range got {
			fam[strings.SplitN(k, "{", 2)[0]]++
		}
		t.Logf("by family: %v", fam)
	}
	t.Logf("series for one fully active peer: %d (%d of them passive, served by the agent)", nAll, nPassive)
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
