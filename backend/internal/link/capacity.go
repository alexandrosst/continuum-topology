package link

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExecEnvFunc is ExecFunc with extra environment, for the one thing iperf3 reads from it (the password).
type ExecEnvFunc func(ctx context.Context, env []string, name string, args ...string) (stdout, stderr []byte, err error)

func osExecEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "LC_ALL=C"}, env...)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return capBytes(out.Bytes()), capBytes(errb.Bytes()), err
}

// UDPRamp is the loss ramp: UDP at Start, doubled each step until more than Loss of it is lost or Max is reached. The
// highest rate that stayed within Loss is what the link carries without dropping.
type UDPRamp struct {
	Enabled    bool
	Start, Max uint64 // bits per second
	Step       time.Duration
	Loss       float64 // fraction
}

// Capacity runs iperf3 against the responders of the paired clusters, now and then, within limits. Where the one-way probe
// answers "is the link healthy", this answers "how much can it carry, and where does it start to drop": TCP with one
// stream and with several, in each direction, and a UDP ramp that finds the rate at which loss begins.
//
// The limits are what make it fit to leave running: one test at a time anywhere in the process (shared with the one-way
// probe through Gate), a budget of bytes per day, a wait while real traffic is using the link, and an optional time of day.
type Capacity struct {
	Meter *Meter
	Log   *slog.Logger
	// Gate is shared with the Runner so a throughput test never overlaps a one-way probe. Load, if set, says how busy the
	// links are.
	Gate *Gate
	Load *Load

	Bin  string
	Port int

	// Every is how often each peer's whole plan is run. Duration is how long each TCP test sends, Omit how much of its
	// start is left out of the figures (TCP slow start).
	Every, Duration, Omit time.Duration
	// Parallel is the number of streams of the multi-stream test (below 2: not run).
	Parallel int
	// Reverse also tests from the peer to here; Bidir adds a test of both at once.
	Reverse, Bidir bool
	UDP            UDPRamp
	// DSCP lists the classes the single-stream TCP test and the UDP ramp are repeated for. Nil: unmarked only.
	DSCP []int
	// TCPMaxRate caps each TCP stream (bits per second; 0: as fast as it goes).
	TCPMaxRate uint64

	// Budget is the bytes the tests may carry per BudgetWindow, all peers together (0: unlimited).
	Budget       uint64
	BudgetWindow time.Duration
	// BusyFraction: a test waits while the observed traffic in its direction is above this fraction of the rate the last TCP
	// test reached there. SkipAbove is the same with an absolute rate, used until a first test has set the former.
	BusyFraction float64
	SkipAbove    uint64
	Window       TimeWindow

	// User, KeyFile (the server's RSA public key) and PasswordFile select iperf3's authenticated mode, in which the server
	// refuses anyone without the credentials.
	User, KeyFile, PasswordFile string

	// Pause is left between two tests so that the one-way probe gets its turn. StartAfter bounds the random delay before a
	// peer is first tested (negative: none).
	Pause      time.Duration
	StartAfter time.Duration

	Exec ExecEnvFunc
	Now  func() time.Time

	password string
	ledger   *Ledger

	mu        sync.Mutex
	pairs     map[string]*Ledger // peer -> its own daily budget, for the peers that have one
	peers     map[string]*capPeer
	good      map[string]string
	lastBytes map[string]uint64
	lastBits  map[string]float64
	warned    map[string]bool
}

type capTest struct {
	kind string // tcp, tcp-parallel, tcp-bidir, udp-ramp
	dir  Direction
	dscp int
}

func (t capTest) key(peer string) string {
	return fmt.Sprintf("%s|%s|%s|%d", peer, t.kind, t.dir, t.dscp)
}

type capPeer struct {
	next    time.Time // when the next cycle starts
	retryAt time.Time // the unfinished cycle waits until then
	queue   []capTest // what is left of this cycle
	// ran is true once a test of this cycle has finished: the peer answers, so a failure after that belongs to the test.
	ran bool
	// failures counts the cycles in a row that failed at their first test, for the back-off.
	failures int
}

// Defaults fills what was left unset.
func (c *Capacity) Defaults() {
	if c.Bin == "" {
		c.Bin = "iperf3"
	}
	if c.Port == 0 {
		c.Port = 8862
	}
	if c.Every == 0 {
		c.Every = 12 * time.Hour
	}
	if c.Duration == 0 {
		c.Duration = 10 * time.Second
	}
	if c.UDP.Enabled {
		if c.UDP.Start == 0 {
			c.UDP.Start = 5e6
		}
		if c.UDP.Max == 0 {
			c.UDP.Max = 200e6
		}
		if c.UDP.Step == 0 {
			c.UDP.Step = 5 * time.Second
		}
		if c.UDP.Loss == 0 {
			c.UDP.Loss = 0.01
		}
	}
	if len(c.DSCP) == 0 {
		c.DSCP = []int{0}
	}
	if c.BudgetWindow == 0 {
		c.BudgetWindow = 24 * time.Hour
	}
	if c.Pause == 0 {
		c.Pause = 3 * time.Second
	}
	if c.Exec == nil {
		c.Exec = osExecEnv
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.ledger == nil {
		c.ledger = &Ledger{Window: c.BudgetWindow, Limit: c.Budget}
	}
	if c.lastBytes == nil {
		c.lastBytes = map[string]uint64{}
	}
}

// Validate checks the settings so a bad value fails at start-up.
func (c *Capacity) Validate() error {
	whole := func(name string, d, lo, hi time.Duration) error {
		if d < lo || d > hi || d%time.Second != 0 {
			return fmt.Errorf("%s must be a whole number of seconds between %s and %s, got %s", name, lo, hi, d)
		}
		return nil
	}
	if c.Every < 10*time.Minute {
		return fmt.Errorf("running the throughput tests more often than every 10 minutes is not supported, got %s", c.Every)
	}
	if err := whole("test duration", c.Duration, 3*time.Second, 60*time.Second); err != nil {
		return err
	}
	if c.Omit < 0 || c.Omit > 10*time.Second || c.Omit >= c.Duration || c.Omit%time.Second != 0 {
		return fmt.Errorf("omitted start must be whole seconds, at most 10s and shorter than the test, got %s", c.Omit)
	}
	if c.Parallel < 0 || c.Parallel > 32 || c.Parallel == 1 {
		return fmt.Errorf("parallel streams must be 0 (off) or between 2 and 32, got %d", c.Parallel)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid iperf3 port %d", c.Port)
	}
	if c.UDP.Enabled {
		if c.UDP.Start < 100e3 || c.UDP.Max < c.UDP.Start || c.UDP.Max > 400e9 || c.UDP.Max/c.UDP.Start > 1024 {
			return fmt.Errorf("UDP ramp from %d to %d bit/s is not valid: start at least 100k, top not below start, at most 400G and 1024 times the start", c.UDP.Start, c.UDP.Max)
		}
		if err := whole("UDP step", c.UDP.Step, 2*time.Second, 30*time.Second); err != nil {
			return err
		}
		if c.UDP.Loss <= 0 || c.UDP.Loss > 0.5 {
			return fmt.Errorf("UDP loss threshold must be above 0 and at most 50%%, got %g", c.UDP.Loss*100)
		}
	}
	if len(c.DSCP) > 4 {
		return errors.New("at most 4 DSCP classes")
	}
	seen := map[int]bool{}
	for _, d := range c.DSCP {
		if d < 0 || d > 63 || seen[d] {
			return fmt.Errorf("DSCP classes must be different values between 0 and 63, got %v", c.DSCP)
		}
		seen[d] = true
	}
	if c.BusyFraction < 0 || c.BusyFraction > 1 {
		return fmt.Errorf("busy fraction must be between 0 and 1, got %g", c.BusyFraction)
	}
	if n := countSet(c.User, c.KeyFile, c.PasswordFile); n != 0 && n != 3 {
		return errors.New("authenticated mode needs a user, the server's public key file and a password file")
	}
	return nil
}

func countSet(ss ...string) int {
	n := 0
	for _, s := range ss {
		if s != "" {
			n++
		}
	}
	return n
}

// Start reads the credentials and wires the budget into the meter. It is called once before Run.
func (c *Capacity) Start() error {
	c.Defaults()
	if err := c.Validate(); err != nil {
		return err
	}
	if c.PasswordFile != "" {
		b, err := os.ReadFile(c.PasswordFile)
		if err != nil {
			return fmt.Errorf("password file: %w", err)
		}
		c.password = strings.TrimSpace(string(b))
		if c.password == "" {
			return errors.New("password file is empty")
		}
	}
	c.Meter.SetBudget(func() (uint64, uint64) { return c.ledger.Used(c.Now()), c.ledger.Limit })
	c.Meter.SetPairBudgets(func(peer string) (used, limit uint64, ok bool) {
		c.mu.Lock()
		l := c.pairs[peer]
		c.mu.Unlock()
		if l == nil {
			return 0, 0, false
		}
		l.mu.Lock()
		limit = l.Limit
		l.mu.Unlock()
		return l.Used(c.Now()), limit, true
	})
	return nil
}

// Run tests until ctx ends.
func (c *Capacity) Run(ctx context.Context) {
	if err := c.Start(); err != nil {
		c.Log.Error("throughput tests not started", "err", err)
		return
	}
	for {
		if !sleep(ctx, c.step(ctx)) {
			return
		}
	}
}

// plan is the tests of one cycle for a peer, in order.
func (c *Capacity) plan() []capTest {
	var out []capTest
	dirs := []Direction{Forward}
	if c.Reverse {
		dirs = append(dirs, Reverse)
	}
	for _, ds := range c.DSCP {
		for _, d := range dirs {
			out = append(out, capTest{"tcp", d, ds})
		}
	}
	if c.Parallel >= 2 {
		for _, d := range dirs {
			out = append(out, capTest{"tcp-parallel", d, c.DSCP[0]})
		}
	}
	if c.Bidir {
		out = append(out, capTest{"tcp-bidir", Forward, c.DSCP[0]})
	}
	if c.UDP.Enabled {
		for _, ds := range c.DSCP {
			for _, d := range dirs {
				out = append(out, capTest{"udp-ramp", d, ds})
			}
		}
	}
	return out
}

// sync adds new peers, with a random first test time, and forgets the ones that are gone.
func (c *Capacity) sync(peers []Peer, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peers == nil {
		c.peers = map[string]*capPeer{}
	}
	live := map[string]bool{}
	for _, p := range peers {
		if !p.Throughput {
			continue // the server did not ask for throughput tests towards this one (or the other side runs them)
		}
		live[p.ID] = true
		if c.peers[p.ID] == nil {
			spread := c.StartAfter
			if spread == 0 {
				spread = min(c.Every/4, 10*time.Minute)
			}
			var d time.Duration
			if spread > 0 {
				d = time.Duration(rand.Int64N(int64(spread) + 1))
			}
			c.peers[p.ID] = &capPeer{next: now.Add(d)}
		}
	}
	for id := range c.peers {
		if !live[id] {
			delete(c.peers, id)
			delete(c.good, id)
			delete(c.pairs, id)
			prefix := id + "|"
			dropPrefix(c.lastBytes, prefix)
			dropPrefix(c.lastBits, prefix)
			dropPrefix(c.warned, prefix)
		}
	}
}

// dropPrefix removes the keys of a per-peer map that belong to the peer whose prefix ("id|") is given.
func dropPrefix[V any](m map[string]V, prefix string) {
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			delete(m, k)
		}
	}
}

// step runs at most one test and returns how long to wait before the next look.
func (c *Capacity) step(ctx context.Context) time.Duration {
	now := c.Now()
	var peers []Peer
	for _, p := range c.Meter.Peers.List() {
		if p.Throughput {
			peers = append(peers, p)
		}
	}
	c.sync(peers, now)
	if !c.Window.Contains(now) {
		return min(c.Window.Opens(now), time.Minute)
	}
	// Choose the peer whose turn came first.
	var pick *Peer
	var pickAt time.Time
	c.mu.Lock()
	for i := range peers {
		st := c.peers[peers[i].ID]
		due := st.next
		if len(st.queue) > 0 {
			due = st.retryAt
		}
		if due.After(now) {
			continue
		}
		if pick == nil || due.Before(pickAt) {
			pick, pickAt = &peers[i], due
		}
	}
	var st *capPeer
	var t capTest
	if pick != nil {
		st = c.peers[pick.ID]
		if len(st.queue) == 0 {
			st.queue = c.plan()
			st.ran = false
		}
		t = st.queue[0]
	}
	c.mu.Unlock()
	if pick == nil {
		return 5 * time.Second
	}

	out := c.runTest(ctx, *pick, t)
	now = c.Now()
	// What is counted is recorded after c.mu is released: the meter takes its own lock while it writes (and then asks for
	// the pair budgets, which take c.mu), so calling it with c.mu held is a lock-order inversion.
	var skipped, failed string
	c.mu.Lock()
	switch {
	case ctx.Err() != nil:
	case out.skip != "":
		st.retryAt = now.Add(out.retry)
		skipped = out.skip
	case out.fail != "":
		failed = out.fail
		if st.ran || out.fail == "other" {
			// The peer answers (or the test, not the peer, is what is wrong): leave this test out and go on with the
			// rest, so the tests before it are not run again and the ones after it are not starved.
			st.failures = 0
			st.queue = st.queue[1:]
			c.advance(st, *pick, now)
		} else {
			st.queue = nil
			st.failures++
			st.next = now.Add(min(c.Every, 30*time.Minute<<min(st.failures-1, 6)))
		}
	default:
		st.ran, st.failures = true, 0
		st.queue = st.queue[1:]
		c.advance(st, *pick, now)
	}
	c.mu.Unlock()
	if skipped != "" {
		c.Meter.ThroughputSkipped(pick.ID, skipped)
	}
	if failed != "" {
		c.Meter.ThroughputFailed(pick.ID, failed)
	}
	return c.Pause
}

// advance moves a peer on after one test of its plan was dealt with: the next test follows at once, or, when the plan is
// done, the next cycle is scheduled. Called with c.mu held.
func (c *Capacity) advance(st *capPeer, p Peer, now time.Time) {
	if len(st.queue) > 0 {
		st.retryAt = now
		return
	}
	every := c.Every
	if p.ThroughputEvery > 0 {
		every = p.ThroughputEvery
	}
	st.next = now.Add(time.Duration(float64(every) * (0.9 + 0.2*rand.Float64())))
}

type outcome struct {
	skip  string // held back, for the metric
	retry time.Duration
	fail  string
}

func (c *Capacity) runTest(ctx context.Context, p Peer, t capTest) outcome {
	now := c.Now()
	if est := c.estimate(p, t); !c.allows(p, now, est) {
		c.warnOversized(p, t, est)
		return outcome{skip: "budget", retry: 30 * time.Minute}
	}
	if c.busy(p, t, now) {
		return outcome{skip: "link_busy", retry: 10 * time.Minute}
	}
	if t.kind == "udp-ramp" {
		return c.runRamp(ctx, p, t)
	}
	streams, extra := 1, []string{}
	if t.kind == "tcp-parallel" {
		streams = c.Parallel
	}
	extra = append(extra, "-t", secs(c.Duration))
	if c.Omit > 0 {
		extra = append(extra, "-O", secs(c.Omit))
	}
	if streams > 1 {
		extra = append(extra, "-P", strconv.Itoa(streams))
	}
	if c.TCPMaxRate > 0 {
		extra = append(extra, "-b", strconv.FormatUint(c.TCPMaxRate, 10))
	}
	if t.kind == "tcp-bidir" {
		extra = append(extra, "--bidir")
	}
	res, out := c.invoke(ctx, p, t, extra)
	if out != (outcome{}) {
		return out
	}
	var total uint64
	proto := "tcp"
	if t.kind == "tcp-bidir" {
		proto = "tcp_bidir"
	}
	for _, f := range res.Flows {
		total += f.Bytes
		c.Meter.ObserveTCP(p.ID, f, proto, res.Streams, t.dscp)
		if proto == "tcp" { // a test loading both ways reaches less in each, which says nothing about the link's capacity
			c.mu.Lock()
			c.setBits(p.ID, f.Direction, res.Streams, t.dscp, f.Bits)
			c.mu.Unlock()
		}
	}
	// The omitted start was sent too, at about the same rate.
	total += uint64(float64(total) * c.Omit.Seconds() / c.Duration.Seconds())
	c.charge(p, c.Now(), total)
	c.mu.Lock()
	c.lastBytes[t.key(p.ID)] = total
	c.mu.Unlock()
	return outcome{}
}

func (c *Capacity) setBits(peer string, d Direction, streams, dscp int, bits float64) {
	if c.lastBits == nil {
		c.lastBits = map[string]float64{}
	}
	c.lastBits[fmt.Sprintf("%s|%s|%d|%d", peer, d, streams, dscp)] = bits
}

// reached is the highest TCP rate the latest tests reached towards (Forward) or from (Reverse) the peer, over the stream
// counts and DSCP classes; 0 before the first. A class that is policed does not lower it.
func (c *Capacity) reached(peer string, d Direction) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best float64
	for _, s := range []int{1, c.Parallel} {
		for _, ds := range c.DSCP {
			if v := c.lastBits[fmt.Sprintf("%s|%s|%d|%d", peer, d, s, ds)]; v > best {
				best = v
			}
		}
	}
	return best
}

func (c *Capacity) runRamp(ctx context.Context, p Peer, t capTest) outcome {
	var steps []UDPStep
	var clean uint64
	var hasClean, saturated bool
	for rate := c.UDP.Start; ; {
		if len(steps) > 0 {
			if !sleep(ctx, c.Pause) {
				return outcome{skip: "server_busy", retry: time.Minute} // stopping; nothing is reported
			}
			if !c.allows(p, c.Now(), rate*uint64(c.UDP.Step.Seconds())/8) {
				return outcome{skip: "budget", retry: 30 * time.Minute}
			}
		}
		res, out := c.invoke(ctx, p, t, []string{"-u", "-b", strconv.FormatUint(rate, 10), "-t", secs(c.UDP.Step)})
		if out != (outcome{}) {
			return out
		}
		f, ok := res.Flow(t.dir)
		if !ok {
			return outcome{fail: "other"}
		}
		c.charge(p, c.Now(), f.Bytes)
		c.Meter.AddTestBytes(p.ID, t.dir, f.Bytes)
		steps = append(steps, UDPStep{Rate: rate, Loss: f.LossRatio(), Jitter: f.Jitter})
		if f.LossRatio() > c.UDP.Loss {
			saturated = true
			break
		}
		if f.Bits < 0.9*float64(rate) {
			// The sender could not send what was asked, so nothing was lost because nothing was offered: this rate, and
			// any above it, say nothing about the link. The ramp ends at the last rate that was really delivered.
			break
		}
		clean, hasClean = rate, true
		if rate >= c.UDP.Max {
			break
		}
		rate = min(rate*2, c.UDP.Max)
	}
	c.Meter.ObserveUDPRamp(p.ID, t.dir, t.dscp, steps, clean, hasClean, saturated)
	return outcome{}
}

// invoke runs one iperf3 client invocation towards the peer, trying its addresses (the one that answered last first), and
// turns what happened into a result or an outcome. extra are the arguments that make it the wanted test.
func (c *Capacity) invoke(ctx context.Context, p Peer, t capTest, extra []string) (IperfResult, outcome) {
	addrs := c.candidates(p)
	if len(addrs) == 0 {
		return IperfResult{}, outcome{fail: "unreachable"}
	}
	var reason string
	sawBusy := false
	for _, a := range addrs {
		res, why := c.once(ctx, a, t, extra)
		switch why {
		case "":
			c.mu.Lock()
			if c.good == nil {
				c.good = map[string]string{}
			}
			c.good[p.ID] = a
			c.mu.Unlock()
			return res, outcome{}
		case "busy":
			sawBusy = true // another node may be free: every node runs its own server
			continue
		}
		reason = why
		if why == "auth" || ctx.Err() != nil {
			break
		}
	}
	if sawBusy && (reason == "" || reason == "timeout" || reason == "refused" || reason == "unreachable") {
		return IperfResult{}, outcome{skip: "server_busy", retry: time.Duration(60+rand.IntN(120)) * time.Second}
	}
	c.Log.Debug("throughput test failed", "peer", p.ID, "test", t.kind, "direction", t.dir, "reason", reason)
	return IperfResult{}, outcome{fail: reason}
}

func (c *Capacity) candidates(p Peer) []string {
	c.mu.Lock()
	last := c.good[p.ID]
	c.mu.Unlock()
	return orderAddresses(p, last, 5)
}

// once runs the client once towards addr.
func (c *Capacity) once(ctx context.Context, addr string, t capTest, extra []string) (IperfResult, string) {
	ip, err := netip.ParseAddr(addr)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
		return IperfResult{}, "other"
	}
	args := []string{"-c", ip.String(), "-p", strconv.Itoa(c.Port), "-J", "-i", "0", "--connect-timeout", "5000"}
	if t.dir == Reverse {
		args = append(args, "-R")
	}
	if t.dscp > 0 {
		args = append(args, "--dscp", strconv.Itoa(t.dscp))
	}
	var env []string
	if c.User != "" {
		args = append(args, "--username", c.User, "--rsa-public-key-path", c.KeyFile)
		env = append(env, "IPERF3_PASSWORD="+c.password)
	}
	args = append(args, extra...)
	release, err := c.Gate.Acquire(ctx)
	if err != nil {
		return IperfResult{}, "timeout"
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, c.Duration+c.Omit+c.UDP.Step+30*time.Second)
	defer cancel()
	out, errOut, err := c.Exec(cctx, env, c.Bin, args...)
	// iperf3 prints its JSON, error included, even when it exits 1.
	res, perr := ParseIperf(bytes.NewReader(out), t.dir)
	if perr == nil {
		return res, ""
	}
	var ie *IperfError
	switch {
	case errors.As(perr, &ie):
		return IperfResult{}, classifyIperf(ie.Msg, false)
	case err != nil:
		return IperfResult{}, classifyIperf(string(errOut)+string(out), cctx.Err() != nil)
	}
	return IperfResult{}, "other"
}

func classifyIperf(msg string, timedOut bool) string {
	if strings.Contains(strings.ToLower(msg), "server is busy") {
		return "busy"
	}
	return classify(msg, timedOut)
}

func secs(d time.Duration) string { return strconv.Itoa(int(d.Seconds())) }

// estimate is what a test is expected to carry, for the budget: what the same test carried last time, or, before there is
// any, a guess from the rate cap (or 1 Gbit/s).
func (c *Capacity) estimate(p Peer, t capTest) uint64 {
	if t.kind == "udp-ramp" {
		var sum uint64
		for r := c.UDP.Start; ; r = min(r*2, c.UDP.Max) {
			sum += r * uint64(c.UDP.Step.Seconds()) / 8
			if r >= c.UDP.Max {
				return sum
			}
		}
	}
	c.mu.Lock()
	last := c.lastBytes[t.key(p.ID)]
	c.mu.Unlock()
	if last > 0 {
		return last
	}
	rate := uint64(1e9)
	if c.TCPMaxRate > 0 {
		rate = c.TCPMaxRate
		if t.kind == "tcp-parallel" {
			rate *= uint64(c.Parallel)
		}
	}
	est := rate * uint64((c.Duration + c.Omit).Seconds()) / 8
	if t.kind == "tcp-bidir" {
		est *= 2
	}
	return est
}

// warnOversized says once that a test can never run because it would not fit the budget even when the window is empty.
func (c *Capacity) warnOversized(p Peer, t capTest, est uint64) {
	limit := c.ledger.Limit
	if pl := c.pairLedger(p); pl != nil && (limit == 0 || pl.Limit < limit) {
		limit = pl.Limit
	}
	if limit == 0 || est <= limit {
		return
	}
	k := t.key(p.ID)
	c.mu.Lock()
	if c.warned == nil {
		c.warned = map[string]bool{}
	}
	done := c.warned[k]
	c.warned[k] = true
	c.mu.Unlock()
	if !done {
		c.Log.Warn("a throughput test is larger than the whole byte budget and will never run; raise the budget or cap the rate", "peer", p.ID, "test", t.kind, "direction", t.dir, "expected_bytes", est, "budget", limit)
	}
}

// pairLedger is the daily budget of one peer, or nil when the server set none for it. It is kept between calls so that
// what was carried stays counted; a changed limit takes effect at once.
func (c *Capacity) pairLedger(p Peer) *Ledger {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.ThroughputBudget == 0 {
		delete(c.pairs, p.ID)
		return nil
	}
	if c.pairs == nil {
		c.pairs = map[string]*Ledger{}
	}
	l := c.pairs[p.ID]
	if l == nil {
		l = &Ledger{Window: c.BudgetWindow}
		c.pairs[p.ID] = l
	}
	l.mu.Lock()
	l.Limit = p.ThroughputBudget
	l.mu.Unlock()
	return l
}

// allows says whether a test expected to carry est bytes fits both the cluster's budget and the peer's own.
func (c *Capacity) allows(p Peer, now time.Time, est uint64) bool {
	if !c.ledger.Allows(now, est) {
		return false
	}
	if pl := c.pairLedger(p); pl != nil {
		return pl.Allows(now, est)
	}
	return true
}

func (c *Capacity) charge(p Peer, now time.Time, n uint64) {
	c.ledger.Charge(now, n)
	if pl := c.pairLedger(p); pl != nil {
		pl.Charge(now, n)
	}
}

// busy says whether the link is carrying real traffic in a direction this test would load.
func (c *Capacity) busy(p Peer, t capTest, now time.Time) bool {
	if c.Load == nil || (c.BusyFraction == 0 && c.SkipAbove == 0) {
		return false
	}
	lr, ok := c.Load.Rate(p.ID, now, 3*time.Minute)
	if !ok {
		return false
	}
	dirs := []Direction{t.dir}
	if t.kind == "tcp-bidir" {
		dirs = []Direction{Forward, Reverse}
	}
	for _, d := range dirs {
		rate := lr.Sent
		if d == Reverse {
			rate = lr.Received
		}
		if c.SkipAbove > 0 && rate > float64(c.SkipAbove) {
			return true
		}
		if r := c.reached(p.ID, d); c.BusyFraction > 0 && r > 0 && rate > c.BusyFraction*r {
			return true
		}
	}
	return false
}

// orderAddresses are a peer's addresses to try, the one that answered last first and the others in random order, at most max.
func orderAddresses(p Peer, last string, max int) []string {
	var out, rest []string
	for _, a := range p.Addresses {
		if s := a.String(); s == last {
			out = append(out, s)
		} else {
			rest = append(rest, s)
		}
	}
	rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	if len(rest) > max {
		rest = rest[:max]
	}
	return append(out, rest...)
}
