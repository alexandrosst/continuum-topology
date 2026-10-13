package link

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// fakeIperf stands in for the iperf3 client. Its answers are the real outputs of iperf3 3.16 (testdata) for TCP, and
// documents built to the same shape for UDP, whose loss depends on the rate the test asked for.
type fakeIperf struct {
	mu    sync.Mutex
	calls [][]string
	envs  [][]string
	// capacity is the UDP rate above which datagrams are lost; busy and fail make the answer an iperf3 error for an address.
	capacity float64
	busy     map[string]bool
	fail     map[string]string
	running  atomic.Int32
	overlap  atomic.Int32
	hold     time.Duration
	// failArg makes the answer an iperf3 error (a fixture) for the invocations that carry the argument; sendMax is the most
	// a UDP sender manages to send, whatever it was asked for.
	failArg map[string]string
	sendMax float64
}

func (f *fakeIperf) exec(_ context.Context, env []string, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	f.mu.Unlock()
	if f.running.Add(1) > 1 {
		f.overlap.Add(1)
	}
	defer f.running.Add(-1)
	if f.hold > 0 {
		time.Sleep(f.hold)
	}
	addr := args[1]
	has := func(a string) bool {
		for _, x := range args {
			if x == a {
				return true
			}
		}
		return false
	}
	val := func(flag string) string {
		for i, x := range args {
			if x == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	if f.busy[addr] {
		return fixtureBytes("busy"), nil, errors.New("exit status 1")
	}
	if why, ok := f.fail[addr]; ok {
		return fixtureBytes(why), nil, errors.New("exit status 1")
	}
	for arg, why := range f.failArg {
		if has(arg) {
			return fixtureBytes(why), nil, errors.New("exit status 1")
		}
	}
	switch {
	case has("-u"):
		rate, _ := strconv.ParseFloat(val("-b"), 64)
		if f.sendMax > 0 {
			rate = min(rate, f.sendMax)
		}
		return udpDoc(rate, f.capacity), nil, nil
	case has("--bidir"):
		return fixtureBytes("bidir"), nil, nil
	case has("-P"):
		return fixtureBytes("tcp4"), nil, nil
	case has("-R"):
		return fixtureBytes("tcp1r"), nil, nil
	}
	return fixtureBytes("tcp1"), nil, nil
}

func fixtureBytes(name string) []byte {
	b, err := os.ReadFile("testdata/iperf3_" + name + ".json")
	if err != nil {
		panic(err)
	}
	return b
}

// udpDoc is what iperf3 prints for a 5-second UDP test at rate when the link carries capacity: what exceeds it is lost.
func udpDoc(rate, capacity float64) []byte {
	const secs = 5
	packets := rate * secs / 8 / 1348
	lost := 0.0
	if capacity > 0 && rate > capacity {
		lost = packets * (1 - capacity/rate)
	}
	sum := func(sender bool) string {
		return fmt.Sprintf(`{"bytes": %d, "bits_per_second": %f, "jitter_ms": 0.05, "lost_packets": %d, "packets": %d, "sender": %t}`,
			int64(rate*secs/8), rate-lost*1348*8/secs, int64(lost), int64(packets), sender)
	}
	return []byte(fmt.Sprintf(`{"start": {"test_start": {"protocol": "UDP", "num_streams": 1}}, "end": {"sum_sent": %s, "sum_received": %s}}`, sum(true), sum(false)))
}

func (f *fakeIperf) argsOf(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls[i], " ")
}

func (f *fakeIperf) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func capFor(t *testing.T, f *fakeIperf, mut func(*Capacity), addrs ...string) (*Capacity, *clock) {
	t.Helper()
	if len(addrs) == 0 {
		addrs = []string{"10.0.0.1"}
	}
	p := NewPeers()
	lp := peer("cl-a", "A", addrs...)
	lp.Throughput = true
	p.Set("cl-me", []*continuumv1.LinkPeer{lp})
	m := NewMeter(p)
	m.EnableProbes()
	ck := &clock{t: time.Unix(1_700_000_000, 0)}
	m.now = ck.now
	c := &Capacity{Meter: m, Exec: f.exec, Now: ck.now, StartAfter: -1, Pause: time.Millisecond, Reverse: true, Parallel: 4, Omit: 2 * time.Second}
	if mut != nil {
		mut(c)
	}
	c.Defaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Meter.SetBudget(func() (uint64, uint64) { return c.ledger.Used(c.Now()), c.ledger.Limit })
	return c, ck
}

// settle runs the scheduler until it has nothing due.
func settle(c *Capacity) {
	for i := 0; i < 200; i++ {
		if c.step(context.Background()) == 5*time.Second {
			return
		}
	}
	panic("scheduler never went idle")
}

func TestThePlanCoversBothDirectionsStreamCountsAndTheRamp(t *testing.T) {
	c, _ := capFor(t, &fakeIperf{}, func(c *Capacity) { c.UDP = UDPRamp{Enabled: true}; c.DSCP = []int{0, 46}; c.Bidir = true })
	var got []string
	for _, x := range c.plan() {
		got = append(got, fmt.Sprintf("%s/%s/%d", x.kind, x.dir, x.dscp))
	}
	want := "tcp/forward/0 tcp/reverse/0 tcp/forward/46 tcp/reverse/46 tcp-parallel/forward/0 tcp-parallel/reverse/0 tcp-bidir/forward/0 udp-ramp/forward/0 udp-ramp/reverse/0 udp-ramp/forward/46 udp-ramp/reverse/46"
	if strings.Join(got, " ") != want {
		t.Errorf("plan:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	c2, _ := capFor(t, &fakeIperf{}, func(c *Capacity) { c.Reverse = false; c.Parallel = 0 })
	if p := c2.plan(); len(p) != 1 || p[0].kind != "tcp" || p[0].dir != Forward {
		t.Errorf("minimal plan = %+v", p)
	}
}

func TestTheSettingsAreChecked(t *testing.T) {
	bad := map[string]func(*Capacity){
		"too often":        func(c *Capacity) { c.Every = time.Minute },
		"short test":       func(c *Capacity) { c.Duration = time.Second },
		"fractional":       func(c *Capacity) { c.Duration = 10500 * time.Millisecond },
		"omit too long":    func(c *Capacity) { c.Omit = 10 * time.Second; c.Duration = 10 * time.Second },
		"one stream":       func(c *Capacity) { c.Parallel = 1 },
		"too many streams": func(c *Capacity) { c.Parallel = 64 },
		"bad port":         func(c *Capacity) { c.Port = 70000 },
		"ramp too low": func(c *Capacity) {
			c.UDP = UDPRamp{Enabled: true, Start: 1000, Max: 1e9, Step: 5 * time.Second, Loss: 0.01}
		},
		"ramp inverted": func(c *Capacity) {
			c.UDP = UDPRamp{Enabled: true, Start: 1e9, Max: 1e6, Step: 5 * time.Second, Loss: 0.01}
		},
		"ramp too steep": func(c *Capacity) {
			c.UDP = UDPRamp{Enabled: true, Start: 1e6, Max: 1e12, Step: 5 * time.Second, Loss: 0.01}
		},
		"loss over half": func(c *Capacity) {
			c.UDP = UDPRamp{Enabled: true, Start: 1e6, Max: 1e8, Step: 5 * time.Second, Loss: 0.9}
		},
		"dscp range":    func(c *Capacity) { c.DSCP = []int{64} },
		"dscp repeated": func(c *Capacity) { c.DSCP = []int{8, 8} },
		"busy fraction": func(c *Capacity) { c.BusyFraction = 2 },
		"half auth":     func(c *Capacity) { c.User = "u" },
	}
	for name, mut := range bad {
		c := &Capacity{Reverse: true}
		mut(c)
		c.Defaults()
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	ok := &Capacity{UDP: UDPRamp{Enabled: true}, User: "u", KeyFile: "k", PasswordFile: "p"}
	ok.Defaults()
	if err := ok.Validate(); err != nil {
		t.Errorf("defaults refused: %v", err)
	}
}

func TestACycleRunsEveryTestOnceAndExportsWhatItFound(t *testing.T) {
	f := &fakeIperf{capacity: 80e6}
	c, _ := capFor(t, f, func(c *Capacity) { c.UDP = UDPRamp{Enabled: true}; c.Bidir = true })
	settle(c)
	// tcp x2, parallel x2, bidir, ramps: forward 5,10,20,40,80,160 (loss at 160) x2.
	if got, want := f.count(), 2+2+1+6+6; got != want {
		t.Fatalf("%d runs, want %d:\n%v", got, want, f.calls)
	}
	// What the commands were.
	first := f.argsOf(0)
	for _, w := range []string{"iperf3 -c 10.0.0.1 -p 8862 -J -i 0 --connect-timeout 5000", "-t 10", "-O 2"} {
		if !strings.Contains(first, w) {
			t.Errorf("first run %q lacks %q", first, w)
		}
	}
	if strings.Contains(first, " -R") || !strings.Contains(f.argsOf(1), " -R") {
		t.Errorf("the second test, and only it, is the reverse one: %q / %q", first, f.argsOf(1))
	}
	if !strings.Contains(f.argsOf(2), "-P 4") || !strings.Contains(f.argsOf(4), "--bidir") {
		t.Errorf("parallel / bidir not requested: %q / %q", f.argsOf(2), f.argsOf(4))
	}
	got := render(c.Meter)
	base := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="`
	for key, want := range map[string]string{
		"continuum_link_throughput_bits_per_second" + base + `forward",protocol="tcp",streams="1",dscp="0"}`:     "1.3847713674505e+10",
		"continuum_link_throughput_retransmits" + base + `forward",protocol="tcp",streams="1",dscp="0"}`:         "36346",
		"continuum_link_throughput_retransmits" + base + `reverse",protocol="tcp",streams="1",dscp="0"}`:         "26052",
		"continuum_link_throughput_bits_per_second" + base + `reverse",protocol="tcp",streams="1",dscp="0"}`:     "1.4543531405561e+10",
		"continuum_link_throughput_udp_clean_bits_per_second" + base + `forward",dscp="0"}`:                      "8e+07",
		"continuum_link_throughput_udp_saturated" + base + `forward",dscp="0"}`:                                  "1",
		"continuum_link_throughput_udp_loss_ratio" + base + `forward",dscp="0",rate_bits_per_second="80000000"}`: "0",
	} {
		if g := got[key]; !sameNumber(g, want) {
			t.Errorf("%s = %q, want %q", key, g, want)
		}
	}
	bi := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="reverse",protocol="tcp_bidir",streams="1",dscp="0"}`
	if got["continuum_link_throughput_retransmits"+bi] != "96" {
		t.Errorf("the bidirectional test's own series: %q", got["continuum_link_throughput_retransmits"+bi])
	}
	if l := got["continuum_link_throughput_udp_loss_ratio"+base+`forward",dscp="0",rate_bits_per_second="160000000"}`]; l == "" || l == "0" {
		t.Errorf("the rung over the capacity shows no loss: %q", l)
	}
	if got["continuum_link_throughput_test_bytes_total"+base+`forward"}`] == "0" || got[`continuum_link_throughput_last_success_timestamp_seconds{cluster="cl-me",peer="cl-a",method="iperf3"}`] == "" {
		t.Error("bytes carried / last success not recorded")
	}
	for _, r := range tpFailReasons {
		if got[`continuum_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="`+r+`"}`] != "0" {
			t.Errorf("failure %s counted in a clean cycle", r)
		}
	}
	// Nothing more is due until the next cycle.
	n := f.count()
	settle(c)
	if f.count() != n {
		t.Error("the cycle repeated at once")
	}
}

func sameNumber(a, b string) bool {
	x, e1 := strconv.ParseFloat(a, 64)
	y, e2 := strconv.ParseFloat(b, 64)
	return e1 == nil && e2 == nil && (x == y || (y != 0 && (x-y)/y < 1e-9 && (y-x)/y < 1e-9))
}

func TestTheRampStopsAtTheTopWithoutSayingSaturated(t *testing.T) {
	f := &fakeIperf{capacity: 0} // never loses
	c, _ := capFor(t, f, func(c *Capacity) {
		c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 40e6, Step: 5 * time.Second, Loss: 0.01}
		c.Reverse, c.Parallel = false, 0
	})
	settle(c)
	// tcp + ramp 10, 20, 40
	if f.count() != 4 {
		t.Fatalf("%d runs: %v", f.count(), f.calls)
	}
	got := render(c.Meter)
	base := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`
	if got["continuum_link_throughput_udp_saturated"+base] != "0" || !sameNumber(got["continuum_link_throughput_udp_clean_bits_per_second"+base], "4e7") {
		t.Errorf("saturated %q clean %q", got["continuum_link_throughput_udp_saturated"+base], got["continuum_link_throughput_udp_clean_bits_per_second"+base])
	}
}

func TestARampThatLosesFromTheFirstRungHasNoCleanRate(t *testing.T) {
	f := &fakeIperf{capacity: 1e6}
	c, _ := capFor(t, f, func(c *Capacity) {
		c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 40e6, Step: 5 * time.Second, Loss: 0.01}
		c.Reverse, c.Parallel = false, 0
	})
	settle(c)
	got := render(c.Meter)
	base := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`
	if _, ok := got["continuum_link_throughput_udp_clean_bits_per_second"+base]; ok {
		t.Error("a clean rate is reported although every rung lost")
	}
	if got["continuum_link_throughput_udp_saturated"+base] != "1" {
		t.Error("not saturated")
	}
}

func TestANewRampReplacesTheOldRungsAndSeriesAreForgottenWithThePeer(t *testing.T) {
	f := &fakeIperf{capacity: 1e12}
	c, ck := capFor(t, f, func(c *Capacity) {
		c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 40e6, Step: 5 * time.Second, Loss: 0.01}
		c.Reverse, c.Parallel = false, 0
	})
	settle(c)
	c.UDP.Max = 20e6
	ck.add(14 * time.Hour)
	settle(c)
	got := render(c.Meter)
	for k := range got {
		if strings.Contains(k, `rate_bits_per_second="40000000"`) {
			t.Errorf("an old rung survived: %s", k)
		}
	}
	c.Meter.Peers.Set("cl-me", nil)
	c.Meter.Forget()
	for k := range render(c.Meter) {
		if strings.Contains(k, "throughput") && strings.Contains(k, `peer="cl-a"`) {
			t.Errorf("series of a removed peer remain: %s", k)
		}
	}
}

func TestTheBudgetHoldsTestsBackAndTheWindowSlides(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) { c.Budget = 7 << 30; c.Reverse, c.Parallel = true, 0 })
	settle(c)
	// Each fixture test carries ~5.2-5.5 GB (+ omitted start): the first fits, the second does not.
	if f.count() != 1 {
		t.Fatalf("%d runs under a budget for one", f.count())
	}
	got := render(c.Meter)
	if got[`continuum_link_throughput_tests_skipped_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="budget"}`] != "1" {
		t.Errorf("skip not counted: %v", got)
	}
	if used, limit := c.ledger.Used(ck.now()), c.ledger.Limit; used < 5<<30 || used > limit || got[`continuum_link_throughput_budget_bytes{cluster="cl-me",method="iperf3"}`] != strconv.Itoa(7<<30) {
		t.Errorf("used %d of %d", used, limit)
	}
	ck.add(31 * time.Minute)
	settle(c)
	if f.count() != 1 {
		t.Error("ran again although the budget was still used up")
	}
	ck.add(24 * time.Hour)
	settle(c)
	if f.count() != 2 {
		t.Errorf("did not resume when the window slid: %d runs", f.count())
	}
}

func TestATestLargerThanTheWholeBudgetWarnsOnceAndNeverRuns(t *testing.T) {
	var logs bytes.Buffer
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) {
		c.Budget = 1 << 20
		c.Reverse, c.Parallel = false, 0
		c.Log = newTestLog(&logs)
	})
	for i := 0; i < 3; i++ {
		settle(c)
		ck.add(time.Hour)
	}
	if f.count() != 0 {
		t.Fatal("ran a test that cannot fit the budget")
	}
	if n := strings.Count(logs.String(), "never run"); n != 1 {
		t.Errorf("warned %d times:\n%s", n, logs.String())
	}
}

func TestAMeasuredTestIsCappedByTheRateLimitInItsEstimate(t *testing.T) {
	c, _ := capFor(t, &fakeIperf{}, func(c *Capacity) { c.TCPMaxRate = 100e6 })
	p := Peer{ID: "x"}
	if got, want := c.estimate(p, capTest{"tcp", Forward, 0}), uint64(100e6*12/8); got != want {
		t.Errorf("single = %d want %d", got, want)
	}
	if got, want := c.estimate(p, capTest{"tcp-parallel", Forward, 0}), uint64(4*100e6*12/8); got != want {
		t.Errorf("parallel = %d want %d", got, want)
	}
	if got, want := c.estimate(p, capTest{"tcp-bidir", Forward, 0}), uint64(2*100e6*12/8); got != want {
		t.Errorf("bidir = %d want %d", got, want)
	}
	c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 40e6, Step: 5 * time.Second, Loss: 0.01}
	if got, want := c.estimate(p, capTest{"udp-ramp", Forward, 0}), uint64((10e6+20e6+40e6)*5/8); got != want {
		t.Errorf("ramp = %d want %d", got, want)
	}
	c.TCPMaxRate = 0
	if got, want := c.estimate(p, capTest{"tcp", Forward, 0}), uint64(1e9*12/8); got != want {
		t.Errorf("unknown, uncapped = %d want %d", got, want)
	}
}

func TestTestsWaitWhileTheLinkIsBusyAndThenRun(t *testing.T) {
	f := &fakeIperf{}
	load := &Load{}
	c, ck := capFor(t, f, func(c *Capacity) {
		c.Load, c.BusyFraction, c.SkipAbove = load, 0.3, 500e6
		c.Reverse, c.Parallel = false, 0
	})
	feed := func(sent uint64) {
		ck.add(time.Minute)
		load.Observe(PeersDoc{Peers: []PeerDoc{{ID: "cl-a", BytesSent: sent}}}, ck.now())
	}
	feed(0)
	feed(60 * 1e9 / 8) // 1 Gbit/s over the minute
	settle(c)
	if f.count() != 0 {
		t.Fatal("started a test on a link carrying 1 Gbit/s")
	}
	if render(c.Meter)[`continuum_link_throughput_tests_skipped_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="link_busy"}`] != "1" {
		t.Error("the wait was not counted")
	}
	feed(60*1e9/8 + 1000) // quiet again
	ck.add(11 * time.Minute)
	feed(60*1e9/8 + 2000)
	settle(c)
	if f.count() != 1 {
		t.Errorf("did not run once the link was quiet: %d", f.count())
	}
	// Having measured the link, the relative rule applies: traffic above 30% of what it reached holds the next cycle back.
	if c.reached("cl-a", Forward) < 1e10 {
		t.Fatalf("reached = %v", c.reached("cl-a", Forward))
	}
	c.SkipAbove = 0
	ck.add(14 * time.Hour)
	base := uint64(60*1e9/8 + 2000)
	feed(base)
	feed(base + uint64(60*5e9/8)) // 5 Gbit/s: over 30% of 13.8 Gbit/s
	n := f.count()
	settle(c)
	if f.count() != n {
		t.Error("the relative rule did not hold the test back")
	}
}

func TestACounterThatGoesBackwardsGivesNoRate(t *testing.T) {
	l := &Load{}
	now := time.Unix(1_700_000_000, 0)
	l.Observe(PeersDoc{Peers: []PeerDoc{{ID: "a", BytesSent: 1000}}}, now)
	l.Observe(PeersDoc{Peers: []PeerDoc{{ID: "a", BytesSent: 10}}}, now.Add(time.Minute))
	if _, ok := l.Rate("a", now.Add(time.Minute), time.Hour); ok {
		t.Error("a rate from a restarted counter")
	}
	l.Observe(PeersDoc{Peers: []PeerDoc{{ID: "a", BytesSent: 7510}}}, now.Add(2*time.Minute))
	r, ok := l.Rate("a", now.Add(2*time.Minute), time.Hour)
	if !ok || r.Sent != 1000 {
		t.Errorf("%+v %v", r, ok)
	}
	if _, ok := l.Rate("a", now.Add(10*time.Minute), time.Minute); ok {
		t.Error("a stale rate was used")
	}
}

func TestABusyServerMovesOnToAnotherNodeAndOnlyWhenAllAreBusyTheTestWaits(t *testing.T) {
	f := &fakeIperf{busy: map[string]bool{"10.0.0.1": true, "10.0.0.2": false}}
	c, _ := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 }, "10.0.0.1", "10.0.0.2")
	settle(c)
	if render(c.Meter)[`continuum_link_throughput_bits_per_second{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",protocol="tcp",streams="1",dscp="0"}`] == "" {
		t.Fatal("the free node was not used")
	}
	f2 := &fakeIperf{busy: map[string]bool{"10.0.0.1": true, "10.0.0.2": true}}
	c2, _ := capFor(t, f2, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 }, "10.0.0.1", "10.0.0.2")
	settle(c2)
	got := render(c2.Meter)
	if got[`continuum_link_throughput_tests_skipped_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="server_busy"}`] != "1" {
		t.Errorf("not counted as a wait: %v", got)
	}
	for _, r := range tpFailReasons {
		if got[`continuum_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="`+r+`"}`] != "0" {
			t.Errorf("a busy server was counted as a failure (%s)", r)
		}
	}
}

func TestAnUnreachablePeerIsOneFailureAndTheCycleIsDropped(t *testing.T) {
	f := &fakeIperf{fail: map[string]string{"10.0.0.1": "refused", "10.0.0.2": "refused"}}
	c, ck := capFor(t, f, nil, "10.0.0.1", "10.0.0.2")
	settle(c)
	got := render(c.Meter)
	if got[`continuum_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="refused"}`] != "1" {
		t.Errorf("failures: %v", got)
	}
	// One address after the other for the one test, then nothing else is tried this cycle.
	if f.count() != 2 {
		t.Errorf("%d runs", f.count())
	}
	ck.add(31 * time.Minute)
	settle(c)
	if f.count() != 4 {
		t.Errorf("did not try again after the retry delay: %d", f.count())
	}
}

func TestNothingRunsOutsideTheTimeWindow(t *testing.T) {
	f := &fakeIperf{}
	w, err := ParseTimeWindow("02:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	c, ck := capFor(t, f, func(c *Capacity) { c.Window = w; c.Reverse, c.Parallel = false, 0 })
	ck.t = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if d := c.step(context.Background()); f.count() != 0 || d != time.Minute {
		t.Fatalf("ran at noon (%d runs, wait %v)", f.count(), d)
	}
	ck.t = time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	settle(c)
	if f.count() != 1 {
		t.Errorf("did not run inside the window: %d", f.count())
	}
}

func TestTimeWindowsCrossMidnightAndAreChecked(t *testing.T) {
	w, _ := ParseTimeWindow("22:00-04:00")
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		h, m int
		in   bool
	}{{23, 0, true}, {1, 30, true}, {3, 59, true}, {4, 0, false}, {12, 0, false}, {21, 59, false}, {22, 0, true}} {
		if w.Contains(at(c.h, c.m)) != c.in {
			t.Errorf("%02d:%02d in = %v, want %v", c.h, c.m, !c.in, c.in)
		}
	}
	if d := w.Opens(at(12, 0)); d != 10*time.Hour {
		t.Errorf("opens in %v", d)
	}
	if d := w.Opens(at(23, 0)); d != 0 {
		t.Errorf("open window reports %v", d)
	}
	if all, _ := ParseTimeWindow(""); !all.Contains(at(5, 5)) {
		t.Error("an empty window must be always")
	}
	for _, bad := range []string{"22:00", "25:00-04:00", "22:00-22:00", "2:00-4:00", "a-b", "22:00-04:60"} {
		if _, err := ParseTimeWindow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestThePasswordGoesInTheEnvironmentNeverTheCommandLine(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("s3cret-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeIperf{}
	c, _ := capFor(t, f, func(c *Capacity) {
		c.User, c.KeyFile, c.PasswordFile = "link", "/keys/public.pem", pw
		c.Reverse, c.Parallel = false, 0
	})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	settle(c)
	args := f.argsOf(0)
	if !strings.Contains(args, "--username link --rsa-public-key-path /keys/public.pem") {
		t.Errorf("credentials missing: %s", args)
	}
	if strings.Contains(args, "s3cret-pass") {
		t.Error("the password is on the command line")
	}
	if len(f.envs[0]) != 1 || f.envs[0][0] != "IPERF3_PASSWORD=s3cret-pass" {
		t.Errorf("env = %v", f.envs[0])
	}
	empty := filepath.Join(dir, "empty")
	_ = os.WriteFile(empty, nil, 0o600)
	c.PasswordFile = empty
	if c.Start() == nil {
		t.Error("an empty password file was accepted")
	}
}

func TestAThroughputTestNeverOverlapsAnOneWayProbe(t *testing.T) {
	gate := NewGate()
	fi := &fakeIperf{hold: 20 * time.Millisecond}
	c, _ := capFor(t, fi, func(c *Capacity) { c.Gate = gate; c.Reverse, c.Parallel = false, 0 })
	var inOwamp, clash atomic.Int32
	fo := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		inOwamp.Add(1)
		defer inOwamp.Add(-1)
		if fi.running.Load() > 0 {
			clash.Add(1)
		}
		time.Sleep(10 * time.Millisecond)
		return summary(t, "owping_to.txt"), nil, nil
	}}
	r := runnerFor(t, fo, "10.0.0.1")
	r.Gate = gate
	r.Meter = c.Meter
	c.Meter.EnableProbes()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); settle(c) }()
	go func() { defer wg.Done(); r.Round(context.Background()) }()
	wg.Wait()
	if clash.Load() != 0 || fi.overlap.Load() != 0 {
		t.Errorf("tests overlapped: %d one-way runs began during a throughput test", clash.Load())
	}
	if fi.count() == 0 || len(fo.calls) == 0 {
		t.Error("one of them did not run")
	}
}

func TestTheGateServesInTurnAndAnAbandonedWaitReleasesNothing(t *testing.T) {
	g := NewGate()
	rel, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("a second holder got in")
	}
	rel()
	rel() // releasing twice must not let a second one in
	r1, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if _, err := g.Acquire(ctx2); err == nil {
		t.Fatal("double release opened the gate to two holders")
	}
	r1()
	var nilGate *Gate
	if r, err := nilGate.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		r()
	}
}

func TestSeriesAreSilentUntilATestRanAndHaveOneHeaderPerFamily(t *testing.T) {
	f := &fakeIperf{capacity: 80e6}
	c, _ := capFor(t, f, func(c *Capacity) { c.UDP = UDPRamp{Enabled: true} })
	c.Meter.budget = nil
	var b bytes.Buffer
	c.Meter.Write(&b)
	if strings.Contains(b.String(), "throughput") {
		t.Fatal("throughput series written before any test")
	}
	settle(c)
	b.Reset()
	c.Meter.Write(&b)
	types := map[string]int{}
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(l, "# TYPE continuum_link_throughput") {
			types[strings.Fields(l)[2]]++
		}
	}
	if len(types) < 8 {
		t.Errorf("families: %v", types)
	}
	for name, n := range types {
		if n != 1 {
			t.Errorf("%s has %d headers", name, n)
		}
	}
}

func newTestLog(w *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

func TestOnlyThePeersTheServerAskedForAreTested(t *testing.T) {
	f := &fakeIperf{}
	c, _ := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 })
	b := peer("cl-b", "B", "10.0.1.1") // not asked for: the other side runs the tests
	a := peer("cl-a", "A", "10.0.0.1")
	a.Throughput = true
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a, b})
	settle(c)
	if f.count() != 1 || !strings.Contains(f.argsOf(0), "10.0.0.1") {
		t.Fatalf("runs: %v", f.calls)
	}
	got := render(c.Meter)
	for k := range got {
		if strings.Contains(k, "throughput") && strings.Contains(k, `peer="cl-b"`) {
			t.Errorf("a series for a peer that is not tested: %s", k)
		}
	}
	// Switching the tests off for a peer takes its series away on the next peer update.
	a.Throughput = false
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a, b})
	c.Meter.Forget()
	for k := range render(c.Meter) {
		if strings.Contains(k, "throughput") && strings.Contains(k, `peer="cl-a"`) {
			t.Errorf("series stayed after the tests were switched off: %s", k)
		}
	}
	n := f.count()
	settle(c)
	if f.count() != n {
		t.Error("kept testing a peer that is no longer asked for")
	}
}

func TestAPairsOwnIntervalReplacesTheClusters(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0; c.Every = 12 * time.Hour })
	a := peer("cl-a", "A", "10.0.0.1")
	a.Throughput, a.ThroughputIntervalSeconds = true, 3600
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a})
	settle(c)
	if f.count() != 1 {
		t.Fatalf("%d", f.count())
	}
	ck.add(30 * time.Minute)
	settle(c)
	if f.count() != 1 {
		t.Fatal("ran again before the pair's interval")
	}
	ck.add(45 * time.Minute) // 75 min in: past 1h + 10%, far below the cluster's 12h
	settle(c)
	if f.count() != 2 {
		t.Errorf("did not run at the pair's interval: %d runs", f.count())
	}
}

func TestAPairCanHaveABudgetOfItsOwnBesideTheClustersAndItsSeriesSayHowMuchIsUsed(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0; c.Budget = 100 << 30 })
	a := peer("cl-a", "A", "10.0.0.1")
	a.Throughput, a.ThroughputBudgetBytes = true, 7<<30 // room for one fixture test (about 5.5 GB), not two
	b := peer("cl-b", "B", "10.0.1.1")
	b.Throughput = true // no budget of its own: only the cluster's
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a, b})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	settle(c)
	ck.add(14 * time.Hour)
	settle(c) // the second cycle of cl-a does not fit its budget; cl-b's does
	got := render(c.Meter)
	skip := `continuum_link_throughput_tests_skipped_total{cluster="cl-me",peer="%s",method="iperf3",reason="budget"}`
	if got[fmt.Sprintf(skip, "cl-a")] != "1" || got[fmt.Sprintf(skip, "cl-b")] != "0" {
		t.Errorf("skips: a=%s b=%s", got[fmt.Sprintf(skip, "cl-a")], got[fmt.Sprintf(skip, "cl-b")])
	}
	if got[`continuum_link_throughput_pair_budget_bytes{cluster="cl-me",peer="cl-a",method="iperf3"}`] != strconv.Itoa(7<<30) {
		t.Errorf("pair budget gauge: %v", got)
	}
	if v, _ := strconv.ParseUint(got[`continuum_link_throughput_pair_budget_used_bytes{cluster="cl-me",peer="cl-a",method="iperf3"}`], 10, 64); v < 5<<30 || v > 7<<30 {
		t.Errorf("pair budget used = %d", v)
	}
	for k := range got {
		if strings.Contains(k, "pair_budget") && strings.Contains(k, `peer="cl-b"`) {
			t.Errorf("a peer without a budget of its own has one: %s", k)
		}
	}
	// Dropping the budget lifts the limit at once.
	a.ThroughputBudgetBytes = 0
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a, b})
	ck.add(31 * time.Minute)
	n := f.count()
	settle(c)
	if f.count() <= n {
		t.Error("the peer stayed held back after its budget was removed")
	}
}

// A scrape holds the meter's lock and asks for the pair budgets, which need the scheduler's; the scheduler must therefore
// never call the meter while it holds its own. Before that was fixed, this hung within moments.
func TestTheSchedulerAndAScrapeNeverWaitForEachOther(t *testing.T) {
	for name, fail := range map[string]map[string]string{"skip": nil, "failure": {"10.0.0.1": "refused"}} {
		t.Run(name, func(t *testing.T) {
			f := &fakeIperf{fail: fail}
			c, ck := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 })
			a := peer("cl-a", "A", "10.0.0.1")
			a.Throughput, a.ThroughputBudgetBytes = true, 1 // every test is held back as over budget
			if fail != nil {
				a.ThroughputBudgetBytes = 1 << 40
			}
			c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{a})
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{}, 2)
			stop := make(chan struct{})
			go func() {
				defer func() { done <- struct{}{} }()
				for i := 0; i < 3000; i++ {
					ck.add(31 * time.Minute * 3) // past any retry delay
					c.step(context.Background())
				}
			}()
			go func() {
				defer func() { done <- struct{}{} }()
				var b bytes.Buffer
				for {
					select {
					case <-stop:
						return
					default:
						b.Reset()
						c.Meter.Write(&b)
					}
				}
			}()
			select {
			case <-done:
				close(stop)
				<-done
			case <-time.After(30 * time.Second):
				t.Fatal("deadlock: the scheduler and a scrape wait for each other")
			}
		})
	}
}

func TestATestThatFailsIsLeftOutAndTheOthersOfTheCycleStillRun(t *testing.T) {
	f := &fakeIperf{failArg: map[string]string{"-u": "refused"}} // UDP blocked between the clusters
	c, ck := capFor(t, f, func(c *Capacity) {
		c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 40e6, Step: 5 * time.Second, Loss: 0.01}
		c.Reverse, c.Parallel = false, 0
	})
	for i := 0; i < 24*6; i++ { // a day in ten-minute steps
		settle(c)
		ck.add(10 * time.Minute)
	}
	tcp, udp := 0, 0
	for i := 0; i < f.count(); i++ {
		if strings.Contains(f.argsOf(i), " -u ") {
			udp++
		} else {
			tcp++
		}
	}
	// A cycle every 12 hours, give or take: the TCP test is not run again for each failure of the ramp.
	if tcp < 2 || tcp > 3 || udp != tcp {
		t.Errorf("tcp %d udp %d runs in a day with a cycle every 12 hours", tcp, udp)
	}
	if got := render(c.Meter)[`continuum_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="refused"}`]; got != strconv.Itoa(udp) {
		t.Errorf("failures counted = %s want %d", got, udp)
	}
}

func TestAFailingTestInTheMiddleDoesNotStopTheTestsAfterIt(t *testing.T) {
	f := &fakeIperf{failArg: map[string]string{"-P": "refused"}}
	c, _ := capFor(t, f, func(c *Capacity) {
		c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 10e6, Step: 5 * time.Second, Loss: 0.01}
		c.Reverse = false
	})
	settle(c)
	got := render(c.Meter)
	if got[`continuum_link_throughput_udp_saturated{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`] == "" {
		t.Errorf("the ramp after the failed parallel test did not run: %v", f.calls)
	}
}

func TestAPeerThatDoesNotAnswerIsRetriedWithGrowingDelays(t *testing.T) {
	f := &fakeIperf{fail: map[string]string{"10.0.0.1": "refused"}}
	c, ck := capFor(t, f, nil)
	settle(c)
	n := f.count()
	for i, wait := range []time.Duration{30 * time.Minute, 60 * time.Minute, 120 * time.Minute} {
		ck.add(wait - time.Minute)
		settle(c)
		if f.count() != n {
			t.Fatalf("round %d: tried again after %s, before the delay of %s", i, wait-time.Minute, wait)
		}
		ck.add(time.Minute)
		settle(c)
		if f.count() <= n {
			t.Fatalf("round %d: not tried again after %s", i, wait)
		}
		n = f.count()
	}
}

func TestTheBusyReferenceIsTheBestClassNotTheLastOne(t *testing.T) {
	c := &Capacity{DSCP: []int{0, 46}, Parallel: 4}
	c.setBits("cl-a", Forward, 1, 0, 10e9)
	c.setBits("cl-a", Forward, 1, 46, 100e6) // policed, and tested after the unmarked one
	if got := c.reached("cl-a", Forward); got != 10e9 {
		t.Errorf("reached = %v", got)
	}
	c.setBits("cl-a", Forward, 1, 0, 9e9) // the latest unmarked figure replaces the old one
	if got := c.reached("cl-a", Forward); got != 9e9 {
		t.Errorf("reached = %v", got)
	}
}

func TestARampEndsAtTheLastRateTheSenderReallyDelivered(t *testing.T) {
	base := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`
	run := func(sendMax float64) (map[string]string, int) {
		f := &fakeIperf{sendMax: sendMax} // no loss anywhere
		c, _ := capFor(t, f, func(c *Capacity) {
			c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 160e6, Step: 5 * time.Second, Loss: 0.01}
			c.Reverse, c.Parallel = false, 0
		})
		settle(c)
		return render(c.Meter), f.count()
	}
	got, n := run(40e6)
	// tcp + 10, 20, 40 delivered, 80 asked but 40 sent: the ramp stops there, with 40 as the lower bound.
	if n != 5 || !sameNumber(got["continuum_link_throughput_udp_clean_bits_per_second"+base], "4e7") || got["continuum_link_throughput_udp_saturated"+base] != "0" {
		t.Errorf("runs %d clean %q saturated %q", n, got["continuum_link_throughput_udp_clean_bits_per_second"+base], got["continuum_link_throughput_udp_saturated"+base])
	}
	got, _ = run(1e6) // not even the first rung was delivered
	if v, ok := got["continuum_link_throughput_udp_clean_bits_per_second"+base]; ok {
		t.Errorf("a clean rate %s although nothing was delivered", v)
	}
}

func TestAPeerThatGoesAwayTakesItsRemembrancesWithIt(t *testing.T) {
	c, ck := capFor(t, &fakeIperf{}, nil)
	settle(c)
	c.mu.Lock()
	c.warned = map[string]bool{"cl-a|tcp|forward|0": true, "cl-ab|tcp|forward|0": true}
	c.lastBytes["cl-ab|tcp|forward|0"] = 1
	c.setBits("cl-ab", Forward, 1, 0, 1)
	c.mu.Unlock()
	if len(c.lastBytes) < 2 || len(c.lastBits) < 2 {
		t.Fatalf("nothing was remembered: %v %v", c.lastBytes, c.lastBits)
	}
	c.sync([]Peer{{ID: "cl-ab", Throughput: true}}, ck.now())
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lastBytes) != 1 || len(c.lastBits) != 1 || len(c.warned) != 1 || !c.warned["cl-ab|tcp|forward|0"] {
		t.Errorf("left over: %v %v %v", c.lastBytes, c.lastBits, c.warned)
	}
}

func TestAMissingRetransmitCountIsNoSampleNotAZero(t *testing.T) {
	c, _ := capFor(t, &fakeIperf{}, nil)
	c.Meter.ObserveTCP("cl-a", IperfFlow{Direction: Forward, Bits: 1e9}, "tcp", 1, 0)
	got := render(c.Meter)
	key := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",protocol="tcp",streams="1",dscp="0"}`
	if _, ok := got["continuum_link_throughput_retransmits"+key]; ok {
		t.Error("retransmits exported although the sender did not report them")
	}
	c.Meter.ObserveTCP("cl-a", IperfFlow{Direction: Forward, Bits: 1e9, HasRetrans: true}, "tcp", 1, 0)
	if v, ok := render(c.Meter)["continuum_link_throughput_retransmits"+key]; !ok || v != "0" {
		t.Errorf("a reported zero is missing: %q", v)
	}
}
