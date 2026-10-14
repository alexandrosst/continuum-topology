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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExecFunc runs a program and returns what it printed on stdout and on stderr. It exists so tests can stand in for owping.
type ExecFunc func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

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
	// Source, if set, replaces the lookup of the local address the system uses to reach an address (for tests).
	Source func(netip.Addr) (netip.Addr, bool)

	// PathEvery is how often the route to each peer is traced and its MTU found (default 10 minutes; negative: never). Path,
	// if set, replaces the real prober (for tests).
	PathEvery time.Duration
	Path      PathProbeFunc

	// Gate, if set, is shared with whatever else uses the link (the throughput tests), so a test never runs beside this one.
	Gate *Gate

	// Now, if set, replaces the clock (for tests).
	Now func() time.Time

	mu       sync.Mutex
	good     map[string]string // peer -> the address that last answered with packets arriving
	lastPath map[string]time.Time
	reached  map[string]string    // peer -> the address its route trace last reached, which the trace then keeps to
	noTrace  map[string]bool      // peers whose route cannot be traced from here (only IPv6 addresses), already counted
	down     map[string]*peerDown // peer -> how it has been failing, for peers whose last round failed at the network level
	state    map[string]string    // peer and direction -> what the log last said about it, so only a change is logged
	absent   map[string]time.Time // peer -> since when it has not been in the peer list, for the state above
	loadSig  string               // the peers the last round had to go through, to say once when that set changes
	loadSeen bool
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
	// maxAfterAllLost is how many more addresses are tried once one has completed a run in which every packet was lost. A
	// run costs about a test time (15 s with the defaults), so more would not fit in the interval.
	maxAfterAllLost = 1
	// runnerStateGrace is how long a peer must be missing from the peer list before what the runner remembers about it (its
	// back-off, the address that answered, the log state) is dropped. The list can be empty or short for a moment (the
	// agent restarting, the server not answering) and forgetting at once would restart every back-off.
	runnerStateGrace = 30 * time.Minute
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
			// Said once when the set of peers changes (see noteLoad), not every round.
			r.Log.Debug("link probe round took longer than the interval", "took", took.Round(time.Second), "interval", r.Every)
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
	peers := r.Meter.Peers.List()
	r.prune(peers)
	r.noteLoad(peers)
	for _, p := range peers {
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

// noteLoad says, when the set of peers this cluster probes changes, how long a round over them takes if it takes longer than
// the interval. Peers are measured one after another (only one test runs at a time anywhere), each in two directions, so the
// time between two measurements of a peer is about the peers times two tests, whatever the interval says. Validate only
// checks that one peer fits. A round that overruns is normal then, so it is not logged each time.
func (r *Runner) noteLoad(peers []Peer) {
	var ids []string
	for _, p := range peers {
		if p.Probed() {
			ids = append(ids, p.ID)
		}
	}
	slices.Sort(ids)
	sig := strings.Join(ids, ",")
	r.mu.Lock()
	changed := !r.loadSeen || sig != r.loadSig
	r.loadSig, r.loadSeen = sig, true
	r.mu.Unlock()
	round := time.Duration(len(ids)) * 2 * r.testTime()
	if !changed || round <= r.Every {
		return
	}
	r.Log.Warn("link probe rounds take longer than the interval with this many peers, so each peer is measured less often than the interval says",
		"peers", len(ids), "interval", r.Every, "round", round.Round(time.Second), "effective_interval", (round + r.Every/10).Round(time.Second),
		"advice", "lower probe.packets or raise probe.interval so a round fits, or accept the longer interval; the figures are as valid, only older")
}

// prune drops what is remembered about peers that have been missing from the list for runnerStateGrace. Run once per round.
func (r *Runner) prune(peers []Peer) {
	now := r.clock()
	present := make(map[string]bool, len(peers))
	for _, p := range peers {
		present[p.ID] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	known := map[string]bool{}
	for id := range r.down {
		known[id] = true
	}
	for id := range r.good {
		known[id] = true
	}
	for id := range r.lastPath {
		known[id] = true
	}
	for id := range r.reached {
		known[id] = true
	}
	for id := range r.noTrace {
		known[id] = true
	}
	for k := range r.state {
		id, _, _ := strings.Cut(k, "\x00")
		known[id] = true
	}
	for id := range r.absent {
		if present[id] || !known[id] {
			delete(r.absent, id)
		}
	}
	for id := range known {
		if present[id] {
			continue
		}
		if r.absent == nil {
			r.absent = map[string]time.Time{}
		}
		since, ok := r.absent[id]
		if !ok {
			r.absent[id] = now
			continue
		}
		if now.Sub(since) < runnerStateGrace {
			continue
		}
		delete(r.down, id)
		delete(r.good, id)
		delete(r.lastPath, id)
		delete(r.reached, id)
		delete(r.noTrace, id)
		delete(r.absent, id)
		for _, d := range directions {
			delete(r.state, probeKey(id, d))
		}
	}
}

func (r *Runner) clock() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
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

// markDown notes a failed round: the first failure skips nothing, the following ones skip 1, 3, 7, 15... rounds, up to
// maxBackoff worth.
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

// maybePath traces the route to a peer if it is due. The target is the first address of pathTarget's order: the one the trace
// last reached, else the one the real traffic is seen using, else the one that answered the OWAMP test, else the best
// class of address of the peer's list, so the route described is the one that matters and does not jump between targets. A
// pair pinned to a kind of address the peer does not have fails the trace.
func (r *Runner) maybePath(ctx context.Context, p Peer) {
	if r.PathEvery < 0 || ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	due := r.clock().Sub(r.lastPath[p.ID]) >= r.PathEvery
	last := r.good[p.ID]
	reached := r.reached[p.ID]
	r.mu.Unlock()
	if !due {
		return
	}
	if noAddress(p) {
		r.Meter.PathFailed(p.ID)
		r.Log.Debug("link path probe failed", "peer", p.ID, "reason", ReasonNoKind, "via", p.Via)
		return
	}
	target, untraceable := pathTarget(p, reached, last)
	if target == "" {
		if untraceable {
			r.noteUntraceable(p.ID)
		}
		return
	}
	dst, err := netip.ParseAddr(target)
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
	r.lastPath[p.ID] = r.clock()
	delete(r.noTrace, p.ID)
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
		if res.Reached {
			r.mu.Lock()
			if r.reached == nil {
				r.reached = map[string]string{}
			}
			r.reached[p.ID] = target
			r.mu.Unlock()
		}
	}
}

// noteUntraceable counts, once, and logs that a peer has only addresses the route trace cannot use (IPv6, until it has been
// tested), so its path series do not just stay absent without a word. No gate is taken and nothing is sent.
func (r *Runner) noteUntraceable(peer string) {
	r.mu.Lock()
	done := r.noTrace[peer]
	if r.noTrace == nil {
		r.noTrace = map[string]bool{}
	}
	r.noTrace[peer] = true
	r.mu.Unlock()
	if done {
		return
	}
	r.Meter.PathFailed(peer)
	r.Log.Info("the route to a peer is not traced: all its addresses are IPv6 and the route trace supports IPv4 only", "peer", peer)
}

// traceable says the route trace can be run towards a.
func traceable(a netip.Addr) bool { return a.Unmap().Is4() || pathIPv6Tested }

// classRank orders the kinds of address by how likely they are the path between the clusters that matters: a mesh address
// is what the clusters were joined by, a public one is what they reach each other at without it, a private one is usually
// the network inside a site (and, between two clusters that both use the same range, not even a path to the other one).
func classRank(a netip.Addr) int {
	switch Class(a) {
	case ClassMesh:
		return 0
	case ClassPublic:
		return 1
	case ClassPrivate:
		return 2
	}
	return 3
}

// pathTarget is the address the route to p is traced towards, and, when there is none, whether that is because every address
// of the peer is one the trace cannot use. It is the address the last trace reached (so the target does not change from one
// trace to the next and a route change is a real one, and a trace is not moved because one answer was dropped), else the
// first of the order every active measurement follows: the addresses the traffic is seen using, the one that answered the
// one-way probe (answered), then the rest by kind and address. The second cluster of a pair makes no one-way test, so it has
// no answered address, and the order of the server's list (by text, so 10.x before 100.64.x) must not decide for it.
func pathTarget(p Peer, reached, answered string) (target string, untraceable bool) {
	preferred, ans, rest := rankAddresses(p, answered)
	slices.SortFunc(rest, func(a, b string) int {
		aa, _ := netip.ParseAddr(a)
		bb, _ := netip.ParseAddr(b)
		if c := classRank(aa) - classRank(bb); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	order := append(append([]string{}, preferred...), ans)
	order = append(order, rest...)
	if reached != "" && slices.Contains(order, reached) {
		order = append([]string{reached}, order...)
	}
	for _, s := range order {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		if traceable(a) {
			return s, false
		}
		untraceable = true
	}
	return "", untraceable
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

// lostRun is a completed run in which every packet was lost.
type lostRun struct {
	addr string
	res  OwampResult
	note string
}

// measure runs one direction towards a peer and returns why it failed, or "" if it was recorded.
//
// A run in which every packet was lost proves that the control connection works, not that the path does: a filter on the test
// ports, a receiver that drops them or an address that is not the one the traffic takes give exactly this, while another
// address of the peer may work. Such a run is therefore not taken as the answer: the address is not remembered as the one that
// answers, and up to maxAfterAllLost more addresses are tried. If none receives anything, the first all-lost run is recorded
// (probe_up 1, loss ratio 1), so a link that is really down stays visible instead of vanishing.
func (r *Runner) measure(ctx context.Context, p Peer, d Direction) string {
	addrs := r.candidates(p)
	if len(addrs) == 0 {
		if noAddress(p) {
			r.Meter.ProbeFailed(p.ID, d, ReasonNoKind)
			r.logFailure(p.ID, d, ReasonNoKind, "")
			return ReasonNoKind
		}
		return ""
	}
	r.mu.Lock()
	good := r.good[p.ID]
	r.mu.Unlock()
	var (
		reason, note string
		lost         *lostRun
		slow, extra  int
		spent        bool
	)
	tried := map[string]bool{}
	// try runs one test and says whether the direction is settled (a run with packets arriving was recorded) and, if the test
	// failed, why.
	try := func(a string) (settled bool, why string) {
		tried[a] = true
		res, why, n := r.once(ctx, a, d)
		switch {
		case why != "":
			reason, note = why, n
		case res.Lost >= res.Sent: // once guarantees Sent > 0
			if lost == nil {
				lost = &lostRun{addr: a, res: res, note: n}
			}
		default:
			r.mu.Lock()
			if r.good == nil {
				r.good = map[string]string{}
			}
			r.good[p.ID] = a
			r.mu.Unlock()
			r.Meter.ObserveProbe(p.ID, d, res)
			r.logRun(p.ID, d, res, n)
			return true, ""
		}
		return false, why
	}
	for _, a := range addrs {
		if lost != nil {
			if extra >= maxAfterAllLost {
				break
			}
			extra++
		}
		settled, why := try(a)
		if settled {
			return ""
		}
		if why == "auth" || ctx.Err() != nil {
			break // another address of the same cluster will not change the answer
		}
		// Silence can cost a whole timeout per address; a few of them are enough to know the peer is not answering.
		if why == "timeout" {
			if slow++; slow >= maxSlowFailures {
				spent = true
				break
			}
		}
	}
	// The address that answered last is the best bet, and it comes after every address the traffic uses: when those are the
	// ones that stay silent, the budget above must not be spent before it was tried.
	if spent && lost == nil && ctx.Err() == nil && good != "" && !tried[good] && slices.Contains(addrs, good) {
		if settled, _ := try(good); settled {
			return ""
		}
	}
	if lost != nil {
		r.Meter.ObserveProbe(p.ID, d, lost.res)
		r.logRun(p.ID, d, lost.res, lost.note)
		return ""
	}
	r.Meter.ProbeFailed(p.ID, d, reason)
	r.logFailure(p.ID, d, reason, note)
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

// once runs one test towards addr and returns the result, or why it failed, and one line of what the program said on stderr
// (made safe to log: see stderrNote).
func (r *Runner) once(ctx context.Context, addr string, d Direction) (OwampResult, string, string) {
	ip, err := netip.ParseAddr(addr)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
		return OwampResult{}, "other", ""
	}
	args := []string{"-M", "-c", strconv.Itoa(r.Packets), "-i", fmt.Sprintf("%.3f", r.Spacing.Seconds()), "-L", fmt.Sprintf("%.1f", r.Timeout.Seconds())}
	if d == Forward {
		args = append(args, "-t")
	} else {
		args = append(args, "-f")
	}
	args = append(args, "-A", "O")
	// Bind the client to the address the system reaches the responder from. The address owping announces as the other end of a
	// test is that of its control connection (the local address of the socket, whatever -S says), and in open mode the
	// responder refuses a test whose announced address is not the one it sees the control connection come from, which only
	// happens when something rewrites addresses on the way (NAT). -S does not change that. It pins the source on a host with
	// several addresses, so the control connection leaves from the address the route to the responder uses and not from one
	// that another policy picked.
	if src, ok := r.sourceFor(ip); ok {
		args = append(args, "-S", src.String())
	}
	if r.DSCP > 0 {
		args = append(args, "-D", strconv.Itoa(r.DSCP))
	}
	args = append(args, net.JoinHostPort(ip.String(), strconv.Itoa(r.Port)))
	release, err := r.Gate.Acquire(ctx)
	if err != nil {
		return OwampResult{}, "timeout", ""
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, r.maxTime())
	defer cancel()
	out, errOut, err := r.Exec(cctx, r.Bin, args...)
	note := stderrNote(errOut, err)
	if err != nil {
		return OwampResult{}, classify(string(errOut)+string(out), cctx.Err() != nil), note
	}
	res, perr := ParseOwping(bytes.NewReader(out))
	if perr != nil {
		if errors.Is(perr, errSessionUnfinished) {
			note = strings.TrimSpace("SESSION_FINISHED 0; " + note)
		} else if note == "" {
			note = tidy(perr.Error(), maxNote)
		}
		return OwampResult{}, classify(string(errOut), false), note
	}
	// owping sends exactly the packets it was asked to, so anything else is not a measurement of this run.
	if res.Sent == 0 || res.Sent > uint64(r.Packets) || res.Duplicates > res.Sent {
		return OwampResult{}, "other", note
	}
	return res, "", note
}

// maxNote bounds the text of the program's complaint that is logged.
const maxNote = 200

// stderrNote makes what a program said on stderr fit for a log line: its non-empty lines joined, control characters
// removed, at most maxNote bytes. The text can come from the remote responder ("Server denied access: <node name>"), so it is
// only ever passed as an attribute, which the handler quotes, never as part of a message. Without any, the error from
// starting or waiting for the program says something.
func stderrNote(stderr []byte, err error) string {
	var sb strings.Builder
	for _, l := range strings.Split(string(stderr), "\n") {
		l = strings.TrimSpace(tidy(l, maxNote))
		if l == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString(" | ")
		}
		sb.WriteString(l)
		if sb.Len() >= maxNote {
			break
		}
	}
	if sb.Len() == 0 && err != nil {
		return tidy(err.Error(), maxNote)
	}
	return cutRunes(sb.String(), maxNote)
}

// logFailure logs a failed direction when it is news: the first failure, or a failure for another reason than the last one.
// The same failure again is a Debug line, so a peer that stays down does not fill the log every round.
func (r *Runner) logFailure(peer string, d Direction, reason, note string) {
	state := "failed " + reason
	if r.swapState(peer, d, state) == state {
		r.Log.Debug("link probe failed", "peer", peer, "direction", d, "reason", reason, "stderr", note)
		return
	}
	r.Log.Warn("link probe failed", "peer", peer, "direction", d, "reason", reason, "stderr", note)
}

// logRun logs a completed run when it changes what was last said about the direction: a recovery after a failure, or a run
// in which every packet was lost (the control connection worked and nothing arrived, which the counts alone do not explain).
func (r *Runner) logRun(peer string, d Direction, res OwampResult, note string) {
	if res.Lost >= res.Sent {
		if r.swapState(peer, d, "all lost") == "all lost" {
			return
		}
		r.Log.Warn("link probe run lost every packet: the responder answered but no test packet was counted",
			"peer", peer, "direction", d, "sent", res.Sent,
			"session_finished", res.Session.Finished, "start_time", res.Session.Start, "end_time", res.Session.End,
			"sync", res.Session.Sync, "max_err", res.Session.MaxErr, "loss_timeout", res.Session.LossTimeout, "stderr", note)
		return
	}
	switch prev := r.swapState(peer, d, "ok"); {
	case strings.HasPrefix(prev, "failed "):
		r.Log.Info("link probe recovered", "peer", peer, "direction", d, "was", strings.TrimPrefix(prev, "failed "))
	case prev == "all lost":
		r.Log.Info("link probe receives packets again", "peer", peer, "direction", d, "lost", res.Lost, "sent", res.Sent)
	}
}

// swapState records what was last logged about a peer's direction and returns what it was before ("" for nothing yet).
func (r *Runner) swapState(peer string, d Direction, state string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		r.state = map[string]string{}
	}
	k := probeKey(peer, d)
	prev := r.state[k]
	r.state[k] = state
	return prev
}

// sourceFor is the local address the system would send to ip from, found by connecting a UDP socket (which sends nothing).
func (r *Runner) sourceFor(ip netip.Addr) (netip.Addr, bool) {
	if r.Source != nil {
		return r.Source(ip)
	}
	return routeSource(ip)
}

// routeSource is the local address the system sends to ip from, or false if there is no route (or ip cannot be sent to).
func routeSource(ip netip.Addr) (netip.Addr, bool) {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return netip.Addr{}, false
	}
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: ip.AsSlice(), Port: 9, Zone: ip.Zone()})
	if err != nil {
		return netip.Addr{}, false
	}
	defer c.Close()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(ua.IP)
	if !ok || a.IsUnspecified() {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// classify turns the program's complaint into a failure reason. What owping prints is limited: it says only "Unable to open
// control connection" whether the connection was refused, unreachable or silent (the connect error itself is not printed), so
// a probe that cannot connect is a "timeout" and "refused" is not seen from it (the connect timing, which dials itself, tells
// them apart). A responder that answers and then refuses is told apart: its answer names the refusal.
func classify(msg string, timedOut bool) string {
	m := strings.ToLower(msg)
	switch {
	// "No Common Modes" is the answer of a responder whose policy (allowFrom, no open mode) allows the client no mode at all;
	// owping adds "Unable to open control connection" after it, which must not be read as silence.
	case strings.Contains(m, "auth") || strings.Contains(m, "unknown user") || strings.Contains(m, "passphrase") || strings.Contains(m, "no common modes"):
		return "auth"
	// The responder answered and refused the test: "Server denied test: <addr>" and "Session Failed!" (the address it sees is
	// not the one the client announced: NAT), "Test not allowed" and "Test denied: resources unavailable" (its limits), "Server
	// denied access: <node>".
	case strings.Contains(m, "server denied test") || strings.Contains(m, "session failed") || strings.Contains(m, "not allowed") ||
		strings.Contains(m, "test denied") || strings.Contains(m, "server denied access"):
		return "denied"
	case strings.Contains(m, "refused"):
		return "refused"
	case strings.Contains(m, "unreachable") || strings.Contains(m, "no route"):
		return "unreachable"
	case timedOut || strings.Contains(m, "timed out") || strings.Contains(m, "timeout") || strings.Contains(m, "unable to open control connection"):
		return "timeout"
	}
	return "other"
}
