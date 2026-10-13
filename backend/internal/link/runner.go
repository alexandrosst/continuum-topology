package link

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// User and KeyFile select authenticated mode when both are set: the responder then refuses anyone without the key.
	// Otherwise the open mode is used, which is only for networks where the responder's port is already restricted.
	User, KeyFile string
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
}

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
	if (r.User == "") != (r.KeyFile == "") {
		return errors.New("authenticated mode needs both a user and a key file")
	}
	if r.PathEvery > 0 && r.PathEvery < time.Minute {
		return fmt.Errorf("tracing the route more often than every minute is not supported, got %s", r.PathEvery)
	}
	if r.DSCP < 0 || r.DSCP > 63 {
		return fmt.Errorf("invalid DSCP %d", r.DSCP)
	}
	// A test, both directions, for every peer must fit in the interval, or the schedule can never keep up.
	if each := r.testTime(); each*2 > r.Every {
		return fmt.Errorf("a test takes about %s each way, which does not fit in an interval of %s", each.Round(time.Second), r.Every)
	}
	return nil
}

func (r *Runner) testTime() time.Duration {
	return time.Duration(r.Packets)*r.Spacing + r.Timeout + 3*time.Second
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
		wait := r.Every - time.Since(started)
		if wait < 0 {
			wait = 0
		}
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

// Round measures every peer once, in both directions.
func (r *Runner) Round(ctx context.Context) {
	r.Defaults()
	for _, p := range r.Meter.Peers.List() {
		if p.NoProbe {
			continue
		}
		for _, d := range directions {
			if ctx.Err() != nil {
				return
			}
			r.measure(ctx, p, d)
		}
		r.maybePath(ctx, p)
	}
}

// maybePath traces the route to a peer if it is due. The target is the address that answered the OWAMP test, so the route
// described is the one the test packets took; before any has, the peer's first address.
func (r *Runner) maybePath(ctx context.Context, p Peer) {
	if r.PathEvery < 0 || ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	due := time.Since(r.lastPath[p.ID]) >= r.PathEvery
	target := r.good[p.ID]
	r.mu.Unlock()
	if !due {
		return
	}
	if target == "" && len(p.Addresses) > 0 {
		target = p.Addresses[0].String()
	}
	dst, err := netip.ParseAddr(target)
	if err != nil {
		return
	}
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

func (r *Runner) measure(ctx context.Context, p Peer, d Direction) {
	addrs := r.candidates(p)
	if len(addrs) == 0 {
		return
	}
	var reason string
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
			return
		}
		reason = why
		if why == "auth" || ctx.Err() != nil {
			break // another address of the same cluster will not change the answer
		}
	}
	r.Meter.ProbeFailed(p.ID, d, reason)
	r.Log.Debug("link probe failed", "peer", p.ID, "direction", d, "reason", reason)
}

// candidates are the peer's addresses to try, the one that answered last time first. A cluster has many node addresses,
// and not every one need run the responder, so a failure moves on to the next rather than counting as a link failure.
// At most five: an unreachable cluster must not hold the one-at-a-time probe for minutes.
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
	if r.User != "" {
		args = append(args, "-A", "A", "-u", r.User, "-k", r.KeyFile)
	} else {
		args = append(args, "-A", "O")
	}
	if r.DSCP > 0 {
		args = append(args, "-D", strconv.Itoa(r.DSCP))
	}
	args = append(args, net.JoinHostPort(ip.String(), strconv.Itoa(r.Port)))
	release, err := r.Gate.Acquire(ctx)
	if err != nil {
		return OwampResult{}, "timeout"
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, r.testTime()+10*time.Second)
	defer cancel()
	out, errOut, err := r.Exec(cctx, r.Bin, args...)
	if err != nil {
		return OwampResult{}, classify(string(errOut)+string(out), cctx.Err() != nil)
	}
	res, perr := ParseOwping(bytes.NewReader(out))
	if perr != nil {
		return OwampResult{}, classify(string(errOut), false)
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
