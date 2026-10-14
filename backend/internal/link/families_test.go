package link

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// MetricFamilies is what other packages check the series they name against (the FUSION dashboards do). It is held to the
// meter's own output: a meter with every source active and every state reached, rendered as the agent and the prober serve it.
// A family the meter gains or loses fails this until the list follows, and a family the fixture does not reach fails it too, so
// the list cannot go stale and the fixture cannot quietly stop covering a family.
func TestMetricFamiliesAreWhatTheMeterExports(t *testing.T) {
	fk := &fakeIperf{capacity: 100e6}
	c, _ := capFor(t, fk, func(c *Capacity) { c.UDP = UDPRamp{Enabled: true}; c.Budget = 100 << 30 })
	a := peer("cl-a", "A", "10.0.0.1", "10.0.0.2", "10.0.0.3")
	a.Throughput, a.ThroughputBudgetBytes = true, 50<<30
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	m := c.Meter
	m.EnableConnects()
	m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 2, BytesOut: 1000, BytesIn: 50, SegsOut: 100, Retransmits: 3, RttUs: 12000, JitterUs: 800, FailedAttempts: 5, FailedRefused: 1}})
	for _, d := range []Direction{Forward, Reverse} {
		m.ObserveProbe("cl-a", d, OwampResult{Sent: 100, Lost: 2, HasDelay: true, Min: 0.01, Median: 0.011, Max: 0.02, Synchronized: true, ClockError: 1e-4, HasClockError: true, Hops: 9})
	}
	m.ObservePath("cl-a", PathResult{MTU: 1500, Hops: 4, Reached: true, Route: "r"})
	m.ObserveConnect("cl-a", 3*time.Millisecond)
	settle(c)

	var out bytes.Buffer
	m.Write(&out)
	types := map[string]string{}
	samples := map[string]int{}
	for _, l := range strings.Split(out.String(), "\n") {
		switch {
		case strings.HasPrefix(l, "# TYPE "):
			fs := strings.Fields(l)
			types[fs[2]] = fs[3]
		case l != "" && !strings.HasPrefix(l, "#"):
			samples[strings.SplitN(l, "{", 2)[0]]++
		}
	}
	var want []MetricFamily
	for n, typ := range types {
		want = append(want, MetricFamily{n, typ})
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
	if got := MetricFamilies(); !reflect.DeepEqual(got, want) {
		t.Errorf("MetricFamilies() differs from what the meter renders\n got: %v\nwant: %v", got, want)
	}
	for _, fam := range MetricFamilies() {
		series := []string{fam.Name}
		if fam.Type == "histogram" {
			series = []string{fam.Name + "_bucket", fam.Name + "_sum", fam.Name + "_count"}
		}
		for _, s := range series {
			if samples[s] == 0 {
				t.Errorf("the fixture renders no sample of %s, so the list is not checked against a real one", s)
			}
		}
	}
}
