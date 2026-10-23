package linksapi

import (
	"context"
	"math"
	"sync"
	"time"

	"ikhnos/internal/connectivity"
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/kernel"
)

// The latest figures of each watched pair, for the Sites screen. The pairs come from the organisation's settings; the numbers
// from the link series that the clusters' collectors send to FUSION (see internal/link for what they are). Nothing is stored
// here: a figure is whatever FUSION held a moment ago, and a pair FUSION has nothing for simply has no figures.

const (
	// LatestEvery is how long one read of FUSION is shared. The screen refreshes itself, and several people may have it open.
	LatestEvery        = 15 * time.Second
	linksLatestTimeout = 10 * time.Second
	// LatestStaleFor is how long the last good readings are still served, marked stale, when FUSION cannot be read. Longer
	// than that, old figures would pass for current ones.
	LatestStaleFor = 5 * time.Minute
	// linkStaleAfter is the age after which a probe that still reports "up" is called stale: it runs about once a minute, so this
	// is many missed runs. The Sites screen uses the same number (STALE_AFTER_MS).
	linkStaleAfter = 15 * time.Minute
	// linkLostAt is the loss from which a run that completed is taken to have had none of its packets arrive.
	linkLostAt = 0.99
)

// LinkPairLatest is what is known about one pair. A figure that FUSION does not hold is absent, never zero. "A to B" is from the
// pair's first cluster to its second, in the order the pair is listed.
type LinkPairLatest struct {
	A string `json:"a"`
	B string `json:"b"`
	// RTTSeconds is the time to open a TCP connection to the other cluster's responder, averaged over the last minutes.
	RTTSeconds *float64 `json:"rttSeconds,omitempty"`
	// LossAToB and LossBToA are the fraction of test packets lost in the latest probe run, in each direction.
	LossAToB *float64 `json:"lossAToB,omitempty"`
	LossBToA *float64 `json:"lossBToA,omitempty"`
	// JitterSeconds is the largest median delay variation of the latest probe runs.
	JitterSeconds *float64 `json:"jitterSeconds,omitempty"`
	// MTUBytes is the smallest path MTU either cluster learned.
	MTUBytes *int `json:"mtuBytes,omitempty"`
	// ThroughputAToB and ThroughputBToA are the TCP rates of the latest throughput tests, in bits per second.
	ThroughputAToB *float64 `json:"throughputAToB,omitempty"`
	ThroughputBToA *float64 `json:"throughputBToA,omitempty"`
	// ProbedAt is when a probe run last finished in either cluster; TestedAt when a throughput test last did.
	ProbedAt *time.Time `json:"probedAt,omitempty"`
	TestedAt *time.Time `json:"testedAt,omitempty"`
	// ProbeAToB and ProbeBToA say how the one-way probe stands in each direction. Absent where FUSION holds no state for the
	// direction (probing off, an agent that does not report it, nothing arrived yet).
	ProbeAToB *LinkProbeLatest `json:"probeAToB,omitempty"`
	ProbeBToA *LinkProbeLatest `json:"probeBToA,omitempty"`
}

// ProbeState is how the one-way probe stands in one direction of a pair.
type ProbeState string

// The states of the one-way probe. They are bounded words, not text: the screen words them, and nothing a store or a cluster said
// reaches them.
const (
	probeOK      ProbeState = "ok"      // the latest run completed
	probeFailing ProbeState = "failing" // the latest run failed; an earlier one completed
	probeNever   ProbeState = "never"   // the latest run failed and none completed within a day
)

// WireWords is the list of ProbeState's words, as the generated types give it.
func (ProbeState) WireWords() []string {
	return []string{string(probeOK), string(probeFailing), string(probeNever)}
}

// ProbeDiagnosis is a conservative reading of the evidence about a failing or doubtful probe.
type ProbeDiagnosis string

// The diagnoses the server may add to a probe.
const (
	diagUDPLost     ProbeDiagnosis = "udp-lost"     // the run completed but nearly every test packet was lost
	diagDenied      ProbeDiagnosis = "denied"       // the responder is reached but the test fails
	diagNoResponder ProbeDiagnosis = "no-responder" // nothing answers on the responder's control port
	diagFiltered    ProbeDiagnosis = "filtered"     // the control port does not answer at all
	diagOneWay      ProbeDiagnosis = "one-way"      // this direction fails and the other works
	diagStale       ProbeDiagnosis = "stale"        // the probe says up but has not completed a run for long
)

// WireWords is the list of ProbeDiagnosis' words, as the generated types give it.
func (ProbeDiagnosis) WireWords() []string {
	return []string{string(diagUDPLost), string(diagDenied), string(diagNoResponder), string(diagFiltered), string(diagOneWay), string(diagStale)}
}

// ProbeFailure is why the latest runs of a probe or a connect timing failed. What the cluster's metrics said is read as one of
// these words and as "other" when it is none of them.
type ProbeFailure string

// The reasons a probe or a connect timing fails.
const (
	failUnreachable ProbeFailure = "unreachable"
	failRefused     ProbeFailure = "refused"
	failTimeout     ProbeFailure = "timeout"
	failAuth        ProbeFailure = "auth"
	failNoSuchKind  ProbeFailure = "no_such_kind"
	failReset       ProbeFailure = "reset"
	failOther       ProbeFailure = "other"
)

// WireWords is the list of ProbeFailure's words, as the generated types give it.
func (ProbeFailure) WireWords() []string {
	return []string{string(failUnreachable), string(failRefused), string(failTimeout), string(failAuth), string(failNoSuchKind), string(failReset), string(failOther)}
}

// LinksReason is why a LinksLatestDoc has no figures.
type LinksReason string

// The reasons FUSION's figures are not given.
const (
	reasonNoFusion    LinksReason = "no-fusion"   // this server has no FUSION
	reasonUnavailable LinksReason = "unavailable" // it has one, which could not be read
)

// WireWords is the list of LinksReason's words, as the generated types give it.
func (LinksReason) WireWords() []string {
	return []string{string(reasonNoFusion), string(reasonUnavailable)}
}

// SetupReason is why the checklists of a LinksLatestDoc are not known.
type SetupReason string

// The one reason the checklists are not known.
const setupUnavailable SetupReason = "unavailable"

// WireWords is the list of SetupReason's words, as the generated types give it.
func (SetupReason) WireWords() []string { return []string{string(setupUnavailable)} }

// LinkProbeLatest is the state of the one-way probe in one direction of a pair.
type LinkProbeLatest struct {
	State ProbeState `json:"state"`
	// Reason is why the latest runs failed, only when they did.
	Reason ProbeFailure `json:"reason,omitempty"`
	// Diagnosis is a conservative reading of the evidence. Several causes can look the same from here, so a diagnosis names what the
	// evidence fits, and is absent when it fits nothing.
	Diagnosis ProbeDiagnosis `json:"diagnosis,omitempty"`
	// At is when a run in this direction last completed.
	At *time.Time `json:"at,omitempty"`
}

// LinksLatestDoc is the answer. When FUSION cannot be read, Available is false and Reason says why in a word the screen turns into
// a sentence; the pairs are then not listed.
type LinksLatestDoc struct {
	Available bool        `json:"available"`
	Reason    LinksReason `json:"reason,omitempty"`
	// Stale says the figures are the last ones read, because FUSION could not be read just now; At is then when they were read.
	Stale bool             `json:"stale,omitempty"`
	Pairs []LinkPairLatest `json:"pairs"`
	// Notes say why a pair is not measured from one of its clusters (see LinkNote), whether or not FUSION can be read.
	Notes []connectivity.LinkNote `json:"notes"`
	// Setup is, for each listed pair, the checklist of what it needs before its figures reach FUSION (see connectivity.LinkSetupSteps), whether or
	// not FUSION can be read. An agent too old to report what its install can do gives "unknown" steps, never failures.
	Setup []connectivity.LinkSetup `json:"setup"`
	// SetupReason says, when the organisation's agents could not be read, why Notes and Setup are empty: because they are
	// not known, not because nothing needs doing.
	SetupReason SetupReason `json:"setupReason,omitempty"`
	At          time.Time   `json:"at"`
}

// linkSample says what the readings handed out are: when FUSION was read for them, and whether that was not the latest attempt.
type linkSample struct {
	At    time.Time
	Stale bool
}

// readingsCache shares one read of FUSION between everyone who asks. A zero value is ready to use.
type readingsCache struct {
	// gate lets one reader in at a time. It is a channel and not a mutex so that those who wait can give up with their request.
	once sync.Once
	gate chan struct{}

	// What follows is only touched by the one holding the gate. tried is when FUSION was last asked, whether or not it answered;
	// r is what is served and at is when r was read.
	tried, at time.Time
	r         fusionapi.LinkReadings
	err       error
	stale     bool
	have      bool
	// good is the last answer that came, and goodAt when.
	good   fusionapi.LinkReadings
	goodAt time.Time
}

func (c *readingsCache) enter(ctx context.Context) error {
	c.once.Do(func() { c.gate = make(chan struct{}, 1) })
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *readingsCache) leave() { <-c.gate }

// get returns the readings, reading FUSION again when the kept ones are older than LatestEvery. One read at a time: those
// who arrive during it wait for it (and stop waiting when their request ends) and share its result. The read is not the caller's:
// it runs with a deadline of its own and survives the caller leaving, so one closed tab does not throw away a read that the next
// viewer needs. A failure is kept as long as a success would be, so a store that is down is not asked again by every refresh;
// meanwhile the last good readings are served, marked stale, for LatestStaleFor, and only after that is the failure shown.
func (c *readingsCache) get(ctx context.Context, now func() time.Time, read func(context.Context) (fusionapi.LinkReadings, error)) (fusionapi.LinkReadings, linkSample, error) {
	if err := c.enter(ctx); err != nil {
		return fusionapi.LinkReadings{}, linkSample{}, err
	}
	defer c.leave()
	if c.have && now().Sub(c.tried) < LatestEvery {
		return c.r, linkSample{At: c.at, Stale: c.stale}, c.err
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), linksLatestTimeout)
	defer cancel()
	r, err := read(rctx)
	t := now()
	c.tried, c.have = t, true
	switch {
	case err == nil:
		c.r, c.err, c.at, c.stale = r, nil, t, false
		c.good, c.goodAt = r, t
	case !c.goodAt.IsZero() && t.Sub(c.goodAt) <= LatestStaleFor:
		c.r, c.err, c.at, c.stale = c.good, nil, c.goodAt, true
	default:
		c.r, c.err, c.at, c.stale = fusionapi.LinkReadings{}, err, t, false
	}
	return c.r, linkSample{At: c.at, Stale: c.stale}, c.err
}

// WithoutHintsOfMeasuredPairs drops the "private-range" hints of the pairs that have figures: the hint says where to look when
// nothing arrives, and a pair that is being measured has no use for it.
func WithoutHintsOfMeasuredPairs(notes []connectivity.LinkNote, pairs []LinkPairLatest) []connectivity.LinkNote {
	measured := map[[2]string]bool{}
	for _, p := range pairs {
		if p.hasFigures() {
			measured[[2]string{p.A, p.B}], measured[[2]string{p.B, p.A}] = true, true
		}
	}
	out := notes[:0:0]
	for _, n := range notes {
		if n.Code == connectivity.NotePrivateRange && measured[[2]string{n.Cluster, n.Peer}] {
			continue
		}
		out = append(out, n)
	}
	return out
}

// hasFigures says whether FUSION held anything for the pair.
func (p LinkPairLatest) hasFigures() bool {
	return p.RTTSeconds != nil || p.LossAToB != nil || p.LossBToA != nil || p.JitterSeconds != nil || p.MTUBytes != nil ||
		p.ThroughputAToB != nil || p.ThroughputBToA != nil || p.ProbedAt != nil || p.TestedAt != nil
}

// fusionLinkPair is one pair in FUSION's data API: the Sites screen's figures, and whose series they are.
type fusionLinkPair struct {
	LinkPairLatest
	// MeasuredBy is the pair's clusters (a, b or both) whose series supplied the figures.
	MeasuredBy []string `json:"measuredBy"`
}

// pairReading is a pair's figures and the clusters whose series supplied them.
type pairReading struct {
	LinkPairLatest
	// By holds the pair's clusters (A, B or both, in that order) whose series supplied at least one figure.
	By []string
}

// latestPairs picks, for each listed pair, the figures about it out of everything the clusters reported. The pair's first
// cluster (the one that also runs the throughput tests) runs the one-way probe and the connect timing, so its series are the
// ones read; the second's are looked at only where the first has nothing, which is when the first cannot measure (the server
// then lets the second probe) or when an older agent still measures from both sides and the two disagree a little. The route
// trace runs on both sides, so the MTU is the smaller of the two. now is when the readings were taken, which the age of a probe
// is judged against.
func latestPairs(links []kernel.LinkSetting, rd fusionapi.LinkReadings, now time.Time) []LinkPairLatest {
	in := readPairs(links, rd, now)
	out := make([]LinkPairLatest, len(in))
	for i, p := range in {
		out[i] = p.LinkPairLatest
	}
	return out
}

// linkIdx indexes readings by cluster, peer and direction. The store may hold two series for one key (a rolled pod's last
// samples beside its successor's) and answers in no defined order, so which one is kept must not depend on it: the larger value
// wins, or the smaller where lower is set (the MTU, where the smaller is the path's).
func linkIdx(in []fusionapi.LinkReading, lower bool) map[[3]string]float64 {
	m := make(map[[3]string]float64, len(in))
	for _, x := range in {
		k := [3]string{x.Cluster, x.Peer, x.Direction}
		if old, ok := m[k]; ok && (lower && x.Value >= old || !lower && x.Value <= old) {
			continue
		}
		m[k] = x.Value
	}
	return m
}

// linkReasons indexes failure readings by key to the reason that failed most often, the first in alphabetical order on a tie, so
// the choice does not depend on the order of the store's answer either. A reason outside the known words reads as "other": the
// label is whatever the cluster's metrics said, and it is not shown as it is.
func linkReasons(in []fusionapi.LinkReading) map[[3]string]ProbeFailure {
	best := make(map[[3]string]fusionapi.LinkReading, len(in))
	for _, x := range in {
		k := [3]string{x.Cluster, x.Peer, x.Direction}
		x.Reason = string(knownFailureReason(x.Reason))
		if old, ok := best[k]; ok && (x.Value < old.Value || x.Value == old.Value && x.Reason >= old.Reason) {
			continue
		}
		best[k] = x
	}
	m := make(map[[3]string]ProbeFailure, len(best))
	for k, x := range best {
		m[k] = ProbeFailure(x.Reason)
	}
	return m
}

// knownFailureReason is r when it is one of the failure reasons the probe and the connect timing count by, else "other".
func knownFailureReason(r string) ProbeFailure {
	for _, w := range (ProbeFailure("")).WireWords() {
		if r == w {
			return ProbeFailure(r)
		}
	}
	return failOther
}

func readPairs(links []kernel.LinkSetting, rd fusionapi.LinkReadings, now time.Time) []pairReading {
	loss, jitter, rtt, tput, probed, tested, up := linkIdx(rd.Loss, false), linkIdx(rd.Jitter, false), linkIdx(rd.RTT, false), linkIdx(rd.Throughput, false), linkIdx(rd.Probed, false), linkIdx(rd.Tested, false), linkIdx(rd.Up, false)
	mtu := linkIdx(rd.MTU, true)
	probeWhy, connectWhy := linkReasons(rd.ProbeFailure), linkReasons(rd.ConnectFailure)
	out := make([]pairReading, 0, len(links))
	for _, l := range links {
		a, b := l.A, l.B
		p := pairReading{LinkPairLatest: LinkPairLatest{A: a, B: b}}
		// used notes that a figure came from a cluster's series (the first element of the key).
		used := map[string]bool{}
		// first returns the first key that has a value.
		first := func(m map[[3]string]float64, keys ...[3]string) *float64 {
			for _, k := range keys {
				if v, ok := m[k]; ok {
					used[k[0]] = true
					return &v
				}
			}
			return nil
		}
		// "forward" is from the reporting cluster to its peer, so A to B is a's forward and b's reverse.
		p.LossAToB = first(loss, [3]string{a, b, "forward"}, [3]string{b, a, "reverse"})
		p.LossBToA = first(loss, [3]string{a, b, "reverse"}, [3]string{b, a, "forward"})
		p.ThroughputAToB = first(tput, [3]string{a, b, "forward"}, [3]string{b, a, "reverse"})
		p.ThroughputBToA = first(tput, [3]string{a, b, "reverse"}, [3]string{b, a, "forward"})
		p.RTTSeconds = first(rtt, [3]string{a, b, ""}, [3]string{b, a, ""})
		var jitterFrom string
		for _, k := range [][3]string{{a, b, "forward"}, {a, b, "reverse"}, {b, a, "forward"}, {b, a, "reverse"}} {
			if v, ok := jitter[k]; ok && (p.JitterSeconds == nil || v > *p.JitterSeconds) {
				p.JitterSeconds, jitterFrom = &v, k[0]
			}
		}
		var mtuFrom string
		for _, k := range [][3]string{{a, b, ""}, {b, a, ""}} {
			if v, ok := mtu[k]; ok && v >= 1 {
				if m := int(math.Round(v)); p.MTUBytes == nil || m < *p.MTUBytes {
					p.MTUBytes, mtuFrom = &m, k[0]
				}
			}
		}
		latest := func(m map[[3]string]float64, keys ...[3]string) *time.Time {
			var best float64
			var from string
			for _, k := range keys {
				if v, ok := m[k]; ok && v > best {
					best, from = v, k[0]
				}
			}
			if best <= 0 {
				return nil
			}
			used[from] = true
			t := time.Unix(int64(best), 0).UTC()
			return &t
		}
		p.ProbedAt = latest(probed, [3]string{a, b, "forward"}, [3]string{a, b, "reverse"}, [3]string{b, a, "forward"}, [3]string{b, a, "reverse"})
		p.TestedAt = latest(tested, [3]string{a, b, ""}, [3]string{b, a, ""})
		if !l.NoProbe {
			// The state of a direction comes from the cluster that reports it first, as the loss does.
			state := func(keys ...[3]string) *LinkProbeLatest {
				for _, k := range keys {
					v, ok := up[k]
					if !ok {
						continue
					}
					used[k[0]] = true
					s := &LinkProbeLatest{State: probeOK, At: latest(probed, keys...)}
					if v < 1 {
						s.State, s.Reason = probeFailing, probeWhy[k]
						if s.At == nil {
							s.State = probeNever
						}
					}
					return s
				}
				return nil
			}
			p.ProbeAToB = state([3]string{a, b, "forward"}, [3]string{b, a, "reverse"})
			p.ProbeBToA = state([3]string{a, b, "reverse"}, [3]string{b, a, "forward"})
			// The connect timing is the pair's too: it says whether the responder's control port is reached at all.
			var connect connectEvidence
			for _, k := range [][3]string{{a, b, ""}, {b, a, ""}} {
				if why, ok := connectWhy[k]; ok {
					connect.failed, connect.reason = true, why
					break
				}
			}
			connect.timed = p.RTTSeconds != nil
			diagnose(p.ProbeAToB, p.ProbeBToA, p.LossAToB, connect, now)
			diagnose(p.ProbeBToA, p.ProbeAToB, p.LossBToA, connect, now)
		}
		for _, c := range []string{jitterFrom, mtuFrom} {
			if c != "" {
				used[c] = true
			}
		}
		for _, c := range []string{a, b} {
			if used[c] {
				p.By = append(p.By, c)
			}
		}
		out = append(out, p)
	}
	return out
}

// connectEvidence is what the connect timing says about the pair's responder: whether connections were timed lately (the round
// trip is the mean over the last minutes) and whether rounds failed lately, and the commonest reason.
type connectEvidence struct {
	timed, failed bool
	reason        ProbeFailure
}

// diagnose adds to one direction's probe state what the evidence fits. The rules are conservative: a wrong diagnosis is worse
// than none, and from the prober's side some causes cannot be told apart (a UDP filter, address translation, clocks more than
// two seconds apart), so the screen words those as "one of". A direction with no state, or with nothing that fits, gets none.
func diagnose(s, other *LinkProbeLatest, loss *float64, c connectEvidence, now time.Time) {
	if s == nil {
		return
	}
	// Connections failing and none timed: nothing was reached on the control port, whatever the probe's own reason says.
	unanswered := c.failed && !c.timed
	// Connections timed and none failing: the responder answers on the control port.
	answers := c.timed && !c.failed
	switch s.State {
	case probeOK:
		switch {
		case loss != nil && *loss >= linkLostAt:
			s.Diagnosis = diagUDPLost
		case s.At != nil && !now.IsZero() && now.Sub(*s.At) > linkStaleAfter:
			s.Diagnosis = diagStale
		}
	default:
		switch {
		case s.Reason == failNoSuchKind:
			// The pair is pinned to a kind of address the peer lacks: the note says so, and no connection could say more.
		case unanswered && c.reason == "refused":
			s.Diagnosis = diagNoResponder
		case unanswered && (c.reason == "timeout" || c.reason == "unreachable"):
			s.Diagnosis = diagFiltered
		case answers && (s.Reason == failOther || s.Reason == failAuth):
			s.Diagnosis = diagDenied
		case other != nil && other.State == probeOK:
			s.Diagnosis = diagOneWay
		}
	}
}
