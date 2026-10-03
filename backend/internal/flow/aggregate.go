package flow

import (
	"container/heap"
	"sort"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/proto"
)

// MaxFlowsPerBatch bounds what one window can send; the busiest edges are kept.
const MaxFlowsPerBatch = 5000

// MaxHeldFlows bounds how many distinct edges the aggregator holds between two flushes. Flushing stops while the agent
// has no connection to the server, but the node collectors keep reporting, so without a cap a long outage would grow
// the map for as long as it lasted. Past the cap the edges seen longest ago are dropped (and counted), so what is kept
// is the most recent traffic, which is what the next window will describe.
const MaxHeldFlows = 20000

// MaxHeldPodFlows bounds the separate, per-pod breakdown table (podFlows below) the same way MaxHeldFlows
// bounds flows - but much smaller, since this table is a best-effort, "right now" supplement (wholesale
// reset on every Flush, never accumulated across windows) rather than the historically-accumulated data
// flows itself feeds, and losing an edge of it under extreme load costs far less.
const MaxHeldPodFlows = 4000

type key struct {
	srcKind, dstKind continuumv1.FlowEndpoint_Kind
	src, dst         string
	port             uint32
	proto            string
}

// podKey extends key with the specific pod names on either side (Flow.src_pod/dst_pod, set by
// resolve.go only when that side resolved to one particular live pod - see its own doc comments). This
// is deliberately never key's own shape: flows stays bounded at workload granularity, and podFlows below
// is where the finer identity lives instead, in its own much smaller, separately-bounded table.
type podKey struct {
	key
	srcPod, dstPod string
}

// Aggregator sums the flows of every node's reports over a window.
type Aggregator struct {
	now func() time.Time

	mu         sync.Mutex
	flows      map[key]*continuumv1.Flow
	touched    map[key]uint64 // when (in touches) each held edge was last added to
	podFlows   map[podKey]*continuumv1.Flow
	podTouched map[podKey]uint64
	touches    uint64
	since      time.Time
	lost       uint64
	dropped    uint64 // edges discarded for want of room since the process started (never reset)
	seq        uint64
	paused     bool
	max        int
	podMax     int

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
	a := &Aggregator{now: time.Now, flows: map[key]*continuumv1.Flow{}, touched: map[key]uint64{}, podFlows: map[podKey]*continuumv1.Flow{}, podTouched: map[podKey]uint64{}, collectors: map[string]collectorSeen{}, max: MaxHeldFlows, podMax: MaxHeldPodFlows}
	a.since = a.now()
	return a
}

func ref(e *continuumv1.FlowEndpoint) string {
	if e.Kind == continuumv1.FlowEndpoint_EXTERNAL {
		return e.Ip
	}
	return e.Ref
}

// mergeFlowCounters folds add's counters/gauges into cur in place - the one rule set for every field
// Flow carries, shared by flows and podFlows below: a held edge accumulates the same way regardless of
// which table it lives in.
func mergeFlowCounters(cur, add *continuumv1.Flow) {
	cur.Connections += add.Connections
	cur.BytesOut += add.BytesOut
	cur.BytesIn += add.BytesIn
	cur.Retransmits += add.Retransmits
	cur.RtoRetransmits += add.RtoRetransmits
	cur.SegsOut += add.SegsOut
	cur.BufferDrops += add.BufferDrops
	cur.MeshBypassSyns += add.MeshBypassSyns
	cur.FailedAttempts += add.FailedAttempts
	if add.RttUs != 0 {
		cur.RttUs = add.RttUs // a gauge, not a sum: the latest sample replaces the last, same as Iface
	}
	if add.JitterUs != 0 {
		cur.JitterUs = add.JitterUs // a gauge, same treatment as RttUs right above it
	}
	if add.HandshakeUs != 0 {
		cur.HandshakeUs = add.HandshakeUs // set once per connection; held the same way as RttUs/JitterUs
	}
	if add.Cwnd != 0 {
		cur.Cwnd = add.Cwnd // a gauge, same treatment as RttUs/JitterUs
	}
	if add.PacingBps != 0 {
		cur.PacingBps = add.PacingBps // a gauge, same treatment as Cwnd right above
	}
	if add.DnsRttUs != 0 {
		cur.DnsRttUs = add.DnsRttUs // a gauge, same treatment as the other sampled figures above
	}
	if add.MssBytes != 0 {
		cur.MssBytes = add.MssBytes // a gauge, same treatment as Cwnd/PacingBps above
	}
	if add.RcvWndBytes != 0 {
		cur.RcvWndBytes = add.RcvWndBytes // a gauge, same treatment as MssBytes/Cwnd above
	}
	if add.SndWndBytes != 0 {
		cur.SndWndBytes = add.SndWndBytes // a gauge, same treatment as RcvWndBytes right above
	}
	if add.WmemQueuedBytes != 0 {
		cur.WmemQueuedBytes = add.WmemQueuedBytes // a gauge, same treatment as RcvWndBytes/SndWndBytes
	}
	if add.SndbufBytes != 0 {
		cur.SndbufBytes = add.SndbufBytes // a gauge, same treatment as the three above
	}
	if add.SniHost != "" {
		cur.SniHost = add.SniHost // also a gauge: one peer essentially always carries one hostname
	}
	cur.DnsQueryNames = mergeDNSNames(cur.DnsQueryNames, add.DnsQueryNames)
	cur.BytesKnown = cur.BytesKnown || add.BytesKnown
	if add.Method == "ebpf" {
		cur.Method = "ebpf"
	}
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
		mergeFlowCounters(cur, f)
	} else {
		a.flows[k] = f
		if len(a.flows) > a.max {
			a.evict()
		}
	}
	a.addPod(k, f)
}

// addPod folds f into the separate, smaller, per-window pod-level breakdown, only when at least one
// side resolved to a specific live pod (f.SrcPod/f.DstPod - see resolve.go). Keyed by k plus those pod
// names, so distinct pod pairs behind the same workload pair stay distinguishable here even though
// flows above deliberately collapses them. A brand-new entry is stored as an independent clone, never
// the same *continuumv1.Flow pointer handed to flows above - the two tables hold genuinely separate
// counters per pod pair, and aliasing one edge's object between both tables would let an unrelated pod
// pair's traffic silently inflate this one's counters the next time flows' own entry for k is merged.
func (a *Aggregator) addPod(k key, f *continuumv1.Flow) {
	if f.SrcPod == "" && f.DstPod == "" {
		return
	}
	pk := podKey{k, f.SrcPod, f.DstPod}
	a.podTouched[pk] = a.touches
	if cur, ok := a.podFlows[pk]; ok {
		mergeFlowCounters(cur, f)
		return
	}
	a.podFlows[pk] = proto.Clone(f).(*continuumv1.Flow)
	if len(a.podFlows) > a.podMax {
		a.evictPod()
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

// agedEntry is one held edge's key paired with when it was last touched - the only two fields eviction
// needs to pick evictions, so it never has to copy a whole *continuumv1.Flow just to sort by one of its
// fields. Generic over the key shape so both flows (key) and podFlows (podKey) below share one eviction
// routine. Mirrors observed.go's edgeAge/oldestEdges (same shape, same reason: evicting a bounded number
// of oldest entries out of a much larger table shouldn't cost a full sort of that table).
type agedEntry[K comparable] struct {
	k K
	t uint64
}

// agedHeap is a bounded max-heap of the oldest-looking agedEntries seen so far during a single linear
// scan: its root (index 0) is always the entry with the LATEST t among those currently held - once the
// heap holds n entries, a new, genuinely older candidate only ever needs to evict that one (the entry
// least likely to belong in the final "n oldest" answer), never re-examine the rest. See observed.go's
// ageHeap for the identical reasoning in more detail.
type agedHeap[K comparable] []agedEntry[K]

func (h agedHeap[K]) Len() int           { return len(h) }
func (h agedHeap[K]) Less(i, j int) bool { return h[i].t > h[j].t }
func (h agedHeap[K]) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *agedHeap[K]) Push(x any)        { *h = append(*h, x.(agedEntry[K])) }
func (h *agedHeap[K]) Pop() any {
	old := *h
	last := len(old) - 1
	x := old[last]
	*h = old[:last]
	return x
}

// evictOldest drops the tenth of the held entries that were touched longest ago from both flows and
// touched, and reports how many it dropped. Generic over the key shape so Aggregator.evict (flows/
// touched/max) and evictPod (podFlows/podTouched/podMax) share this one implementation. Finds those n
// oldest entries in one O(len(flows) * log n) pass via a bounded max-heap, rather than a full
// O(len(flows) log len(flows)) sort of the whole table just to throw away the n it actually needs.
func evictOldest[K comparable, V any](flows map[K]*V, touched map[K]uint64, max_ int) int {
	n := max(len(flows)-max_, max_/10, 1)
	h := make(agedHeap[K], 0, n)
	for k, t := range touched {
		age := agedEntry[K]{k, t}
		switch {
		case len(h) < n:
			heap.Push(&h, age)
		case age.t < h[0].t:
			heap.Pop(&h)
			heap.Push(&h, age)
		}
	}
	for _, x := range h {
		delete(flows, x.k)
		delete(touched, x.k)
	}
	return len(h)
}

// evict drops the tenth of the held edges that were touched longest ago. It runs once per max/10 additions past the
// cap, so the cost is amortised, and what was dropped is counted so the agent can say so.
func (a *Aggregator) evict() {
	a.dropped += uint64(evictOldest(a.flows, a.touched, a.max))
}

// evictPod is evict's exact counterpart for podFlows/podTouched/podMax. Unlike evict, nothing counts
// what this drops: podFlows is a best-effort, wholesale-reset-every-Flush supplement, not the historically-
// accumulated data flows itself feeds, so losing one of its edges under extreme load within a single
// window is not worth a dedicated stat.
func (a *Aggregator) evictPod() {
	evictOldest(a.podFlows, a.podTouched, a.podMax)
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

// SetPodMax is SetMax's counterpart for the separate per-pod breakdown table (tests use a small one to
// exercise its eviction without needing thousands of distinct pod pairs).
func (a *Aggregator) SetPodMax(n int) {
	a.mu.Lock()
	a.podMax = max(n, 1)
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
		a.podFlows, a.podTouched = map[podKey]*continuumv1.Flow{}, map[podKey]uint64{}
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

// Seen records that a collector reported, whatever it saw. linkSaturation is that collector's latest
// per-interface throughput/saturation reading (FlowReport.link_saturation) - replaced wholesale on
// every report, the same "latest, not accumulated" treatment collectorSeen already gives the rest of
// CollectorInfo, since a saturation percentage is a gauge of the window just reported, not something
// that means anything summed across windows. snatExhaustion is that collector's current lifetime total
// of EADDRNOTAVAIL connect() failures (FlowReport.snat_exhaustion, see its own doc comment) - the same
// wholesale-replace treatment, even though the number itself is already a running total from the
// collector's own side: the aggregator still just takes the latest reading rather than summing reports,
// so a collector restart (which resets its own counter to 0) is reflected here too, not papered over by
// an ever-growing server-side sum.
func (a *Aggregator) Seen(node, method string, bytesKnown bool, linkSaturation []*continuumv1.LinkSaturation, snatExhaustion uint64) {
	a.mu.Lock()
	if a.paused {
		a.mu.Unlock()
		return
	}
	a.collectors[node+"/"+method] = collectorSeen{&continuumv1.CollectorInfo{Node: node, Method: method, BytesKnown: bytesKnown, LinkSaturation: linkSaturation, SnatExhaustion: snatExhaustion}, a.now()}
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
	flows, podFlows, lost := a.flows, a.podFlows, a.lost
	a.flows, a.touched, a.since, a.lost = map[key]*continuumv1.Flow{}, map[key]uint64{}, now, 0
	a.podFlows, a.podTouched = map[podKey]*continuumv1.Flow{}, map[podKey]uint64{}
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
	if len(flows) == 0 && len(podFlows) == 0 && lost == 0 && len(collectors) == 0 {
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
	// podFlows is already bounded to podMax (<= MaxHeldPodFlows) while held, and reset wholesale right
	// above - no second per-batch truncation needed the way b.Flows above still gets one.
	for _, f := range podFlows {
		b.PodFlows = append(b.PodFlows, f)
	}
	sort.Slice(b.PodFlows, func(i, j int) bool {
		if b.PodFlows[i].Connections != b.PodFlows[j].Connections {
			return b.PodFlows[i].Connections > b.PodFlows[j].Connections
		}
		return b.PodFlows[i].SrcPod+b.PodFlows[i].DstPod < b.PodFlows[j].SrcPod+b.PodFlows[j].DstPod
	})
	a.seq++
	b.Seq = a.seq
	return b
}
