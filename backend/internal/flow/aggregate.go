package flow

import (
	"container/heap"
	"sort"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// MaxFlowsPerBatch bounds what one window can send; the busiest edges are kept.
const MaxFlowsPerBatch = 5000

// MaxHeldFlows bounds how many distinct edges the aggregator holds between two flushes. Flushing stops while the agent
// has no connection to the server, but the node collectors keep reporting, so without a cap a long outage would grow
// the map for as long as it lasted. Past the cap the edges seen longest ago are dropped (and counted), so what is kept
// is the most recent traffic, which is what the next window will describe.
const MaxHeldFlows = 20000

type key struct {
	srcKind, dstKind continuumv1.FlowEndpoint_Kind
	src, dst         string
	port             uint32
	proto            string
}

// Aggregator sums the flows of every node's reports over a window.
type Aggregator struct {
	now func() time.Time

	mu      sync.Mutex
	flows   map[key]*continuumv1.Flow
	touched map[key]uint64 // when (in touches) each held edge was last added to
	touches uint64
	since   time.Time
	lost    uint64
	dropped uint64 // edges discarded for want of room since the process started (never reset)
	seq     uint64
	paused  bool
	max     int

	collectors map[string]collectorSeen
}

type collectorSeen struct {
	info *continuumv1.CollectorInfo
	at   time.Time
}

// collectorTTL is how long a collector counts as present after its last report. Collectors report every
// window even when they saw nothing, so a few missed windows mean it is gone.
const collectorTTL = 5 * time.Minute

func NewAggregator() *Aggregator {
	a := &Aggregator{now: time.Now, flows: map[key]*continuumv1.Flow{}, touched: map[key]uint64{}, collectors: map[string]collectorSeen{}, max: MaxHeldFlows}
	a.since = a.now()
	return a
}

func ref(e *continuumv1.FlowEndpoint) string {
	if e.Kind == continuumv1.FlowEndpoint_EXTERNAL {
		return e.Ip
	}
	return e.Ref
}

func (a *Aggregator) Add(f *continuumv1.Flow) {
	k := key{f.Src.Kind, f.Dst.Kind, ref(f.Src), ref(f.Dst), f.Port, f.Protocol}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.paused {
		return
	}
	a.touches++
	a.touched[k] = a.touches
	if cur, ok := a.flows[k]; ok {
		cur.Connections += f.Connections
		cur.BytesOut += f.BytesOut
		cur.BytesIn += f.BytesIn
		cur.Retransmits += f.Retransmits
		cur.SegsOut += f.SegsOut
		cur.BufferDrops += f.BufferDrops
		cur.FailedAttempts += f.FailedAttempts
		if f.RttUs != 0 {
			cur.RttUs = f.RttUs // a gauge, not a sum: the latest sample replaces the last, same as Iface
		}
		if f.JitterUs != 0 {
			cur.JitterUs = f.JitterUs // a gauge, same treatment as RttUs right above it
		}
		if f.HandshakeUs != 0 {
			cur.HandshakeUs = f.HandshakeUs // set once per connection; held the same way as RttUs/JitterUs
		}
		if f.Cwnd != 0 {
			cur.Cwnd = f.Cwnd // a gauge, same treatment as RttUs/JitterUs
		}
		if f.PacingBps != 0 {
			cur.PacingBps = f.PacingBps // a gauge, same treatment as Cwnd right above
		}
		if f.DnsRttUs != 0 {
			cur.DnsRttUs = f.DnsRttUs // a gauge, same treatment as the other sampled figures above
		}
		if f.SniHost != "" {
			cur.SniHost = f.SniHost // also a gauge: one peer essentially always carries one hostname
		}
		cur.DnsQueryNames = mergeDNSNames(cur.DnsQueryNames, f.DnsQueryNames)
		cur.BytesKnown = cur.BytesKnown || f.BytesKnown
		if f.Method == "ebpf" {
			cur.Method = "ebpf"
		}
		return
	}
	a.flows[k] = f
	if len(a.flows) > a.max {
		a.evict()
	}
}

// maxHeldDNSNames bounds how many distinct domain names one held (and, later, one stored) dns-noise
// edge remembers - a resolver edge can legitimately field many different lookups, but "recently asked
// about" is the point, not a full log.
const maxHeldDNSNames = 8

// mergeDNSNames folds add's distinct, non-empty names into cur, newest first, capped at
// maxHeldDNSNames - used both here (within one window) and by the server's own FlowEdge accumulation
// (across many windows), since the list-vs-gauge reasoning is identical either way.
func mergeDNSNames(cur, add []string) []string {
	for _, n := range add {
		if n == "" {
			continue
		}
		found := false
		for _, c := range cur {
			if c == n {
				found = true
				break
			}
		}
		if !found {
			cur = append([]string{n}, cur...)
		}
	}
	if len(cur) > maxHeldDNSNames {
		cur = cur[:maxHeldDNSNames]
	}
	return cur
}

// agedKey is one held edge's key paired with when it was last touched - the only two fields evict needs
// to pick evictions, so it never has to copy a whole *continuumv1.Flow just to sort by one of its fields.
// Mirrors observed.go's edgeAge/oldestEdges (same shape, same reason: evicting a bounded number of oldest
// entries out of a much larger table shouldn't cost a full sort of that table).
type agedKey struct {
	k key
	t uint64
}

// agedHeap is a bounded max-heap of the oldest-looking agedKeys seen so far during a single linear scan:
// its root (index 0) is always the entry with the LATEST t among those currently held - once the heap
// holds n entries, a new, genuinely older candidate only ever needs to evict that one (the entry least
// likely to belong in the final "n oldest" answer), never re-examine the rest. See observed.go's ageHeap
// for the identical reasoning in more detail.
type agedHeap []agedKey

func (h agedHeap) Len() int           { return len(h) }
func (h agedHeap) Less(i, j int) bool { return h[i].t > h[j].t }
func (h agedHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *agedHeap) Push(x any)        { *h = append(*h, x.(agedKey)) }
func (h *agedHeap) Pop() any {
	old := *h
	last := len(old) - 1
	x := old[last]
	*h = old[:last]
	return x
}

// evict drops the tenth of the held edges that were touched longest ago. It runs once per max/10 additions past the
// cap, so the cost is amortised, and what was dropped is counted so the agent can say so. Finds those n oldest
// entries in one O(len(a.flows) * log n) pass via a bounded max-heap, rather than a full O(len(a.flows) log
// len(a.flows)) sort of the whole table just to throw away the n it actually needs.
func (a *Aggregator) evict() {
	n := max(len(a.flows)-a.max, a.max/10, 1)
	h := make(agedHeap, 0, n)
	for k, t := range a.touched {
		age := agedKey{k, t}
		switch {
		case len(h) < n:
			heap.Push(&h, age)
		case age.t < h[0].t:
			heap.Pop(&h)
			heap.Push(&h, age)
		}
	}
	for _, x := range h {
		delete(a.flows, x.k)
		delete(a.touched, x.k)
		a.dropped++
	}
}

// Dropped is how many edges were discarded because too many were being held (since the process started).
func (a *Aggregator) Dropped() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// Held is how many distinct edges wait for the next flush.
func (a *Aggregator) Held() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.flows)
}

// SetMax lowers or raises the cap on held edges (tests use a small one).
func (a *Aggregator) SetMax(n int) {
	a.mu.Lock()
	a.max = max(n, 1)
	a.mu.Unlock()
}

// SetPaused stops the aggregator from taking anything in (and forgets what it held and which collectors it had heard
// from): the server asked this collector to stop, and stopping means the data is not kept, not merely not sent.
func (a *Aggregator) SetPaused(p bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.paused == p {
		return
	}
	a.paused = p
	if p {
		a.flows, a.touched, a.collectors, a.lost = map[key]*continuumv1.Flow{}, map[key]uint64{}, map[string]collectorSeen{}, 0
	} else {
		a.since = a.now()
	}
}

// Presence says how many node collectors reported within the last collectorTTL and when the last report of any kind came.
func (a *Aggregator) Presence() (collectors int, last time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for _, c := range a.collectors {
		if now.Sub(c.at) <= collectorTTL {
			collectors++
		}
		if c.at.After(last) {
			last = c.at
		}
	}
	return collectors, last
}

// Seen records that a collector reported, whatever it saw.
func (a *Aggregator) Seen(node, method string, bytesKnown bool) {
	a.mu.Lock()
	if a.paused {
		a.mu.Unlock()
		return
	}
	a.collectors[node+"/"+method] = collectorSeen{&continuumv1.CollectorInfo{Node: node, Method: method, BytesKnown: bytesKnown}, a.now()}
	a.mu.Unlock()
}

func (a *Aggregator) AddLost(n uint64) {
	a.mu.Lock()
	if !a.paused {
		a.lost += n
	}
	a.mu.Unlock()
}

// Flush returns everything seen since the last flush, or nil when there was nothing to say.
func (a *Aggregator) Flush() *continuumv1.FlowBatch {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	window := int32(now.Sub(a.since).Round(time.Second) / time.Second)
	if window < 1 {
		window = 1
	}
	if a.paused {
		return nil
	}
	flows, lost := a.flows, a.lost
	a.flows, a.touched, a.since, a.lost = map[key]*continuumv1.Flow{}, map[key]uint64{}, now, 0
	var collectors []*continuumv1.CollectorInfo
	for k, c := range a.collectors {
		if now.Sub(c.at) > collectorTTL {
			delete(a.collectors, k)
			continue
		}
		collectors = append(collectors, c.info)
	}
	sort.Slice(collectors, func(i, j int) bool {
		return collectors[i].Node+collectors[i].Method < collectors[j].Node+collectors[j].Method
	})
	if len(flows) == 0 && lost == 0 && len(collectors) == 0 {
		return nil
	}
	b := &continuumv1.FlowBatch{WindowSeconds: window, Lost: lost, Collectors: collectors}
	for _, f := range flows {
		b.Flows = append(b.Flows, f)
	}
	sort.Slice(b.Flows, func(i, j int) bool {
		if b.Flows[i].Connections != b.Flows[j].Connections {
			return b.Flows[i].Connections > b.Flows[j].Connections
		}
		return ref(b.Flows[i].Src)+ref(b.Flows[i].Dst) < ref(b.Flows[j].Src)+ref(b.Flows[j].Dst)
	})
	if len(b.Flows) > MaxFlowsPerBatch {
		b.Lost += uint64(len(b.Flows) - MaxFlowsPerBatch)
		b.Flows = b.Flows[:MaxFlowsPerBatch]
	}
	a.seq++
	b.Seq = a.seq
	return b
}
