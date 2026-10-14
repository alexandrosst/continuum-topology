package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

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
	At        time.Time        `json:"at"`
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
	doc := LinksLatestDoc{Pairs: []LinkPairLatest{}, At: now().UTC()}
	links := a.core(r).Settings().Links
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
	readings, at, err := a.linkReadings.get(r.Context(), now, func(ctx context.Context) (fusionapi.LinkReadings, error) {
		return a.Fusion.dataClient().LinkReadings(ctx, fusionapi.AllSignals())
	})
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
	doc.Pairs = latestPairs(links, readings)
	writeJSON(w, 200, doc)
}

// latestPairs picks, for each listed pair, the figures about it out of everything the clusters reported. Both clusters of a pair
// measure towards each other, and they may disagree a little: the pair's first cluster (the one that also runs the throughput
// tests) is believed first, the second only where the first has nothing.
func latestPairs(links []LinkSetting, rd fusionapi.LinkReadings) []LinkPairLatest {
	idx := func(in []fusionapi.LinkReading) map[[3]string]float64 {
		m := make(map[[3]string]float64, len(in))
		for _, x := range in {
			m[[3]string{x.Cluster, x.Peer, x.Direction}] = x.Value
		}
		return m
	}
	loss, jitter, rtt, mtu, tput, probed, tested := idx(rd.Loss), idx(rd.Jitter), idx(rd.RTT), idx(rd.MTU), idx(rd.Throughput), idx(rd.Probed), idx(rd.Tested)
	out := make([]LinkPairLatest, 0, len(links))
	for _, l := range links {
		a, b := l.A, l.B
		p := LinkPairLatest{A: a, B: b}
		// first returns the first key that has a value.
		first := func(m map[[3]string]float64, keys ...[3]string) *float64 {
			for _, k := range keys {
				if v, ok := m[k]; ok {
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
		for _, k := range [][3]string{{a, b, "forward"}, {a, b, "reverse"}, {b, a, "forward"}, {b, a, "reverse"}} {
			if v, ok := jitter[k]; ok && (p.JitterSeconds == nil || v > *p.JitterSeconds) {
				p.JitterSeconds = &v
			}
		}
		for _, k := range [][3]string{{a, b, ""}, {b, a, ""}} {
			if v, ok := mtu[k]; ok && v >= 1 {
				if m := int(math.Round(v)); p.MTUBytes == nil || m < *p.MTUBytes {
					p.MTUBytes = &m
				}
			}
		}
		latest := func(m map[[3]string]float64, keys ...[3]string) *time.Time {
			var best float64
			for _, k := range keys {
				if v, ok := m[k]; ok && v > best {
					best = v
				}
			}
			if best <= 0 {
				return nil
			}
			t := time.Unix(int64(best), 0).UTC()
			return &t
		}
		p.ProbedAt = latest(probed, [3]string{a, b, "forward"}, [3]string{a, b, "reverse"}, [3]string{b, a, "forward"}, [3]string{b, a, "reverse"})
		p.TestedAt = latest(tested, [3]string{a, b, ""}, [3]string{b, a, ""})
		out = append(out, p)
	}
	return out
}
