package link

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
)

// Why a throughput test could not be run or finished (counted), and why one was left for later (counted separately,
// because a skipped test is the scheduler doing its job).
var (
	tpFailReasons = []string{"unreachable", "refused", "timeout", "auth", "other"}
	tpSkipReasons = []string{"link_busy", "budget", "server_busy"}
)

type tpKey struct {
	dir     Direction
	proto   string // "tcp" or "tcp_bidir"
	streams int
	dscp    int
}

type tpVal struct {
	bits    float64
	retrans uint64
	rtt     float64
	hasRTT  bool
}

// UDPStep is one rung of a loss ramp: the rate the sender was asked for and what the receiver saw.
type UDPStep struct {
	Rate   uint64
	Loss   float64
	Jitter float64
}

type udpKey struct {
	dir  Direction
	dscp int
}

type udpRamp struct {
	steps     []UDPStep
	clean     uint64
	hasClean  bool
	saturated bool
}

type tpStats struct {
	tcp     map[tpKey]tpVal
	udp     map[udpKey]udpRamp
	bytes   map[Direction]uint64
	failed  map[string]uint64
	skipped map[string]uint64
	lastOK  time.Time
}

func newTPStats() *tpStats {
	return &tpStats{tcp: map[tpKey]tpVal{}, udp: map[udpKey]udpRamp{}, bytes: map[Direction]uint64{}, failed: map[string]uint64{}, skipped: map[string]uint64{}}
}

// SetBudget tells the meter where to read how much of the throughput tests' byte budget is used, for the exported gauges.
func (m *Meter) SetBudget(f func() (used, limit uint64)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.budget = f
	m.mu.Unlock()
}

func (m *Meter) throughput(peer string) *tpStats {
	s := m.tp[peer]
	if s == nil {
		s = newTPStats()
		if m.tp == nil {
			m.tp = map[string]*tpStats{}
		}
		m.tp[peer] = s
	}
	return s
}

func (m *Meter) knownPeer(peer string) bool {
	for _, p := range m.Peers.List() {
		if p.ID == peer {
			return true
		}
	}
	return false
}

// ObserveTCP records the latest finished TCP test in f's direction. proto is "tcp", or "tcp_bidir" for a test that loaded both directions at once, whose figures are not comparable with a single direction's. Bytes carried are added to the running total.
func (m *Meter) ObserveTCP(peer string, f IperfFlow, proto string, streams, dscp int) {
	if m == nil || !m.knownPeer(peer) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.throughput(peer)
	s.tcp[tpKey{f.Direction, proto, streams, dscp}] = tpVal{bits: f.Bits, retrans: f.Retransmits, rtt: f.RTT, hasRTT: f.HasRTT}
	s.bytes[f.Direction] += f.Bytes
	s.lastOK = m.now()
}

// AddTestBytes counts bytes a test carried in a direction without reporting a reading (the rungs of a ramp are counted
// as they finish, the ramp's reading when it is complete).
func (m *Meter) AddTestBytes(peer string, d Direction, n uint64) {
	if m == nil || n == 0 || !m.knownPeer(peer) {
		return
	}
	m.mu.Lock()
	m.throughput(peer).bytes[d] += n
	m.mu.Unlock()
}

// ObserveUDPRamp replaces the readings of a direction's loss ramp. clean is the highest rate that stayed within the loss
// threshold (hasClean false when even the lowest rung was over it), and saturated says that a higher rung was over it, so
// clean is close to what the link carries rather than only a lower bound.
func (m *Meter) ObserveUDPRamp(peer string, d Direction, dscp int, steps []UDPStep, clean uint64, hasClean, saturated bool) {
	if m == nil || !m.knownPeer(peer) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.throughput(peer)
	s.udp[udpKey{d, dscp}] = udpRamp{steps: append([]UDPStep(nil), steps...), clean: clean, hasClean: hasClean, saturated: saturated}
	s.lastOK = m.now()
}

// ThroughputFailed counts a test that could not be run or finished (anything but a known reason is "other").
func (m *Meter) ThroughputFailed(peer, reason string) {
	m.countTP(peer, reason, tpFailReasons, func(s *tpStats) map[string]uint64 { return s.failed })
}

// ThroughputSkipped counts a test the scheduler held back.
func (m *Meter) ThroughputSkipped(peer, reason string) {
	m.countTP(peer, reason, tpSkipReasons, func(s *tpStats) map[string]uint64 { return s.skipped })
}

func (m *Meter) countTP(peer, reason string, known []string, pick func(*tpStats) map[string]uint64) {
	if m == nil || !m.knownPeer(peer) {
		return
	}
	ok := false
	for _, r := range known {
		ok = ok || r == reason
	}
	if !ok {
		reason = "other"
	}
	m.mu.Lock()
	pick(m.throughput(peer))[reason]++
	m.mu.Unlock()
}

// writeThroughput renders the throughput-test series. Called with m.mu held. Nothing is written until a test has been
// run or counted, so a cluster that does not run them reports none.
func (m *Meter) writeThroughput(w io.Writer, own string, peerIDs []string) {
	if len(m.tp) == 0 && m.budget == nil {
		return
	}
	lbl := func(peer string, extra ...string) string {
		l := fmt.Sprintf(`cluster="%s",peer="%s",method="iperf3"`, esc(own), esc(peer))
		for i := 0; i+1 < len(extra); i += 2 {
			l += fmt.Sprintf(`,%s="%s"`, extra[i], esc(extra[i+1]))
		}
		return l
	}
	head := func(name, typ, help string) { fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	itoa := strconv.Itoa

	// tcp series: one header, every sample of every peer, in a stable order.
	tcpKeys := func(s *tpStats) []tpKey {
		ks := make([]tpKey, 0, len(s.tcp))
		for k := range s.tcp {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool {
			a, b := ks[i], ks[j]
			if a.dir != b.dir {
				return a.dir < b.dir
			}
			if a.streams != b.streams {
				return a.streams < b.streams
			}
			return a.dscp < b.dscp
		})
		return ks
	}
	tcp := func(name, help string, get func(tpVal) (float64, bool)) {
		head(name, "gauge", help)
		for _, p := range peerIDs {
			s := m.tp[p]
			if s == nil {
				continue
			}
			for _, k := range tcpKeys(s) {
				if v, ok := get(s.tcp[k]); ok {
					fmt.Fprintf(w, "%s{%s} %g\n", name, lbl(p, "direction", string(k.dir), "protocol", k.proto, "streams", itoa(k.streams), "dscp", itoa(k.dscp)), v)
				}
			}
		}
	}
	tcp("continuum_link_throughput_bits_per_second", "TCP throughput the latest test achieved in this direction, measured where the data arrived. streams is the number of parallel connections; dscp the class the packets were marked with.", func(v tpVal) (float64, bool) { return v.bits, true })
	tcp("continuum_link_throughput_retransmits", "TCP segments the sender had to resend during the latest test: loss that the test itself provoked, at full rate.", func(v tpVal) (float64, bool) { return float64(v.retrans), true })
	tcp("continuum_link_throughput_rtt_seconds", "Mean TCP round-trip time during the latest test. Compare it with the idle round-trip time: the difference is queueing delay (bufferbloat). Reported for the direction this cluster sends in only.", func(v tpVal) (float64, bool) { return v.rtt, v.hasRTT })

	udpKeys := func(s *tpStats) []udpKey {
		ks := make([]udpKey, 0, len(s.udp))
		for k := range s.udp {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool {
			if ks[i].dir != ks[j].dir {
				return ks[i].dir < ks[j].dir
			}
			return ks[i].dscp < ks[j].dscp
		})
		return ks
	}
	udp := func(name, help string, perStep bool, get func(udpRamp, UDPStep) (float64, bool), ramp func(udpRamp) (float64, bool)) {
		head(name, "gauge", help)
		for _, p := range peerIDs {
			s := m.tp[p]
			if s == nil {
				continue
			}
			for _, k := range udpKeys(s) {
				r := s.udp[k]
				if perStep {
					for _, st := range r.steps {
						if v, ok := get(r, st); ok {
							fmt.Fprintf(w, "%s{%s} %g\n", name, lbl(p, "direction", string(k.dir), "dscp", itoa(k.dscp), "rate_bits_per_second", strconv.FormatUint(st.Rate, 10)), v)
						}
					}
				} else if v, ok := ramp(r); ok {
					fmt.Fprintf(w, "%s{%s} %g\n", name, lbl(p, "direction", string(k.dir), "dscp", itoa(k.dscp)), v)
				}
			}
		}
	}
	udp("continuum_link_throughput_udp_loss_ratio", "Fraction of UDP datagrams lost at each rate of the latest loss ramp. The rate at which this leaves zero is where the link starts to drop.", true,
		func(_ udpRamp, st UDPStep) (float64, bool) { return st.Loss, true }, nil)
	udp("continuum_link_throughput_udp_jitter_seconds", "Receiver jitter at each rate of the latest loss ramp.", true,
		func(_ udpRamp, st UDPStep) (float64, bool) { return st.Jitter, true }, nil)
	udp("continuum_link_throughput_udp_clean_bits_per_second", "The highest UDP rate of the latest ramp that stayed within the loss threshold. Absent if even the lowest rate lost more.", false, nil,
		func(r udpRamp) (float64, bool) { return float64(r.clean), r.hasClean })
	udp("continuum_link_throughput_udp_saturated", "1 if a higher rate than the clean one lost more than the threshold, so the clean rate is near what the link carries; 0 if the ramp ended at its top rate without exceeding it, so the link carries at least that.", false, nil,
		func(r udpRamp) (float64, bool) {
			if r.saturated {
				return 1, true
			}
			return 0, true
		})

	head("continuum_link_throughput_test_bytes_total", "counter", "Bytes the throughput tests have put on the link in this direction: what they cost.")
	for _, p := range peerIDs {
		s := m.tp[p]
		for _, d := range directions {
			v := uint64(0)
			if s != nil {
				v = s.bytes[d]
			}
			fmt.Fprintf(w, "continuum_link_throughput_test_bytes_total{%s} %d\n", lbl(p, "direction", string(d)), v)
		}
	}
	reasons := func(name, help string, known []string, get func(*tpStats) map[string]uint64) {
		head(name, "counter", help)
		for _, p := range peerIDs {
			s := m.tp[p]
			for _, r := range known {
				v := uint64(0)
				if s != nil {
					v = get(s)[r]
				}
				fmt.Fprintf(w, "%s{%s} %d\n", name, lbl(p, "reason", r), v)
			}
		}
	}
	reasons("continuum_link_throughput_test_failures_total", "Throughput tests that could not be run or finished, by reason.", tpFailReasons, func(s *tpStats) map[string]uint64 { return s.failed })
	reasons("continuum_link_throughput_tests_skipped_total", "Throughput tests held back to be tried later: the link was busy with real traffic, the byte budget was used up, or the peer's server was serving another test.", tpSkipReasons, func(s *tpStats) map[string]uint64 { return s.skipped })
	head("continuum_link_throughput_last_success_timestamp_seconds", "gauge", "When a throughput test towards or from the peer last finished. Absent until the first time.")
	for _, p := range peerIDs {
		if s := m.tp[p]; s != nil && !s.lastOK.IsZero() {
			fmt.Fprintf(w, "continuum_link_throughput_last_success_timestamp_seconds{%s} %d\n", lbl(p), s.lastOK.Unix())
		}
	}
	if m.budget != nil {
		used, limit := m.budget()
		cl := fmt.Sprintf(`cluster="%s",method="iperf3"`, esc(own))
		head("continuum_link_throughput_budget_used_bytes", "gauge", "Bytes the throughput tests carried in the budget window, all peers together.")
		fmt.Fprintf(w, "continuum_link_throughput_budget_used_bytes{%s} %d\n", cl, used)
		head("continuum_link_throughput_budget_bytes", "gauge", "The byte budget of the throughput tests per window; 0 for none.")
		fmt.Fprintf(w, "continuum_link_throughput_budget_bytes{%s} %d\n", cl, limit)
	}
}
