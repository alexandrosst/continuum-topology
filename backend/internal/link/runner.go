package link

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExecFunc runs a program and returns what it printed on stdout and on stderr. It exists so tests can stand in for owping.
type ExecFunc func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

func osExec(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return capBytes(out.Bytes()), capBytes(errb.Bytes()), err
}

func capBytes(b []byte) []byte {
	if len(b) > 1<<20 {
		return b[:1<<20]
	}
	return b
}

// Runner measures, in each direction and one at a time, the loss and delay of the path to every peer cluster with owping
// (the OWAMP client from perfSONAR), against the responder every cluster runs. It runs a short test per peer and
// direction every Every, which keeps the load on a link to a few packets per second for a few seconds.
//
// Only one test runs at a time, anywhere in the process: the extra load of probing must not grow with the number of
// peers, and the responder serves one session per client at a time.
type Runner struct {
	Meter *Meter
	Log   *slog.Logger

	// Bin is the owping program. Port is the responder's control port (OWAMP's is 861).
	Bin  string
	Port int
	// Packets are sent per test, Spacing apart (on average, as OWAMP spaces them at random). Every is how often a peer is
	// measured. Timeout is how long a lost packet is waited for.
	Packets int
	Spacing time.Duration
	Every   time.Duration
	Timeout time.Duration
	// DSCP, if not zero, marks the test packets.
	DSCP int

	Exec ExecFunc

	// PathEvery is how often the route to each peer is traced and its MTU found (default 10 minutes; negative: never). Path,
	// if set, replaces the real prober (for tests).
	PathEvery time.Duration
	Path      PathProbeFunc

	// Gate, if set, is shared with whatever else uses the link (the throughput tests), so a test never runs beside this one.
	Gate *Gate

	mu       sync.Mutex
	good     map[string]string // peer -> the address that last answered
	lastPath map[string]time.Time
	down     map[string]*peerDown // peer -> how it has been failing, for peers whose last round failed at the network level
}

// peerDown is a peer that could not be reached in its last round(s). Instead of retrying it every round, which would hold
// up every other peer for as long as its attempts take to time out, it is skipped for a growing number of rounds.
type peerDown struct {
	fails  int    // consecutive rounds that failed
	skip   int    // rounds still to be skipped
	reason string // why the last one failed
}

const (
	// maxSlowFailures is how many attempts that ended in silence (a black-holed port costs a whole timeout each) are made
	// towards one peer in one direction before giving up on its remaining addresses for the round.
	maxSlowFailures = 3
	// maxBackoff is the longest a failing peer is left alone.
	maxBackoff = 10 * time.Minute
)

// Defaults fills what was left unset.
func (r *Runner) Defaults() {
	if r.Bin == "" {
		r.Bin = "owping"
	}
	if r.Port == 0 {
		r.Port = 861
	}
	if r.Packets == 0 {
		r.Packets = 100
	}
	if r.Spacing == 0 {
		r.Spacing = 100 * time.Millisecond
	}
	if r.Every == 0 {
		r.Every = time.Minute
	}
	if r.Timeout == 0 {
		r.Timeout = 2 * time.Second
	}
	if r.PathEvery == 0 {
		r.PathEvery = 10 * time.Minute
	}
	if r.Exec == nil {
		r.Exec = osExec
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
}

// Validate checks the settings so a bad value fails at start-up, not on the first test.
func (r *Runner) Validate() error {
	if r.Packets < 10 || r.Packets > 10000 {
		return fmt.Errorf("packets per test must be between 10 and 10000, got %d", r.Packets)
	}
	if r.Spacing < 10*time.Millisecond || r.Spacing > 5*time.Second {
		return fmt.Errorf("packet spacing must be between 10ms and 5s, got %s", r.Spacing)
	}
	if r.Every < 10*time.Second {
		return fmt.Errorf("measuring more often than every 10s is not supported, got %s", r.Every)
	}
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("invalid responder port %d", r.Port)
	}
	if r.PathEvery > 0 && r.PathEvery < time.Minute {
		return fmt.Errorf("tracing the route more often than every minute is not supported, got %s", r.PathEvery)
	}
	if r.DSCP < 0 || r.DSCP > 63 {
		return fmt.Errorf("invalid DSCP %d", r.DSCP)
	}
	// A test in each direction must fit in the interval, or the schedule can never keep up. The whole round is not checked,
	// as the peer list changes while the prober runs; Run says so when a round overruns.
	if each := r.testTime(); each*2 > r.Every {
		return fmt.Errorf("a test takes about %s each way, which does not fit in an interval of %s", each.Round(time.Second), r.Every)
	}
	return nil
}

func (r *Runner) testTime() time.Duration {
	return time.Duration(r.Packets)*r.Spacing + r.Timeout + 3*time.Second
}

// maxTime is how long a test may take before it is killed. OWAMP spaces packets at random (exponentially), so the time to
// send them has a standard deviation of sqrt(Packets)*Spacing around its mean; five of those keep a healthy test from being
// killed even with few packets and wide spacing.
func (r *Runner) maxTime() time.Duration {
	spread := 5 * math.Sqrt(float64(r.Packets)) * float64(r.Spacing)
	return r.testTime() + 10*time.Second + time.Duration(spread)
}

// Run measures until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	r.Defaults()
	if err := r.Validate(); err != nil {
		r.Log.Error("link probe not started", "err", err)
		return
	}
	// A random start keeps many clusters from probing the same peer in the same second after a restart.
	if !sleep(ctx, time.Duration(rand.Int64N(int64(r.Every/4)+1))) {
		return
	}
	for {
		started := time.Now()
		r.Round(ctx)
		took := time.Since(started)
		if took > r.Every && ctx.Err() == nil {
			r.Log.Warn("link probe round took longer than the interval", "took", took.Round(time.Second), "interval", r.Every)
		}
		// Never start the next round at once: the gate would not be free for the throughput tests between two rounds.
		wait := max(r.Every-took, r.Every/10)
		if !sleep(ctx, wait) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Round measures every peer once, in both directions, except those the other cluster of the pair measures.
func (r *Runner) Round(ctx context.Context) {
	r.Defaults()
	for _, p := range r.Meter.Peers.List() {
		if p.NoProbe {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		// A peer the other cluster measures is only traced from here: the route can differ by direction.
		if p.Probed() {
			r.measurePeer(ctx, p)
		}
		r.maybePath(ctx, p)
	}
}

// measurePeer measures a peer in both directions. A peer that cannot be reached at the network level costs a lot of time
// (every address may have to time out), and all the others wait behind it, so then the other direction is not tried, and the
// peer is skipped for a growing number of rounds. Skipped runs are still counted as failures, so the counters keep the
// rhythm of the rounds.
func (r *Runner) measurePeer(ctx context.Context, p Peer) {
	if reason, skipped := r.skipDown(p.ID); skipped {
		for _, d := range directions {
			r.Meter.ProbeFailed(p.ID, d, reason)
		}
		return
	}
	fwd := r.measure(ctx, p, Forward)
	if ctx.Err() != nil {
		return
	}
	if unreachable(fwd) {
		r.Meter.ProbeFailed(p.ID, Reverse, fwd)
		r.markDown(p.ID, fwd)
		return
	}
	rev := r.measure(ctx, p, Reverse)
	switch {
	case ctx.Err() != nil:
	case fwd == "" || rev == "":
		r.markUp(p.ID)
	case unreachable(rev):
		r.markDown(p.ID, rev)
	}
}

// unreachable says a failure reason means the peer could not be talked to, as opposed to answering and refusing.
func unreachable(reason string) bool {
	return reason == "timeout" || reason == "unreachable" || reason == "refused"
}

func (r *Runner) skipDown(peer string) (reason string, skipped bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.down[peer]
	if d == nil || d.skip == 0 {
		return "", false
	}
	d.skip--
	return d.reason, true
}

// markDown notes a failed round: the peer is then skipped for 1, 3, 7... rounds, up to maxBackoff worth.
func (r *Runner) markDown(peer, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down == nil {
		r.down = map[string]*peerDown{}
	}
	d := r.down[peer]
	if d == nil {
		d = &peerDown{}
		r.down[peer] = d
	}
	d.fails++
	d.reason = reason
	limit := max(int(maxBackoff/r.Every), 1)
	d.skip = limit
	if d.fails < 30 {
		d.skip = min(1<<(d.fails-1)-1, limit)
	}
}

func (r *Runner) markUp(peer string) {
	r.mu.Lock()
	delete(r.down, peer)
	r.mu.Unlock()
}

// maybePath traces the route to a peer if it is due. The target is the first address of the order every active measurement
// follows (see traceTarget): the one the real traffic is seen using, or else the one that answered the OWAMP test, so the
// route described is the one that matters. A pair pinned to a kind of address the peer does not have fails the trace.
func (r *Runner) maybePath(ctx context.Context, p Peer) {
	if r.PathEvery < 0 || ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	due := time.Since(r.lastPath[p.ID]) >= r.PathEvery
	last := r.good[p.ID]
	r.mu.Unlock()
	if !due {
		return
	}
	if noAddress(p) {
		r.Meter.PathFailed(p.ID)
		r.Log.Debug("link path probe failed", "peer", p.ID, "reason", ReasonNoKind, "via", p.Via)
		return
	}
	dst, err := netip.ParseAddr(traceTarget(p, last))
	if err != nil {
		return
	}
	// The trace takes its turn like every other measurement: its probes would be delayed or dropped beside a throughput test,
	// and would load the link during one. The wait does not count against the trace's own time.
	release, err := r.Gate.Acquire(ctx)
	if err != nil {
		return
	}
	defer release()
	r.mu.Lock()
	if r.lastPath == nil {
		r.lastPath = map[string]time.Time{}
	}
	r.lastPath[p.ID] = time.Now()
	r.mu.Unlock()
	probe := r.Path
	if probe == nil {
		probe = PathProber{SrcPort: srcPortFor(p.ID)}.Probe
	}
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := probe(pctx, dst)
	switch {
	case errors.Is(err, ErrPathUnsupported):
	case err != nil:
		r.Meter.PathFailed(p.ID)
		r.Log.Debug("link path probe failed", "peer", p.ID, "err", err)
	default:
		r.Meter.ObservePath(p.ID, res)
	}
}

// srcPortFor gives each peer its own fixed local port in 40000-49999, so that routers which balance load by flow send each
// probe of the same peer the same way and a route change is a real one.
func srcPortFor(peer string) int {
	h := uint32(2166136261)
	for i := 0; i < len(peer); i++ {
		h = (h ^ uint32(peer[i])) * 16777619
	}
	return 40000 + int(h%10000)
}

// measure runs one direction towards a peer and returns why it failed, or "" if it was recorded.
func (r *Runner) measure(ctx context.Context, p Peer, d Direction) string {
	addrs := r.candidates(p)
	if len(addrs) == 0 {
		if noAddress(p) {
			r.Meter.ProbeFailed(p.ID, d, ReasonNoKind)
			r.Log.Debug("link probe failed", "peer", p.ID, "direction", d, "reason", ReasonNoKind, "via", p.Via)
			return ReasonNoKind
		}
		return ""
	}
	var reason string
	slow := 0
	for _, a := range addrs {
		res, why := r.once(ctx, a, d)
		if why == "" {
			r.mu.Lock()
			if r.good == nil {
				r.good = map[string]string{}
			}
			r.good[p.ID] = a
			r.mu.Unlock()
			r.Meter.ObserveProbe(p.ID, d, res)
			return ""
		}
		reason = why
		if why == "auth" || ctx.Err() != nil {
			break // another address of the same cluster will not change the answer
		}
		// Silence can cost a whole timeout per address; a few of them are enough to know the peer is not answering.
		if why == "timeout" {
			if slow++; slow >= maxSlowFailures {
				break
			}
		}
	}
	r.Meter.ProbeFailed(p.ID, d, reason)
	r.Log.Debug("link probe failed", "peer", p.ID, "direction", d, "reason", reason)
	return reason
}

// candidates are the peer's addresses to try (see orderAddresses): those the traffic uses first, then the one that answered
// last time. A cluster has many node addresses, and not every one need run the responder, so a failure moves on to the next
// rather than counting as a link failure.
// At most five, and measure gives up after three that time out, so an unreachable cluster costs about a minute and a half
// a direction at most, and then it is left alone for a while (see measurePeer).
func (r *Runner) candidates(p Peer) []string {
	r.mu.Lock()
	last := r.good[p.ID]
	r.mu.Unlock()
	return orderAddresses(p, last, 5)
}

// once runs one test towards addr and returns the result, or why it failed.
func (r *Runner) once(ctx context.Context, addr string, d Direction) (OwampResult, string) {
	ip, err := netip.ParseAddr(addr)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
		return OwampResult{}, "other"
	}
	args := []string{"-M", "-c", strconv.Itoa(r.Packets), "-i", fmt.Sprintf("%.3f", r.Spacing.Seconds()), "-L", fmt.Sprintf("%.1f", r.Timeout.Seconds())}
	if d == Forward {
		args = append(args, "-t")
	} else {
		args = append(args, "-f")
	}
	args = append(args, "-A", "O")
	if r.DSCP > 0 {
		args = append(args, "-D", strconv.Itoa(r.DSCP))
	}
	args = append(args, net.JoinHostPort(ip.String(), strconv.Itoa(r.Port)))
	release, err := r.Gate.Acquire(ctx)
	if err != nil {
		return OwampResult{}, "timeout"
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, r.maxTime())
	defer cancel()
	out, errOut, err := r.Exec(cctx, r.Bin, args...)
	if err != nil {
		return OwampResult{}, classify(string(errOut)+string(out), cctx.Err() != nil)
	}
	res, perr := ParseOwping(bytes.NewReader(out))
	if perr != nil {
		return OwampResult{}, classify(string(errOut), false)
	}
	// owping sends exactly the packets it was asked to, so anything else is not a measurement of this run.
	if res.Sent == 0 || res.Sent > uint64(r.Packets) || res.Duplicates > res.Sent {
		return OwampResult{}, "other"
	}
	return res, ""
}

// classify turns the program's complaint into a failure reason.
func classify(msg string, timedOut bool) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "auth") || strings.Contains(m, "unknown user") || strings.Contains(m, "passphrase"):
		return "auth"
	case strings.Contains(m, "refused"):
		return "refused"
	case strings.Contains(m, "unreachable") || strings.Contains(m, "no route"):
		return "unreachable"
	case timedOut || strings.Contains(m, "timed out") || strings.Contains(m, "timeout") || strings.Contains(m, "unable to open control connection"):
		return "timeout"
	}
	return "other"
}
