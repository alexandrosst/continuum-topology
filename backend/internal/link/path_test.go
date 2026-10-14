package link

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func a(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestTheRouteFingerprintIgnoresSilentHopsAndNeverContainsAnAddress(t *testing.T) {
	full := routeFingerprint([]netip.Addr{a("10.0.0.1"), a("172.16.0.1"), a("192.0.2.9")})
	gap := routeFingerprint([]netip.Addr{a("10.0.0.1"), {}, a("172.16.0.1"), {}, a("192.0.2.9")})
	if full == "" || full != gap {
		t.Fatalf("a router that stayed quiet changed the fingerprint: %q vs %q", full, gap)
	}
	if other := routeFingerprint([]netip.Addr{a("10.0.0.1"), a("172.16.0.2"), a("192.0.2.9")}); other == full {
		t.Fatal("a different route has the same fingerprint")
	}
	if strings.Contains(full, "10.0.0.1") || len(full) != 12 {
		t.Fatalf("fingerprint %q", full)
	}
	if routeFingerprint(nil) != "" || routeFingerprint([]netip.Addr{{}, {}}) != "" {
		t.Fatal("no answering hop must give no fingerprint")
	}
}

func TestARouteChangeIsCountedOnlyWhenTheNewRouteIsSeenTwiceInARow(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	changes := func() string {
		return render(m)[`continuum_link_path_changes_total{cluster="cl-me",peer="cl-a"}`]
	}
	obs := func(route string) { m.ObservePath("cl-a", PathResult{Route: route, Reached: true, Hops: 5, MTU: 1500}) }
	obs("aaa")
	if changes() != "0" {
		t.Fatalf("the first route is not a change: %s", changes())
	}
	obs("bbb") // one odd probe
	obs("aaa")
	if changes() != "0" {
		t.Fatalf("a single different probe was counted: %s", changes())
	}
	obs("bbb")
	obs("bbb") // the new route holds
	if changes() != "1" {
		t.Fatalf("a real change was not counted exactly once: %s", changes())
	}
	obs("bbb")
	obs("bbb")
	if changes() != "1" {
		t.Fatalf("a stable route kept counting: %s", changes())
	}
	obs("aaa")
	obs("aaa")
	if changes() != "2" {
		t.Fatalf("changing back is a change too: %s", changes())
	}
	m.ObservePath("cl-a", PathResult{}) // a probe that learned no route does not disturb the state
	if changes() != "2" {
		t.Fatal("an empty route counted")
	}
}

func TestOnlyATraceThatReachedTheDestinationTakesPartInRouteChanges(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	changes := func() string {
		return render(m)[`continuum_link_path_changes_total{cluster="cl-me",peer="cl-a"}`]
	}
	cut := func(route string) { m.ObservePath("cl-a", PathResult{Route: route, Reached: false}) }
	hole := func(route string) { m.ObservePath("cl-a", PathResult{Route: route, Blackhole: true}) }
	full := func(route string) { m.ObservePath("cl-a", PathResult{Route: route, Reached: true, Hops: 5, MTU: 1500}) }
	cut("short") // a trace that stopped early must not become the route the next one is compared with
	cut("short")
	full("aaa")
	cut("short")
	cut("short")
	hole("short")
	hole("short")
	full("aaa")
	if changes() != "0" {
		t.Fatalf("truncated traces were counted as route changes: %s", changes())
	}
	full("bbb")
	full("bbb")
	if changes() != "1" {
		t.Fatalf("a real change was not counted: %s", changes())
	}
}

func TestPathSeriesAreOnlyWrittenForWhatWasLearned(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	m.ObservePath("cl-a", PathResult{MTU: 1400, Hops: 7, Reached: true, Route: "abc"})
	got := render(m)
	base := `{cluster="cl-me",peer="cl-a"}`
	for k, want := range map[string]string{
		"continuum_link_path_mtu_bytes" + base:                      "1400",
		"continuum_link_path_reached" + base:                        "1",
		"continuum_link_path_mtu_blackhole" + base:                  "0",
		"continuum_link_path_last_success_timestamp_seconds" + base: "1700000000",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	// a black hole: no MTU is claimed, and no hop count when the end was never reached
	m.ObservePath("cl-a", PathResult{Blackhole: true, Route: "abc"})
	got = render(m)
	if hasKey(got, "path_mtu_bytes") {
		t.Errorf("values the latest probe did not learn are still shown: %v", got)
	}
	if got["continuum_link_path_mtu_blackhole"+base] != "1" || got["continuum_link_path_reached"+base] != "0" {
		t.Errorf("black hole not shown: %v", got)
	}
	m.PathFailed("cl-a")
	if render(m)["continuum_link_path_probe_failures_total"+base] != "1" {
		t.Error("failure not counted")
	}
}

func TestThePathIsTracedOnItsOwnScheduleToTheAddressThatAnswered(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f, "10.0.0.1", "10.0.0.2")
	var calls atomic.Int32
	var last atomic.Value
	r.Path = func(_ context.Context, dst netip.Addr) (PathResult, error) {
		calls.Add(1)
		last.Store(dst.String())
		return PathResult{MTU: 1500, Hops: 4, Reached: true, Route: "r1"}, nil
	}
	r.PathEvery = time.Hour
	r.Round(context.Background())
	r.Round(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("traced %d times in two rounds with an hour between traces", calls.Load())
	}
	if hasKey(render(r.Meter), "path_hops") {
		t.Fatal("the path probe exports a hop count of its own")
	}
	r.mu.Lock()
	good := r.good["cl-a"]
	r.mu.Unlock()
	if last.Load().(string) != good {
		t.Errorf("traced %v, but the test packets went to %v", last.Load(), good)
	}
}

func TestAPathProbeThatCannotRunIsCountedAndAnUnsupportedAddressIsSkippedQuietly(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f, "10.0.0.1")
	r.PathEvery = 10 * time.Minute
	r.Path = func(context.Context, netip.Addr) (PathResult, error) { return PathResult{}, errors.New("boom") }
	r.Round(context.Background())
	if render(r.Meter)[`continuum_link_path_probe_failures_total{cluster="cl-me",peer="cl-a"}`] != "1" {
		t.Fatal("a failed trace was not counted")
	}
	r2 := runnerFor(t, f, "10.0.0.1")
	r2.PathEvery = 10 * time.Minute
	r2.Path = func(context.Context, netip.Addr) (PathResult, error) { return PathResult{}, ErrPathUnsupported }
	r2.Round(context.Background())
	if render(r2.Meter)[`continuum_link_path_probe_failures_total{cluster="cl-me",peer="cl-a"}`] != "0" {
		t.Fatal("an unsupported address was counted as a failure")
	}
}

func TestTracingCanBeSwitchedOffAndItsIntervalIsChecked(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f, "10.0.0.1")
	var calls atomic.Int32
	r.Path = func(context.Context, netip.Addr) (PathResult, error) { calls.Add(1); return PathResult{}, nil }
	r.PathEvery = -1
	r.Round(context.Background())
	if calls.Load() != 0 {
		t.Fatal("traced although switched off")
	}
	r.PathEvery = 10 * time.Second
	if err := r.Validate(); err == nil {
		t.Fatal("tracing every 10 seconds was accepted")
	}
}

func TestEachPeerGetsAStablePortInTheEphemeralRange(t *testing.T) {
	p1, p2 := srcPortFor("cl-a"), srcPortFor("cl-b")
	if p1 != srcPortFor("cl-a") || p1 < 40000 || p1 >= 50000 || p2 < 40000 || p2 >= 50000 {
		t.Fatalf("%d %d", p1, p2)
	}
}
