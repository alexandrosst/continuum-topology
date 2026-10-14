package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"continuum/internal/fusionapi"
)

// The latest figures of each watched pair, for the Sites screen. The pairs come from the organisation's settings; the numbers
// from the link series that the clusters' collectors send to FUSION (see internal/link for what they are). Nothing is stored
// here: a figure is whatever FUSION held a moment ago, and a pair FUSION has nothing for simply has no figures.

const (
	// linksLatestEvery is how long one read of FUSION is shared. The screen refreshes itself, and several people may have it open.
	linksLatestEvery   = 15 * time.Second
	linksLatestTimeout = 10 * time.Second
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
}

// LinksLatestDoc is the answer. When FUSION cannot be read, Available is false and Reason says why in a word the screen turns into
// a sentence; the pairs are then not listed.
type LinksLatestDoc struct {
	Available bool             `json:"available"`
	Reason    string           `json:"reason,omitempty"` // no-fusion | unavailable
	Pairs     []LinkPairLatest `json:"pairs"`
	// Notes say why a pair is not measured from one of its clusters (see LinkNote), whether or not FUSION can be read.
	Notes []LinkNote `json:"notes"`
	At    time.Time  `json:"at"`
}

type linkReadingsCache struct {
	mu   sync.Mutex
	at   time.Time
	r    fusionapi.LinkReadings
	err  error
	have bool
}

// get returns the readings, reading FUSION again when the kept ones are older than linksLatestEvery. One read at a time:
// those who arrive during it wait for it and share its result, and a failure is kept just as long, so a store that is down is
// not asked again by every refresh.
func (c *linkReadingsCache) get(ctx context.Context, now func() time.Time, read func(context.Context) (fusionapi.LinkReadings, error)) (fusionapi.LinkReadings, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.have && now().Sub(c.at) < linksLatestEvery {
		return c.r, c.at, c.err
	}
	ctx, cancel := context.WithTimeout(ctx, linksLatestTimeout)
	defer cancel()
	r, err := read(ctx)
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return fusionapi.LinkReadings{}, now(), err // the caller left; this says nothing about the store
	}
	c.r, c.err, c.at, c.have = r, err, now(), true
	return r, c.at, err
}

func (a *Admin) linksLatest(w http.ResponseWriter, r *http.Request) {
	now := a.C.Now
	doc := LinksLatestDoc{Pairs: []LinkPairLatest{}, Notes: []LinkNote{}, At: now().UTC()}
	links := a.core(r).Settings().Links
	if notes := a.tn(r).Hub.LinkNotes(r.Context(), links); notes != nil {
		doc.Notes = notes
	}
	if a.Fusion == nil {
		doc.Reason = "no-fusion"
		writeJSON(w, 200, doc)
		return
	}
	if len(links) == 0 {
		doc.Available = true
		writeJSON(w, 200, doc)
		return
	}
	pairs, at, err := a.linkFigures(r.Context(), a.Fusion.dataClient(), links)
	if err != nil {
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		// What the store said is not for a viewer: it can name hosts. The administrator who needs it has FUSION's own page.
		doc.Reason = "unavailable"
		writeJSON(w, 200, doc)
		return
	}
	doc.Available = true
	doc.At = at.UTC()
	doc.Pairs = pairs
	writeJSON(w, 200, doc)
}

// fusionLinkPair is one pair in FUSION's data API: the Sites screen's figures, and whose series they are.
type fusionLinkPair struct {
	LinkPairLatest
	// MeasuredBy is the pair's clusters (a, b or both) whose series supplied the figures.
	MeasuredBy []string `json:"measuredBy"`
}

// fusionLinks is GET /api/v1/fusion/links: the figures of the Sites screen for a script, a dashboard or a decision engine that holds
// a FUSION access token. The pairs are those of the FUSION organisation's settings. The figures are read for everyone alike, with
// the server's own scope and fixed queries (see linkFigures), and what the caller may see is then decided here by cluster id: a
// pair is shown only when the token may read both of its clusters, and a token without the metrics signal, or one limited to
// namespaces, is refused (Scope.NeedLinks).
func (a *Admin) fusionLinks(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
	if err := who.Scope.NeedLinks(); err != nil {
		a.fusionErr(w, r, err)
		return
	}
	q := r.URL.Query()
	for _, k := range []string{"cluster", "peer"} {
		if v := q.Get(k); q.Has(k) && (v == "" || len(v) > 253 || !utf8.ValidString(v)) {
			a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: k + " must be 1 to 253 characters of text"})
			return
		}
	}
	cluster, peer := q.Get("cluster"), q.Get("peer")
	if peer != "" && cluster == "" {
		a.fusionErr(w, r, &fusionapi.Error{Status: http.StatusBadRequest, Msg: "peer needs cluster: it names the other end of the link"})
		return
	}
	links, err := a.fusionLinkSettings(r.Context())
	if err != nil {
		a.fusionErr(w, r, err)
		return
	}
	var want []LinkSetting
	for _, l := range links {
		switch {
		case !who.Scope.LinkPairVisible(l.A, l.B):
		case cluster != "" && l.A != cluster && l.B != cluster:
		case peer != "" && l.A != peer && l.B != peer:
		default:
			want = append(want, l)
		}
	}
	doc := struct {
		Pairs []fusionLinkPair `json:"pairs"`
		At    time.Time        `json:"at"`
	}{Pairs: []fusionLinkPair{}, At: a.C.Now().UTC()}
	if len(want) > 0 {
		pairs, at, err := a.linkPairReadings(r.Context(), c, want)
		if err != nil {
			switch {
			case errors.Is(r.Context().Err(), context.Canceled):
			case errors.Is(err, context.DeadlineExceeded):
				a.fusionErr(w, r, err)
			default: // what the store said can name hosts
				writeErr(w, http.StatusServiceUnavailable, "the link figures could not be read from FUSION's metrics store")
			}
			return
		}
		doc.At = at.UTC()
		for _, p := range pairs {
			by := p.By
			if by == nil {
				by = []string{}
			}
			doc.Pairs = append(doc.Pairs, fusionLinkPair{LinkPairLatest: p.LinkPairLatest, MeasuredBy: by})
		}
	}
	writeJSON(w, 200, doc)
}

// fusionLinkSettings are the pairs the FUSION organisation asked to have watched.
func (a *Admin) fusionLinkSettings(ctx context.Context) ([]LinkSetting, error) {
	org := a.fusionOrg()
	if a.P == nil {
		if a.C.OrgID == org {
			return a.C.Settings().Links, nil
		}
		return nil, &fusionapi.Error{Status: http.StatusServiceUnavailable, Msg: "the organisation's settings are not available"}
	}
	t, err := a.P.Tenant(ctx, org)
	if err != nil || t == nil {
		return nil, &fusionapi.Error{Status: http.StatusServiceUnavailable, Msg: "the organisation's settings are not available"}
	}
	return t.C.Settings().Links, nil
}

// linkFigures is the one place the figures are read, for the Sites screen and for FUSION's data API alike: the latest figures of
// the listed pairs, and when FUSION was read. The queries are fixed and run with an unrestricted scope of the server's own, whoever
// asks: nothing a caller supplies reaches them, and what a caller may see of the answer is decided by the caller of this function
// (the Sites screen by the organisation, the data API by the token's clusters). One read is shared by everyone for linksLatestEvery.
func (a *Admin) linkFigures(ctx context.Context, c *fusionapi.Client, links []LinkSetting) ([]LinkPairLatest, time.Time, error) {
	pairs, at, err := a.linkPairReadings(ctx, c, links)
	if err != nil {
		return nil, at, err
	}
	out := make([]LinkPairLatest, len(pairs))
	for i, p := range pairs {
		out[i] = p.LinkPairLatest
	}
	return out, at, nil
}

// linkPairReadings is linkFigures with, for each pair, which clusters' series the figures came from.
func (a *Admin) linkPairReadings(ctx context.Context, c *fusionapi.Client, links []LinkSetting) ([]pairReading, time.Time, error) {
	readings, at, err := a.linkReadings.get(ctx, a.C.Now, func(ctx context.Context) (fusionapi.LinkReadings, error) {
		return c.LinkReadings(ctx, fusionapi.AllSignals())
	})
	if err != nil {
		return nil, at, err
	}
	return readPairs(links, readings), at, nil
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
// trace runs on both sides, so the MTU is the smaller of the two.
func latestPairs(links []LinkSetting, rd fusionapi.LinkReadings) []LinkPairLatest {
	in := readPairs(links, rd)
	out := make([]LinkPairLatest, len(in))
	for i, p := range in {
		out[i] = p.LinkPairLatest
	}
	return out
}

func readPairs(links []LinkSetting, rd fusionapi.LinkReadings) []pairReading {
	idx := func(in []fusionapi.LinkReading) map[[3]string]float64 {
		m := make(map[[3]string]float64, len(in))
		for _, x := range in {
			m[[3]string{x.Cluster, x.Peer, x.Direction}] = x.Value
		}
		return m
	}
	loss, jitter, rtt, mtu, tput, probed, tested := idx(rd.Loss), idx(rd.Jitter), idx(rd.RTT), idx(rd.MTU), idx(rd.Throughput), idx(rd.Probed), idx(rd.Tested)
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
