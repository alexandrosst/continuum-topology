package link

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/probe"
)

// Bucket upper bounds, in seconds. Explicit buckets keep the series count fixed whatever the traffic does.
var (
	rttBuckets       = []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2.5}
	jitterBuckets    = []float64{0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25}
	handshakeBuckets = rttBuckets
)

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
	flows, connections       uint64
	bytesSent, bytesReceived uint64
	segmentsSent             uint64
	retransmits, rtoRetrans  uint64
	bufferDrops              uint64
	failed                   map[string]uint64
	rtt, jitter, handshake   *hist
	lastSeen                 time.Time
}

func newStats() *stats {
	return &stats{failed: map[string]uint64{}, rtt: newHist(rttBuckets), jitter: newHist(jitterBuckets), handshake: newHist(handshakeBuckets)}
}

// Meter labels the traffic the node flow collectors observe by the peer cluster it went to or came from, and keeps
// running totals per peer. Everything is counts and distributions per cluster pair: no addresses, ports or workloads are
// kept, so the number of series depends only on how many peers are configured.
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
	budget     func() (used, limit uint64)

	peersSecret []byte
	peersReplay *probe.ReplayCache
}

// NewMeter reads peers from p.
func NewMeter(p *Peers) *Meter { return &Meter{Peers: p, now: time.Now, per: map[string]*stats{}} }

// Observe adds one flow report's rows. A row whose far end is not a peer is ignored.
//
// Direction: bytes_out is what the caller sent and bytes_in what the callee sent, so what *this* cluster sent is bytes_out
// when it dialed (client) and bytes_in when it accepted. Retransmits and segments are the local socket's own, so they
// always describe what this cluster sent towards the peer.
func (m *Meter) Observe(rows []*continuumv1.RawFlow) {
	if m == nil || m.Peers.Empty() {
		return
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		if r == nil {
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
		s.flows++
		s.connections += r.Connections
		sent, recv := r.BytesOut, r.BytesIn
		if !r.Client {
			sent, recv = r.BytesIn, r.BytesOut
		}
		s.bytesSent += sent
		s.bytesReceived += recv
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
		if r.JitterUs > 0 {
			s.jitter.observe(float64(r.JitterUs) / 1e6)
		}
		if r.HandshakeUs > 0 {
			s.handshake.observe(float64(r.HandshakeUs) / 1e6)
		}
	}
}

// Forget drops the totals of peers that are no longer configured.
func (m *Meter) Forget() {
	if m == nil {
		return
	}
	keep := map[string]bool{}
	for _, p := range m.Peers.List() {
		keep[p.ID] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.per {
		if !keep[id] {
			delete(m.per, id)
		}
	}
	for id := range m.paths {
		if !keep[id] {
			delete(m.paths, id)
		}
	}
	for id := range m.tp {
		if !keep[id] {
			delete(m.tp, id)
		}
	}
	for k := range m.probes {
		if id, _, _ := strings.Cut(k, "\x00"); !keep[id] {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.probesOnly {
		ids := make([]string, len(peers))
		for i, p := range peers {
			ids[i] = p.ID
		}
		m.writeProbes(w, own, ids)
		m.writePaths(w, own, ids)
		m.writeThroughput(w, own, ids)
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
	counter := func(name, help string, get func(*stats) uint64) {
		head(name, "counter", help)
		for _, p := range peers {
			v := uint64(0)
			if s := m.per[p.ID]; s != nil {
				v = get(s)
			}
			fmt.Fprintf(w, "%s{%s} %d\n", name, lbl(p.ID), v)
		}
	}
	counter("continuum_link_flows_total", "Flow rows observed to or from the peer (one per connection group per report window).", func(s *stats) uint64 { return s.flows })
	counter("continuum_link_connections_total", "Connections observed to or from the peer.", func(s *stats) uint64 { return s.connections })
	counter("continuum_link_sent_bytes_total", "Bytes this cluster sent to the peer, from observed connections.", func(s *stats) uint64 { return s.bytesSent })
	counter("continuum_link_received_bytes_total", "Bytes this cluster received from the peer, from observed connections.", func(s *stats) uint64 { return s.bytesReceived })
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
	histo("continuum_link_jitter_seconds", "Mean deviation of the round-trip time (the kernel's mdev) on observed connections to the peer.", jitterBuckets, func(s *stats) *hist { return s.jitter })
	histo("continuum_link_handshake_seconds", "Time to establish observed connections to the peer, from first SYN to established.", handshakeBuckets, func(s *stats) *hist { return s.handshake })
	head("continuum_link_last_observation_timestamp_seconds", "gauge", "When traffic with the peer was last observed. Absent until the first time.")
	for _, p := range peers {
		if s := m.per[p.ID]; s != nil && !s.lastSeen.IsZero() {
			fmt.Fprintf(w, "continuum_link_last_observation_timestamp_seconds{%s} %d\n", lbl(p.ID), s.lastSeen.Unix())
		}
	}
	if m.probing {
		ids := make([]string, len(peers))
		for i, p := range peers {
			ids[i] = p.ID
		}
		m.writeProbes(w, own, ids)
		m.writePaths(w, own, ids)
		m.writeThroughput(w, own, ids)
	}
}

func trimFloat(f float64) string { return fmt.Sprintf("%g", f) }
