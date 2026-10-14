package link

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/probe"
)

// Bucket upper bounds, in seconds. Explicit buckets keep the series count fixed whatever the traffic does. The kernel's RTT
// mean deviation is not kept: the one-way probe's delay variation says the same thing better.
var rttBuckets = []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2.5}

type hist struct {
	bounds []float64
	counts []uint64 // per bound, not cumulative; the last slot is +Inf
	sum    float64
	n      uint64
}

func newHist(bounds []float64) *hist {
	return &hist{bounds: bounds, counts: make([]uint64, len(bounds)+1)}
}

func (h *hist) observe(v float64) {
	i := sort.SearchFloat64s(h.bounds, v)
	h.counts[i]++
	h.sum += v
	h.n++
}

// failure reasons, in the order they are written.
var failReasons = []string{"refused", "timeout", "reset", "unreachable", "other"}

type stats struct {
	connections              uint64
	bytesSent, bytesReceived uint64
	segmentsSent             uint64
	retransmits, rtoRetrans  uint64
	bufferDrops              uint64
	failed                   map[string]uint64
	rtt                      *hist
	lastSeen                 time.Time
	// Whether any report that counted bytes, and any that could not (the conntrack collector without accounting), carried
	// traffic with this peer. The byte counters are only written while they are not known to be zero for want of a counter.
	bytesSeen, bytesMissing bool
	// used is the small table of the addresses the traffic with this peer was seen using, for the prober (see usage.go).
	// It is memory only and no series is made from it.
	used usedAddrs
}

// bytesUnknown says the byte totals are not measurements: only reports that could not count bytes ever arrived.
func (s *stats) bytesUnknown() bool { return s.bytesMissing && !s.bytesSeen }

func newStats() *stats {
	return &stats{failed: map[string]uint64{}, rtt: newHist(rttBuckets)}
}

// Meter labels the traffic the node flow collectors observe by the peer cluster it went to or came from, and keeps
// running totals per peer. Everything exported is counts and distributions per cluster pair: no addresses, ports or
// workloads are labelled; see the cardinality test for how many series a peer costs. The one thing kept about addresses is
// the small in-memory table of which of a peer's addresses its traffic is seen using (usage.go), which only the prober's
// peer list reads.
type Meter struct {
	Peers *Peers
	now   func() time.Time

	mu         sync.Mutex
	per        map[string]*stats
	probing    bool
	probesOnly bool
	probes     map[string]*probeStats
	paths      map[string]*pathStats
	tp         map[string]*tpStats
	connects   map[string]*connectStats
	// timingConnects turns the connect-timing series on.
	timingConnects bool
	// proberPorts are the ports the link prober's own traffic uses; flows to or from them are not the link's traffic.
	proberPorts []portRange
	// ownAddrs are the addresses of the agent's own pod; a connection that starts from one of them is the agent's, not a workload's.
	ownAddrs   map[netip.Addr]bool
	budget     func() (used, limit uint64)
	pairBudget func(peer string) (used, limit uint64, ok bool)

	peersSecret []byte
	peersReplay *probe.ReplayCache
	// proberSeen is when the prober last made a valid request for the peers (zero: never); install is what the chart switched on
	// around this meter. Both are reported to the server as the agent's link capabilities (see capabilities.go).
	proberSeen time.Time
	install    Install

	// The iperf3 server credentials the agent holds for this cluster's responder pods, and the secret and replay cache of the
	// request that fetches them (see iperfapi.go). Memory only.
	iperfSecret []byte
	iperfReplay *probe.ReplayCache
	iperfSrv    *IperfServer
}

// NewMeter reads peers from p.
func NewMeter(p *Peers) *Meter { return &Meter{Peers: p, now: time.Now, per: map[string]*stats{}} }

// portRange is an inclusive range of ports, the unit of parsePorts.
type portRange struct{ lo, hi uint32 }

// parsePorts reads a list of ports and port ranges such as "8861,8862,18900-18999". Empty is no ports.
func parsePorts(spec string) ([]portRange, error) {
	var out []portRange
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(f, "-")
		l, err := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
		h := l
		if err == nil && isRange {
			h, err = strconv.ParseUint(strings.TrimSpace(hi), 10, 16)
		}
		if err != nil || l == 0 || h < l {
			return nil, fmt.Errorf("%q is not a port or a range of ports", f)
		}
		out = append(out, portRange{uint32(l), uint32(h)})
	}
	return out, nil
}

// IgnorePorts makes Observe leave out every flow whose service port is in spec (see parsePorts), whatever the protocol
// and the direction: the link prober's own traffic (the one-way probe's control and test ports, the TCP connect timer, the
// throughput tests) would otherwise count as the link's traffic, and so inflate its connection, round-trip and
// failure series and, worse, read as load on the link to the very throughput tests that cause it.
func (m *Meter) IgnorePorts(spec string) error {
	if m == nil {
		return nil
	}
	r, err := parsePorts(spec)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.proberPorts = r
	m.mu.Unlock()
	return nil
}

// IgnoreOwn makes ObserveRows leave out every flow whose local end is one of the agent's own addresses (a comma-separated list,
// as the chart gives them from the pod's status). The agent dials the addresses of other clusters itself: it tests the targets
// it was given for the measured paths and it holds the stream to the server, which may sit in the peer cluster. Those
// connections are the agent's own, they say nothing about what workloads send across the link, and the ones that fail (an
// address no route leads to) would show as failures and retransmits of the link. Workloads have addresses of their own, so
// theirs are counted as before.
func (m *Meter) IgnoreOwn(spec string) error {
	if m == nil {
		return nil
	}
	own := map[netip.Addr]bool{}
	for _, f := range strings.Split(spec, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return fmt.Errorf("%q is not an address", f)
		}
		own[a.Unmap()] = true
	}
	m.mu.Lock()
	m.ownAddrs = own
	m.mu.Unlock()
	return nil
}

// ownRow says whether the row is a connection of the agent itself. Called with m.mu held.
func (m *Meter) ownRow(r *continuumv1.RawFlow) bool {
	if len(m.ownAddrs) == 0 {
		return false
	}
	a, err := netip.ParseAddr(r.LocalIp)
	return err == nil && m.ownAddrs[a.Unmap()]
}

// Observe adds one flow report's rows, which carry byte counts. See ObserveRows.
func (m *Meter) Observe(rows []*continuumv1.RawFlow) { m.ObserveRows(rows, true) }

// ObserveRows adds one flow report's rows. A row whose far end is not a peer is ignored, and so is one on a port of the link
// prober's own (see IgnorePorts). bytesKnown is the report's own flag: when false (the conntrack collector with no byte
// accounting) the rows' byte counts are zeros, not measurements, so they are not added and the peer's byte counters are not
// written until a report that has them arrives.
//
// Direction: bytes_out is what the caller sent and bytes_in what the callee sent, so what *this* cluster sent is bytes_out
// when it dialed (client) and bytes_in when it accepted. Retransmits and segments are the local socket's own, so they
// always describe what this cluster sent towards the peer.
func (m *Meter) ObserveRows(rows []*continuumv1.RawFlow, bytesKnown bool) {
	if m == nil || m.Peers.Empty() {
		return
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		if r == nil || m.proberPort(r.Port) || m.ownRow(r) {
			continue
		}
		peer := m.Peers.Lookup(r.PeerIp)
		if peer == "" {
			continue
		}
		s := m.per[peer]
		if s == nil {
			s = newStats()
			m.per[peer] = s
		}
		s.lastSeen = now
		s.connections += r.Connections
		var carried uint64
		if bytesKnown {
			carried = r.BytesOut + r.BytesIn
			sent, recv := r.BytesOut, r.BytesIn
			if !r.Client {
				sent, recv = r.BytesIn, r.BytesOut
			}
			s.bytesSent += sent
			s.bytesReceived += recv
			s.bytesSeen = true
		} else {
			s.bytesMissing = true
		}
		s.segmentsSent += uint64(r.SegsOut)
		s.retransmits += uint64(r.Retransmits)
		s.rtoRetrans += uint64(r.RtoRetransmits)
		s.bufferDrops += uint64(r.BufferDrops)
		if r.FailedAttempts > 0 {
			named := r.FailedRefused + r.FailedTimeout + r.FailedReset + r.FailedUnreachable
			s.failed["refused"] += r.FailedRefused
			s.failed["timeout"] += r.FailedTimeout
			s.failed["reset"] += r.FailedReset
			s.failed["unreachable"] += r.FailedUnreachable
			if r.FailedAttempts > named {
				s.failed["other"] += r.FailedAttempts - named
			}
		}
		if r.RttUs > 0 {
			s.rtt.observe(float64(r.RttUs) / 1e6)
		}
		if a, err := netip.ParseAddr(r.PeerIp); err == nil {
			s.used.add(a.Unmap(), rowWeight(r.Connections, carried), now)
		}
	}
}

// proberPort says whether port is one the link prober's own traffic uses. Called with m.mu held.
func (m *Meter) proberPort(port uint32) bool {
	for _, r := range m.proberPorts {
		if port >= r.lo && port <= r.hi {
			return true
		}
	}
	return false
}

// Forget drops the totals of peers that are no longer configured, and from the table of addresses in use those of a peer that
// are no longer among its addresses or have not been seen for an hour.
func (m *Meter) Forget() {
	if m == nil {
		return
	}
	keep := map[string]bool{}
	addrs := map[string]map[netip.Addr]bool{}
	for _, p := range m.Peers.List() {
		keep[p.ID] = true
		addrs[p.ID] = map[netip.Addr]bool{}
		for _, a := range p.Addresses {
			addrs[p.ID][a] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.per {
		if !keep[id] {
			delete(m.per, id)
			continue
		}
		s.used.prune(m.now(), addrs[id])
	}
	probed, traced, tested := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range m.Peers.List() {
		probed[p.ID], traced[p.ID], tested[p.ID] = p.Probed(), p.Traced(), p.Throughput
	}
	for id := range m.paths {
		if !traced[id] {
			delete(m.paths, id)
		}
	}
	for id := range m.tp {
		if !tested[id] {
			delete(m.tp, id)
		}
	}
	for id := range m.connects {
		if !probed[id] {
			delete(m.connects, id)
		}
	}
	for k := range m.probes {
		if id, _, _ := strings.Cut(k, "\x00"); !probed[id] {
			delete(m.probes, k)
		}
	}
}

// OnlyProbes makes the meter write nothing but the active-probe series, for a process (the prober) that runs beside the
// agent: the agent already exports the passive series for the same peers, and two exporters must not claim one series.
func (m *Meter) OnlyProbes() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.probing, m.probesOnly = true, true
	m.mu.Unlock()
}

// EnableProbes makes the meter render the series of the active one-way probe. Without it none are written, so a cluster
// that does not run the probe never reports a loss counter of zero that nothing measured.
func (m *Meter) EnableProbes() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.probing = true
	m.mu.Unlock()
}

func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// Write renders the Prometheus text exposition format.
func (m *Meter) Write(w io.Writer) {
	own := m.Peers.Own()
	peers := m.Peers.List()
	// Everything is rendered into memory first and only then handed to w: w is a client's socket, and a client that reads
	// slowly must not hold the meter's lock (every flow report and every probe result takes it) while it does.
	var buf bytes.Buffer
	m.mu.Lock()
	m.write(&buf, own, peers)
	m.mu.Unlock()
	_, _ = w.Write(buf.Bytes())
}

// write renders the meter's own series. Called with m.mu held.
func (m *Meter) write(w io.Writer, own string, peers []Peer) {
	if m.probesOnly {
		m.writeActive(w, own, peers)
		return
	}
	lbl := func(peer string, extra ...string) string {
		l := fmt.Sprintf(`cluster="%s",peer="%s"`, esc(own), esc(peer))
		for i := 0; i+1 < len(extra); i += 2 {
			l += fmt.Sprintf(`,%s="%s"`, extra[i], esc(extra[i+1]))
		}
		return l
	}
	head := func(name, typ, help string) { fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	head("continuum_link_peer_info", "gauge", "A cluster this one measures its links to; always 1. The name is for display only, series are keyed by the peer id.")
	for _, p := range peers {
		fmt.Fprintf(w, "continuum_link_peer_info{%s,peer_name=\"%s\"} 1\n", lbl(p.ID), esc(p.Name))
	}
	counterFor := func(list []Peer, name, help string, get func(*stats) uint64) {
		head(name, "counter", help)
		for _, p := range list {
			v := uint64(0)
			if s := m.per[p.ID]; s != nil {
				v = get(s)
			}
			fmt.Fprintf(w, "%s{%s} %d\n", name, lbl(p.ID), v)
		}
	}
	counter := func(name, help string, get func(*stats) uint64) { counterFor(peers, name, help, get) }
	counter("continuum_link_connections_total", "Connections observed to or from the peer.", func(s *stats) uint64 { return s.connections })
	// A peer whose reports could not count bytes has no byte series: zero would read as a quiet link.
	known := peers[:0:0]
	for _, p := range peers {
		if s := m.per[p.ID]; s == nil || !s.bytesUnknown() {
			known = append(known, p)
		}
	}
	counterFor(known, "continuum_link_sent_bytes_total", "Bytes this cluster sent to the peer, from observed connections. Absent for a peer whose traffic is seen only by a collector that cannot count bytes (conntrack without nf_conntrack_acct).", func(s *stats) uint64 { return s.bytesSent })
	counterFor(known, "continuum_link_received_bytes_total", "Bytes this cluster received from the peer, from observed connections. Absent for a peer whose traffic is seen only by a collector that cannot count bytes (conntrack without nf_conntrack_acct).", func(s *stats) uint64 { return s.bytesReceived })
	counter("continuum_link_segments_sent_total", "TCP segments this cluster sent towards the peer; the denominator for the retransmit ratio.", func(s *stats) uint64 { return s.segmentsSent })
	counter("continuum_link_retransmits_total", "TCP segments this cluster had to resend towards the peer (the kernel's own counter). The best passive sign of packet loss on the link.", func(s *stats) uint64 { return s.retransmits })
	counter("continuum_link_rto_retransmits_total", "Of the retransmits, those the retransmission timer fired for: nothing came back at all.", func(s *stats) uint64 { return s.rtoRetrans })
	counter("continuum_link_receive_drops_total", "Receive-side socket drops on connections with the peer: the local application not draining, not the network.", func(s *stats) uint64 { return s.bufferDrops })
	head("continuum_link_connection_failures_total", "counter", "Connection attempts to or from the peer that never established, by the kernel's reason.")
	for _, p := range peers {
		for _, r := range failReasons {
			v := uint64(0)
			if s := m.per[p.ID]; s != nil {
				v = s.failed[r]
			}
			fmt.Fprintf(w, "continuum_link_connection_failures_total{%s} %d\n", lbl(p.ID, "reason", r), v)
		}
	}
	histo := func(name, help string, bounds []float64, get func(*stats) *hist) {
		head(name, "histogram", help)
		for _, p := range peers {
			h := newHist(bounds)
			if s := m.per[p.ID]; s != nil {
				h = get(s)
			}
			cum := uint64(0)
			for i, b := range h.bounds {
				cum += h.counts[i]
				fmt.Fprintf(w, "%s_bucket{%s} %d\n", name, lbl(p.ID, "le", trimFloat(b)), cum)
			}
			cum += h.counts[len(h.bounds)]
			fmt.Fprintf(w, "%s_bucket{%s} %d\n", name, lbl(p.ID, "le", "+Inf"), cum)
			fmt.Fprintf(w, "%s_sum{%s} %g\n%s_count{%s} %d\n", name, lbl(p.ID), h.sum, name, lbl(p.ID), h.n)
		}
	}
	histo("continuum_link_rtt_seconds", "Smoothed TCP round-trip time of observed connections to the peer, one sample per connection group per report window.", rttBuckets, func(s *stats) *hist { return s.rtt })
	head("continuum_link_last_observation_timestamp_seconds", "gauge", "When traffic with the peer was last observed. Absent until the first time.")
	for _, p := range peers {
		if s := m.per[p.ID]; s != nil && !s.lastSeen.IsZero() {
			fmt.Fprintf(w, "continuum_link_last_observation_timestamp_seconds{%s} %d\n", lbl(p.ID), s.lastSeen.Unix())
		}
	}
	if m.probing {
		m.writeActive(w, own, peers)
	}
}

// writeActive renders the series of the active measurements, each for the peers it was asked for: the one-way probe and the
// route trace for those not switched off, the throughput tests for those they run towards. A peer that is not measured has no
// series at all, rather than counters that read zero because nothing was ever tried. Called with m.mu held.
func (m *Meter) writeActive(w io.Writer, own string, peers []Peer) {
	var probed, traced, tested []string
	for _, p := range peers {
		if p.Probed() {
			probed = append(probed, p.ID)
		}
		if p.Traced() {
			traced = append(traced, p.ID)
		}
		if p.Throughput {
			tested = append(tested, p.ID)
		}
	}
	m.writeProbes(w, own, probed)
	m.writePaths(w, own, traced)
	m.writeConnects(w, own, probed)
	m.writeThroughput(w, own, tested)
}

func trimFloat(f float64) string { return fmt.Sprintf("%g", f) }
