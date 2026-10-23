package link

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
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
	// tcpBits is the rate the link carries for TCP when a rate cap is given and is above it (0: more than the fixture).
	tcpBits float64
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
	// A TCP test with a rate cap (-b, per stream) cannot carry more than the cap: the fixture's figures, taken at about 4 Gbit/s,
	// are scaled down to it.
	capped := func(doc []byte) ([]byte, []byte, error) {
		b, err := strconv.ParseFloat(val("-b"), 64)
		if err != nil || b <= 0 {
			return doc, nil, nil
		}
		streams := 1.0
		if v, err := strconv.ParseFloat(val("-P"), 64); err == nil {
			streams = v
		}
		rate := b * streams
		if f.tcpBits > 0 && f.tcpBits < rate {
			rate = f.tcpBits
		}
		if scale := rate / fixtureBitsPerSecond; scale < 1 {
			return boundedDoc(doc, scale), nil, nil
		}
		return doc, nil, nil
	}
	switch {
	case has("-b") && !has("-u"):
		switch {
		case has("--bidir"):
			return capped(fixtureBytes("bidir"))
		case has("-P"):
			return capped(fixtureBytes("tcp4"))
		case has("-R"):
			return capped(fixtureBytes("tcp1r"))
		}
		return capped(fixtureBytes("tcp1"))
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

const fixtureBitsPerSecond = 4.15e9

var bytesField = regexp.MustCompile(`"bytes": *(\d+)`)
var bitsField = regexp.MustCompile(`"bits_per_second": *([0-9.eE+]+)`)

// boundedDoc scales every byte count of an iperf3 document, as if the test had stopped after fewer bytes.
func boundedDoc(doc []byte, scale float64) []byte {
	doc = bitsField.ReplaceAllFunc(doc, func(m []byte) []byte {
		v, _ := strconv.ParseFloat(string(bitsField.FindSubmatch(m)[1]), 64)
		return []byte(fmt.Sprintf(`"bits_per_second": %f`, v*scale))
	})
	return bytesField.ReplaceAllFunc(doc, func(m []byte) []byte {
		v, _ := strconv.ParseFloat(string(bytesField.FindSubmatch(m)[1]), 64)
		return []byte(fmt.Sprintf(`"bytes": %d`, int64(v*scale)))
	})
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
	p.Set("cl-me", []*ikhnosv1.LinkPeer{lp})
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
		"ikhnos_link_throughput_bits_per_second" + base + `forward",protocol="tcp",streams="1",dscp="0"}`:   "1.3847713674505e+10",
		"ikhnos_link_throughput_retransmits" + base + `forward",protocol="tcp",streams="1",dscp="0"}`:       "36346",
		"ikhnos_link_throughput_retransmits" + base + `reverse",protocol="tcp",streams="1",dscp="0"}`:       "26052",
		"ikhnos_link_throughput_bits_per_second" + base + `reverse",protocol="tcp",streams="1",dscp="0"}`:   "1.4543531405561e+10",
		"ikhnos_link_throughput_udp_clean_bits_per_second" + base + `forward",dscp="0"}`:                    "8e+07",
		"ikhnos_link_throughput_udp_saturated" + base + `forward",dscp="0"}`:                                "1",
		"ikhnos_link_throughput_udp_loss_ratio" + base + `forward",dscp="0",point="clean"}`:                 "0",
		"ikhnos_link_throughput_udp_point_bits_per_second" + base + `forward",dscp="0",point="clean"}`:      "8e+07",
		"ikhnos_link_throughput_udp_point_bits_per_second" + base + `forward",dscp="0",point="first_loss"}`: "1.6e+08",
	} {
		if g := got[key]; !sameNumber(g, want) {
			t.Errorf("%s = %q, want %q", key, g, want)
		}
	}
	bi := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="reverse",protocol="tcp_bidir",streams="1",dscp="0"}`
	if got["ikhnos_link_throughput_retransmits"+bi] != "96" {
		t.Errorf("the bidirectional test's own series: %q", got["ikhnos_link_throughput_retransmits"+bi])
	}
	if l := got["ikhnos_link_throughput_udp_loss_ratio"+base+`forward",dscp="0",point="first_loss"}`]; l == "" || l == "0" {
		t.Errorf("the rung over the capacity shows no loss: %q", l)
	}
	if j := got["ikhnos_link_throughput_udp_jitter_seconds"+base+`forward",dscp="0",point="first_loss"}`]; j == "" {
		t.Error("the first lossy rung has no jitter")
	}
	// Six rungs were run per direction, but only two points of each are exported: the series do not follow the rungs.
	for k := range got {
		if strings.Contains(k, "rate_bits_per_second") || (strings.Contains(k, "throughput_udp_") && strings.Contains(k, "loss_ratio") && !strings.Contains(k, "point=")) {
			t.Errorf("a per-rung series is exported: %s", k)
		}
	}
	if got["ikhnos_link_throughput_test_bytes_total"+base+`forward"}`] == "0" || got[`ikhnos_link_throughput_last_success_timestamp_seconds{cluster="cl-me",peer="cl-a",method="iperf3"}`] == "" {
		t.Error("bytes carried / last success not recorded")
	}
	for _, r := range tpFailReasons {
		if got[`ikhnos_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="`+r+`"}`] != "0" {
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
	if got["ikhnos_link_throughput_udp_saturated"+base] != "0" || !sameNumber(got["ikhnos_link_throughput_udp_clean_bits_per_second"+base], "4e7") {
		t.Errorf("saturated %q clean %q", got["ikhnos_link_throughput_udp_saturated"+base], got["ikhnos_link_throughput_udp_clean_bits_per_second"+base])
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
	if _, ok := got["ikhnos_link_throughput_udp_clean_bits_per_second"+base]; ok {
		t.Error("a clean rate is reported although every rung lost")
	}
	pt := func(m, point string) string {
		return got["ikhnos_link_throughput_udp_"+m+`{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0",point="`+point+`"}`]
	}
	if pt("loss_ratio", "clean") != "" || pt("point_bits_per_second", "clean") != "" || pt("loss_ratio", "first_loss") == "" || !sameNumber(pt("point_bits_per_second", "first_loss"), "1e7") {
		t.Errorf("a ramp that lost at once has no clean point and its first rung as the first loss: %v", got)
	}
	if got["ikhnos_link_throughput_udp_saturated"+base] != "1" {
		t.Error("not saturated")
	}
}

func TestARampThatNeverLosesHasNoFirstLossPoint(t *testing.T) {
	f := &fakeIperf{capacity: 0}
	c, _ := capFor(t, f, func(c *Capacity) {
		c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 40e6, Step: 5 * time.Second, Loss: 0.01}
		c.Reverse, c.Parallel = false, 0
	})
	settle(c)
	got := render(c.Meter)
	base := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0",point="`
	if _, ok := got["ikhnos_link_throughput_udp_loss_ratio"+base+`clean"}`]; !ok {
		t.Error("no clean point")
	}
	if !sameNumber(got["ikhnos_link_throughput_udp_point_bits_per_second"+base+`clean"}`], "4e7") {
		t.Errorf("the clean point is not the top rung: %v", got)
	}
	for k := range got {
		if strings.Contains(k, `point="first_loss"`) {
			t.Errorf("a first-loss point without any loss: %s", k)
		}
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
	for k, v := range got {
		if strings.Contains(k, "udp_point_bits_per_second") && strings.Contains(k, `point="clean"`) && !sameNumber(v, "2e7") {
			t.Errorf("the old ramp's top rung survived: %s = %s", k, v)
		}
		if strings.Contains(k, `rate_bits_per_second`) {
			t.Errorf("a per-rung series: %s", k)
		}
	}
	c.Meter.Peers.Set("cl-me", nil)
	c.Meter.Forget()
	ck.add(peerGrace + time.Minute)
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
	if got[`ikhnos_link_throughput_tests_skipped_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="budget"}`] != "1" {
		t.Errorf("skip not counted: %v", got)
	}
	if used, limit := c.ledger.Used(ck.now()), c.ledger.Limit; used < 5<<30 || used > limit || got[`ikhnos_link_throughput_budget_bytes{cluster="cl-me",method="iperf3"}`] != strconv.Itoa(7<<30) {
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
	if render(c.Meter)[`ikhnos_link_throughput_tests_skipped_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="link_busy"}`] != "1" {
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
	if render(c.Meter)[`ikhnos_link_throughput_bits_per_second{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",protocol="tcp",streams="1",dscp="0"}`] == "" {
		t.Fatal("the free node was not used")
	}
	f2 := &fakeIperf{busy: map[string]bool{"10.0.0.1": true, "10.0.0.2": true}}
	c2, _ := capFor(t, f2, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 }, "10.0.0.1", "10.0.0.2")
	settle(c2)
	got := render(c2.Meter)
	if got[`ikhnos_link_throughput_tests_skipped_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="server_busy"}`] != "1" {
		t.Errorf("not counted as a wait: %v", got)
	}
	for _, r := range tpFailReasons {
		if got[`ikhnos_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="`+r+`"}`] != "0" {
			t.Errorf("a busy server was counted as a failure (%s)", r)
		}
	}
}

func TestAnUnreachablePeerIsOneFailureAndTheCycleIsDropped(t *testing.T) {
	f := &fakeIperf{fail: map[string]string{"10.0.0.1": "refused", "10.0.0.2": "refused"}}
	c, ck := capFor(t, f, nil, "10.0.0.1", "10.0.0.2")
	settle(c)
	got := render(c.Meter)
	if got[`ikhnos_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="refused"}`] != "1" {
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
		if strings.HasPrefix(l, "# TYPE ikhnos_link_throughput") {
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
	c.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{a, b})
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
	c.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{a, b})
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
	c.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{a})
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
	c.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{a, b})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	settle(c)
	ck.add(14 * time.Hour)
	settle(c) // the second cycle of cl-a does not fit its budget; cl-b's does
	got := render(c.Meter)
	skip := `ikhnos_link_throughput_tests_skipped_total{cluster="cl-me",peer="%s",method="iperf3",reason="budget"}`
	if got[fmt.Sprintf(skip, "cl-a")] != "1" || got[fmt.Sprintf(skip, "cl-b")] != "0" {
		t.Errorf("skips: a=%s b=%s", got[fmt.Sprintf(skip, "cl-a")], got[fmt.Sprintf(skip, "cl-b")])
	}
	if got[`ikhnos_link_throughput_pair_budget_bytes{cluster="cl-me",peer="cl-a",method="iperf3"}`] != strconv.Itoa(7<<30) {
		t.Errorf("pair budget gauge: %v", got)
	}
	if v, _ := strconv.ParseUint(got[`ikhnos_link_throughput_pair_budget_used_bytes{cluster="cl-me",peer="cl-a",method="iperf3"}`], 10, 64); v < 5<<30 || v > 7<<30 {
		t.Errorf("pair budget used = %d", v)
	}
	for k := range got {
		if strings.Contains(k, "pair_budget") && strings.Contains(k, `peer="cl-b"`) {
			t.Errorf("a peer without a budget of its own has one: %s", k)
		}
	}
	// Dropping the budget lifts the limit at once.
	a.ThroughputBudgetBytes = 0
	c.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{a, b})
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
			c.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{a})
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
	if got := render(c.Meter)[`ikhnos_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="refused"}`]; got != strconv.Itoa(udp) {
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
	if got[`ikhnos_link_throughput_udp_saturated{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`] == "" {
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
	c.setBits("cl-a", Forward, 1, 0, 10e9, false)
	c.setBits("cl-a", Forward, 1, 46, 100e6, false) // policed, and tested after the unmarked one
	if got := c.reached("cl-a", Forward); got != 10e9 {
		t.Errorf("reached = %v", got)
	}
	c.setBits("cl-a", Forward, 1, 0, 9e9, false) // the latest unmarked figure replaces the old one
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
	if n != 5 || !sameNumber(got["ikhnos_link_throughput_udp_clean_bits_per_second"+base], "4e7") || got["ikhnos_link_throughput_udp_saturated"+base] != "0" {
		t.Errorf("runs %d clean %q saturated %q", n, got["ikhnos_link_throughput_udp_clean_bits_per_second"+base], got["ikhnos_link_throughput_udp_saturated"+base])
	}
	got, _ = run(1e6) // not even the first rung was delivered
	if v, ok := got["ikhnos_link_throughput_udp_clean_bits_per_second"+base]; ok {
		t.Errorf("a clean rate %s although nothing was delivered", v)
	}
}

func TestAPeerThatGoesAwayTakesItsRemembrancesWithIt(t *testing.T) {
	c, ck := capFor(t, &fakeIperf{}, nil)
	settle(c)
	c.mu.Lock()
	c.warned = map[string]bool{"cl-a|tcp|forward|0": true, "cl-ab|tcp|forward|0": true}
	c.lastBytes["cl-ab|tcp|forward|0"] = 1
	c.setBits("cl-ab", Forward, 1, 0, 1, false)
	c.mu.Unlock()
	if len(c.lastBytes) < 2 || len(c.lastBits) < 2 {
		t.Fatalf("nothing was remembered: %v %v", c.lastBytes, c.lastBits)
	}
	c.sync([]Peer{{ID: "cl-ab", Throughput: true}}, ck.now())
	ck.add(peerGrace + time.Minute) // inside the grace nothing is forgotten (see state_blip_test.go)
	c.sync([]Peer{{ID: "cl-ab", Throughput: true}}, ck.now())
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lastBytes) != 1 || len(c.lastBits) != 1 || len(c.warned) != 1 || !c.warned["cl-ab|tcp|forward|0"] {
		t.Errorf("left over: %v %v %v", c.lastBytes, c.lastBits, c.warned)
	}
}

func TestAMissingRetransmitCountIsNoSampleNotAZero(t *testing.T) {
	c, _ := capFor(t, &fakeIperf{}, nil)
	c.Meter.ObserveTCP("cl-a", IperfFlow{Direction: Forward, Bits: 1e9}, "tcp", 1, 0, false)
	got := render(c.Meter)
	key := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",protocol="tcp",streams="1",dscp="0"}`
	if _, ok := got["ikhnos_link_throughput_retransmits"+key]; ok {
		t.Error("retransmits exported although the sender did not report them")
	}
	c.Meter.ObserveTCP("cl-a", IperfFlow{Direction: Forward, Bits: 1e9, HasRetrans: true}, "tcp", 1, 0, false)
	if v, ok := render(c.Meter)["ikhnos_link_throughput_retransmits"+key]; !ok || v != "0" {
		t.Errorf("a reported zero is missing: %q", v)
	}
}

// With a loss threshold above 10% the receiver's rate at a rung can be below 90% of the asked one only because the link dropped
// that much; the sender still offered it. The ramp must go on to the first rung over the threshold.
func TestARampWithAHighLossThresholdIsNotMistakenForASenderThatCouldNotKeepUp(t *testing.T) {
	base := `{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`
	f := &fakeIperf{capacity: 34e6} // 40M offered: 15% lost, 34M received
	c, _ := capFor(t, f, func(c *Capacity) {
		c.UDP = UDPRamp{Enabled: true, Start: 10e6, Max: 160e6, Step: 5 * time.Second, Loss: 0.2}
		c.Reverse, c.Parallel = false, 0
	})
	settle(c)
	got := render(c.Meter)
	// tcp + rungs 10, 20, 40 (15% lost, within 20%) and 80 (57% lost).
	if n := f.count(); n != 5 {
		t.Errorf("%d runs, want 5: %v", n, f.calls)
	}
	if !sameNumber(got["ikhnos_link_throughput_udp_clean_bits_per_second"+base], "4e7") || got["ikhnos_link_throughput_udp_saturated"+base] != "1" {
		t.Errorf("clean %q saturated %q, want 4e7 and 1", got["ikhnos_link_throughput_udp_clean_bits_per_second"+base], got["ikhnos_link_throughput_udp_saturated"+base])
	}
}

func TestTheOfferedRateIsReconstructedFromTheReceiversFigures(t *testing.T) {
	for name, tc := range map[string]struct {
		f    IperfFlow
		want float64
	}{
		"no loss":     {IperfFlow{Bits: 40e6, Packets: 100}, 40e6},
		"15% lost":    {IperfFlow{Bits: 34e6, Packets: 100, Lost: 15}, 40e6},
		"all lost":    {IperfFlow{Bits: 0, Packets: 100, Lost: 100}, 0},
		"no datagram": {IperfFlow{Bits: 5e6}, 5e6},
	} {
		if got := offeredBits(tc.f); got < tc.want*0.999 || got > tc.want*1.001 {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestATCPRateCapAboveWhatTheUDPRampAllowsIsRefused(t *testing.T) {
	c := &Capacity{TCPMaxRate: 400e9}
	c.Defaults()
	if err := c.Validate(); err != nil {
		t.Errorf("400G is the top: %v", err)
	}
	c.TCPMaxRate = 400e9 + 1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "400G") {
		t.Errorf("a cap above 400G was accepted: %v", err)
	}
	// The estimate at the top of what is allowed does not wrap around: 400G x 32 streams x 70 s.
	c = &Capacity{TCPMaxRate: 400e9, Parallel: 32, Duration: 60 * time.Second, Omit: 10 * time.Second}
	c.Defaults()
	est := c.estimate(Peer{ID: "x"}, capTest{kind: "tcp-parallel"})
	if want := uint64(400e9) * 32 * 70 / 8; est != want {
		t.Errorf("estimate %d, want %d", est, want)
	}
}

// The connect timing keeps out of the throughput tests: the flag is on exactly while iperf3 runs.
func TestTheTestingFlagIsOnExactlyWhileTheClientRuns(t *testing.T) {
	f := &fakeIperf{}
	var c *Capacity
	var during []bool
	var gate bool
	c, _ = capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0 })
	inner := c.Exec
	c.Exec = func(ctx context.Context, env []string, name string, args ...string) ([]byte, []byte, error) {
		during = append(during, c.Testing.On())
		return inner(ctx, env, name, args...)
	}
	if c.Testing.On() {
		t.Fatal("on before any test")
	}
	settle(c)
	gate = c.Testing.On()
	if len(during) == 0 || gate {
		t.Fatalf("during %v, after %v", during, gate)
	}
	for _, on := range during {
		if !on {
			t.Errorf("the flag was off while iperf3 ran: %v", during)
		}
	}
}

// The settings page offers 500 MB and 50 MB a day, less than the guess for a first TCP test (a full gigabit for the whole test).
// Without a bounded first test none would ever run, and since the held-back test stayed first in the queue, the ramp behind it
// would not run either.
func TestATestThatWouldNotFitTheBudgetRunsRateCappedAndTheTestsBehindItAreNotHeldBack(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) {
		c.Budget = 500 << 20
		c.Reverse, c.Parallel = false, 0
		c.UDP = UDPRamp{Enabled: true, Start: 5e6, Max: 20e6, Step: 2 * time.Second, Loss: 0.01}
	})
	settle(c)
	if f.count() < 2 {
		t.Fatalf("%d runs: the TCP test and the ramp should both have run", f.count())
	}
	first := f.argsOf(0)
	if !strings.Contains(first, "-t 10") || !strings.Contains(first, "-O 2") || !strings.Contains(first, " -b ") {
		t.Errorf("the first TCP test keeps its length and gets a rate cap: %s", first)
	}
	if strings.Contains(first, "-n ") {
		t.Errorf("a test is no longer ended by bytes: %s", first)
	}
	// 125 MB over 12 seconds is 87 Mbit/s; the fixture's gigabit link comes up to the cap, so the reading is a floor.
	if v := boundedOf(render(c.Meter), "forward"); v != "1" {
		t.Errorf("a capped reading that reached its cap is not marked as a floor: %q", v)
	}
	var ramp bool
	for i := 1; i < f.count(); i++ {
		ramp = ramp || strings.Contains(f.argsOf(i), "-u")
	}
	if !ramp {
		t.Error("the ramp did not run behind the bounded test")
	}
	if used := c.ledger.Used(ck.now()); used == 0 || used > c.ledger.Limit {
		t.Errorf("budget used = %d of %d", used, c.ledger.Limit)
	}
}

func TestATestHasNoCapWhenItFitsItsShareAndOneWhenItDoesNot(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) { c.Reverse, c.Parallel = false, 0; c.Every = time.Hour })
	settle(c)
	if a := f.argsOf(0); strings.Contains(a, " -b ") {
		t.Errorf("a test with no budget was capped: %s", a)
	}
	// What the first test carried (several GB) is more than a 24th of 500 MB: the next one is bounded to that share.
	c.ledger.Limit = 500 << 20
	ck.add(25 * time.Hour) // the first test's several GB have left the 24-hour window
	settle(c)
	a := f.argsOf(f.count() - 1)
	if !strings.Contains(a, " -b ") || !strings.Contains(a, "-t 10") {
		t.Fatalf("a test larger than its share keeps its length and gets a cap: %s", a)
	}
}

func TestATestThatCanNeverFitIsDroppedFromTheCycleNotRetriedForever(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) {
		c.Budget = 4 << 20 // a test's share (1 MiB) cannot pay for a rate cap of 1 Mbit/s
		c.Reverse, c.Parallel = false, 0
		c.UDP = UDPRamp{Enabled: true, Start: 1e6, Max: 4e6, Step: 2 * time.Second, Loss: 0.01}
	})
	settle(c)
	ck.add(time.Minute)
	settle(c)
	var tcp, ramp int
	for i := 0; i < f.count(); i++ {
		if strings.Contains(f.argsOf(i), "-u") {
			ramp++
		} else {
			tcp++
		}
	}
	if tcp != 0 || ramp == 0 {
		t.Errorf("tcp runs %d, ramp runs %d: the TCP test can never fit, the ramp can", tcp, ramp)
	}
}

// Under the 50 MB a day of the settings page the default ramp (up to 200 Mbit/s, about 320 MB) would never run: it stops at the highest
// rate whose whole ramp fits its share, and still reports, as a lower bound.
func TestTheRampStopsAtTheHighestRateThatFitsItsShareOfASmallBudget(t *testing.T) {
	f := &fakeIperf{}
	c, _ := capFor(t, f, func(c *Capacity) {
		c.Budget = 50 << 20
		c.Reverse, c.Parallel = false, 0
		c.UDP = UDPRamp{Enabled: true, Start: 5e6, Max: 200e6, Step: 5 * time.Second, Loss: 0.01}
	})
	settle(c)
	var top float64
	var ramps int
	for i := 0; i < f.count(); i++ {
		a := f.argsOf(i)
		if !strings.Contains(a, "-u") {
			continue
		}
		ramps++
		fs := strings.Fields(a)
		for j, x := range fs {
			if x == "-b" {
				v, _ := strconv.ParseFloat(fs[j+1], 64)
				top = max(top, v)
			}
		}
	}
	if ramps == 0 || top > 20e6 {
		t.Fatalf("%d ramp steps, highest rate %v: the ramp should run, and stay within its share", ramps, top)
	}
	if used := c.ledger.Used(c.Now()); used > c.ledger.Limit {
		t.Errorf("used %d of %d", used, c.ledger.Limit)
	}
}

func boundedOf(got map[string]string, dir string) string {
	for k, v := range got {
		if strings.HasPrefix(k, "ikhnos_link_throughput_bounded{") && strings.Contains(k, `direction="`+dir+`"`) && strings.Contains(k, `protocol="tcp"`) {
			return v
		}
	}
	return ""
}

// A link slower than the cap is what the test measured: the reading is exact, not a floor.
func TestAReadingBelowItsCapIsExactAndNotMarkedAsAFloor(t *testing.T) {
	f := &fakeIperf{tcpBits: 20e6}
	c, _ := capFor(t, f, func(c *Capacity) { c.Budget = 500 << 20; c.Reverse, c.Parallel = false, 0 })
	settle(c)
	got := render(c.Meter)
	if v := boundedOf(got, "forward"); v != "0" {
		t.Errorf("bounded = %q for a link below the cap", v)
	}
	// And a run is not charged for more than it carried.
	if used := c.ledger.Used(c.Now()); used > 40<<20 {
		t.Errorf("charged %d bytes for a 20 Mbit/s link", used)
	}
}

// A reading that came up to its cap says nothing about the size of a test without one: the next estimate stays large, so the next
// test is capped again instead of being run uncapped on a link that carries a hundred times its share.
func TestACappedReadingDoesNotMakeTheNextTestRunUncapped(t *testing.T) {
	f := &fakeIperf{}
	c, ck := capFor(t, f, func(c *Capacity) { c.Budget = 500 << 20; c.Reverse, c.Parallel = false, 0; c.Every = time.Hour })
	settle(c)
	ck.add(25 * time.Hour)
	settle(c)
	if f.count() < 2 {
		t.Fatalf("%d runs", f.count())
	}
	if a := f.argsOf(f.count() - 1); !strings.Contains(a, " -b ") {
		t.Errorf("the second test ran uncapped: %s", a)
	}
}
