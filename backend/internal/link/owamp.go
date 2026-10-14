package link

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Direction is which way a one-way measurement ran, from this cluster's point of view.
type Direction string

const (
	// Forward is from this cluster to the peer.
	Forward Direction = "forward"
	// Reverse is from the peer to this cluster.
	Reverse Direction = "reverse"
)

// Directions in the order they are written.
var directions = []Direction{Forward, Reverse}

// OwampResult is one finished one-way test as `owping -M` summarises it: a number of packets sent in one direction at a
// steady rate, and what became of them. Delays are seconds. HasDelay is false when no packet arrived.
type OwampResult struct {
	Sent, Lost, Duplicates uint64
	HasDelay               bool
	Min, Median, Max       float64
	// Synchronized is the tool's own judgement that both clocks were disciplined (its SYNC field), and ClockError the error
	// bound it derived from them, in seconds (HasClockError says it was printed). Absolute delay is only meaningful when
	// Synchronized.
	Synchronized  bool
	ClockError    float64
	HasClockError bool
	// Hops is how many routers the packets crossed, from the TTL they arrived with (they leave with 255); 0 when unknown.
	Hops int
}

// initialTTL is the TTL OWAMP sends its test packets with.
const initialTTL = 255

// maxOwampCount bounds the packet counts read from the summary, so a corrupt value can never wrap a counter. A run is at most
// 10000 packets.
const maxOwampCount = math.MaxInt32

// ParseOwping reads the machine-readable summary `owping -M` prints (KEY, a tab, a value; bucket tables between tags).
// Lines the tool prints around it, such as the "results available" notice, are skipped. It fails when the summary is
// missing or does not say how many packets were sent, so a truncated run is never reported as a clean one.
func ParseOwping(r io.Reader) (OwampResult, error) {
	var res OwampResult
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	sc.Buffer(make([]byte, 0, 64<<10), 256<<10)
	var seenSummary, seenSent, inTable, haveMin, haveMed, haveMax bool
	var minTTL int
	var haveTTL bool
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(line, "<") {
			inTable = !strings.HasPrefix(line, "</")
			continue
		}
		if inTable {
			continue
		}
		key, val, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if key == "SUMMARY" {
			seenSummary = true
			continue
		}
		if !seenSummary {
			continue
		}
		num := func() (float64, bool) {
			f, err := strconv.ParseFloat(val, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return 0, false
			}
			return f, true
		}
		switch key {
		case "SENT", "LOST", "DUPS":
			f, ok := num()
			if !ok {
				break
			}
			if f < 0 || f > maxOwampCount {
				return OwampResult{}, fmt.Errorf("owping reports an implausible %s of %v", key, f)
			}
			switch key {
			case "SENT":
				res.Sent, seenSent = uint64(f), true
			case "LOST":
				res.Lost = uint64(f)
			default:
				res.Duplicates = uint64(f)
			}
		case "SYNC":
			res.Synchronized = val == "1"
		case "MAXERR":
			if f, ok := num(); ok && f >= 0 {
				res.ClockError, res.HasClockError = f, true
			}
		case "MIN":
			if f, ok := num(); ok {
				res.Min, haveMin = f, true
			}
		case "MEDIAN":
			if f, ok := num(); ok {
				res.Median, haveMed = f, true
			}
		case "MAX":
			if f, ok := num(); ok {
				res.Max, haveMax = f, true
			}
		case "MINTTL":
			if f, ok := num(); ok && f > 0 && f <= initialTTL {
				minTTL, haveTTL = int(f), true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return OwampResult{}, err
	}
	if !seenSummary || !seenSent {
		return OwampResult{}, fmt.Errorf("owping printed no summary")
	}
	if res.Lost > res.Sent {
		return OwampResult{}, fmt.Errorf("owping reports %d lost of %d sent", res.Lost, res.Sent)
	}
	// The delay figures count only as a set: a partial or out-of-order one is dropped, and the loss counts are kept.
	res.HasDelay = haveMin && haveMed && haveMax && res.Min <= res.Median && res.Median <= res.Max
	if res.Lost == res.Sent {
		res.HasDelay = false // nothing arrived; any delay printed is not about this run
	}
	if !res.HasDelay {
		res.Min, res.Median, res.Max = 0, 0, 0
	}
	if haveTTL {
		res.Hops = initialTTL - minTTL
	}
	return res, nil
}

// Probe failure reasons, in the order they are written.
var probeFailReasons = []string{"unreachable", "refused", "timeout", "auth", "other", ReasonNoKind}

type probeStats struct {
	sent, lost, dups uint64
	failed           map[string]uint64
	// last is the latest run's reading; nil when there is none or the latest attempt failed, so a dead link never keeps
	// showing the figures of its last good run.
	last   *OwampResult
	lastOK time.Time
	// tried says an attempt has been recorded, and failedLast that the latest one failed.
	tried, failedLast bool
}

func probeKey(peer string, d Direction) string { return peer + "\x00" + string(d) }

// ObserveProbe records a finished one-way test towards (Forward) or from (Reverse) a peer. The packet counts are added to
// running totals; the rest is kept as the latest reading.
func (m *Meter) ObserveProbe(peer string, d Direction, r OwampResult) {
	if m == nil || m.Peers.Own() == "" {
		return
	}
	if !m.Peers.Has(peer) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.probe(peer, d)
	s.sent += r.Sent
	s.lost += r.Lost
	s.dups += r.Duplicates
	cp := r
	s.last = &cp
	s.lastOK = m.now()
	s.tried, s.failedLast = true, false
}

// ProbeFailed counts a test that could not be run or finished, by reason (one of the probe failure reasons; anything else
// is "other"), and withdraws the per-run readings of that direction: they described a run that is no longer the latest.
func (m *Meter) ProbeFailed(peer string, d Direction, reason string) {
	if m == nil {
		return
	}
	ok := false
	for _, r := range probeFailReasons {
		if r == reason {
			ok = true
		}
	}
	if !ok {
		reason = "other"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.probe(peer, d)
	s.failed[reason]++
	s.last = nil
	s.tried, s.failedLast = true, true
}

func (m *Meter) probe(peer string, d Direction) *probeStats {
	k := probeKey(peer, d)
	s := m.probes[k]
	if s == nil {
		s = &probeStats{failed: map[string]uint64{}}
		if m.probes == nil {
			m.probes = map[string]*probeStats{}
		}
		m.probes[k] = s
	}
	return s
}

// writeProbes renders the active-probe series. Called with m.mu held.
func (m *Meter) writeProbes(w io.Writer, own string, peerIDs []string) {
	lbl := func(peer string, d Direction, extra ...string) string {
		l := fmt.Sprintf(`cluster="%s",peer="%s",direction="%s",method="owamp"`, esc(own), esc(peer), d)
		for i := 0; i+1 < len(extra); i += 2 {
			l += fmt.Sprintf(`,%s="%s"`, extra[i], esc(extra[i+1]))
		}
		return l
	}
	head := func(name, typ, help string) { fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	each := func(f func(peer string, d Direction, s *probeStats)) {
		for _, p := range peerIDs {
			for _, d := range directions {
				f(p, d, m.probes[probeKey(p, d)])
			}
		}
	}
	counter := func(name, help string, get func(*probeStats) uint64) {
		head(name, "counter", help)
		each(func(p string, d Direction, s *probeStats) {
			v := uint64(0)
			if s != nil {
				v = get(s)
			}
			fmt.Fprintf(w, "%s{%s} %d\n", name, lbl(p, d), v)
		})
	}
	// sample is one value of a gauge family, with the stat label it carries (empty for none).
	type sample struct {
		label string
		v     float64
	}
	// gauge writes every sample a series' latest reading yields, under one HELP/TYPE header.
	gauge := func(name, help string, get func(*OwampResult) []sample) {
		head(name, "gauge", help)
		each(func(p string, d Direction, s *probeStats) {
			if s == nil || s.last == nil {
				return
			}
			for _, sm := range get(s.last) {
				if sm.label == "" {
					fmt.Fprintf(w, "%s{%s} %g\n", name, lbl(p, d), sm.v)
				} else {
					fmt.Fprintf(w, "%s{%s} %g\n", name, lbl(p, d, "stat", sm.label), sm.v)
				}
			}
		})
	}
	one := func(v float64, ok bool) []sample {
		if !ok {
			return nil
		}
		return []sample{{v: v}}
	}
	counter("continuum_link_probe_packets_sent_total", "Test packets sent in this direction by the active one-way probe.", func(s *probeStats) uint64 { return s.sent })
	counter("continuum_link_probe_packets_lost_total", "Test packets that never arrived, counted per direction from sequence numbers. rate(lost)/rate(sent) is the loss on that direction alone.", func(s *probeStats) uint64 { return s.lost })
	counter("continuum_link_probe_packets_duplicated_total", "Test packets that arrived more than once.", func(s *probeStats) uint64 { return s.dups })
	head("continuum_link_probe_failures_total", "counter", "Probe runs that could not be completed, by reason.")
	each(func(p string, d Direction, s *probeStats) {
		for _, r := range probeFailReasons {
			v := uint64(0)
			if s != nil {
				v = s.failed[r]
			}
			fmt.Fprintf(w, "continuum_link_probe_failures_total{%s} %d\n", lbl(p, d, "reason", r), v)
		}
	})
	gauge("continuum_link_probe_loss_ratio", "Fraction of test packets lost in the most recent run in this direction.", func(r *OwampResult) []sample {
		if r.Sent == 0 {
			return nil
		}
		return one(float64(r.Lost)/float64(r.Sent), true)
	})
	gauge("continuum_link_probe_delay_variation_seconds", "How far the one-way delay rose above its minimum in the most recent run. Valid whether or not the clocks agree.", func(r *OwampResult) []sample {
		if !r.HasDelay {
			return nil
		}
		return []sample{{"median", r.Median - r.Min}, {"max", r.Max - r.Min}}
	})
	gauge("continuum_link_probe_one_way_delay_seconds", "Absolute one-way delay in the most recent run. Present only when both clocks were synchronised, because otherwise it includes the offset between them.", func(r *OwampResult) []sample {
		if !r.HasDelay || !r.Synchronized {
			return nil
		}
		return []sample{{"min", r.Min}, {"median", r.Median}, {"max", r.Max}}
	})
	gauge("continuum_link_probe_clock_synchronized", "1 if both ends had a synchronised clock during the most recent run, so absolute one-way delay can be trusted.", func(r *OwampResult) []sample {
		if r.Synchronized {
			return one(1, true)
		}
		return one(0, true)
	})
	gauge("continuum_link_probe_clock_error_seconds", "The bound on the clock error the tool derived for the most recent run. Present only when the clocks were synchronised.", func(r *OwampResult) []sample { return one(r.ClockError, r.HasClockError && r.Synchronized) })
	gauge("continuum_link_probe_path_hops", "Routers crossed by the test packets in the most recent run, from the TTL they arrived with.", func(r *OwampResult) []sample { return one(float64(r.Hops), r.Hops > 0) })
	head("continuum_link_probe_up", "gauge", "1 if the latest probe run in this direction completed, 0 if it failed (the per-run gauges are then absent). Absent until the first attempt.")
	each(func(p string, d Direction, s *probeStats) {
		if s != nil && s.tried {
			v := 1
			if s.failedLast {
				v = 0
			}
			fmt.Fprintf(w, "continuum_link_probe_up{%s} %d\n", lbl(p, d), v)
		}
	})
	head("continuum_link_probe_last_success_timestamp_seconds", "gauge", "When a probe run in this direction last completed. Absent until the first time.")
	each(func(p string, d Direction, s *probeStats) {
		if s != nil && !s.lastOK.IsZero() {
			fmt.Fprintf(w, "continuum_link_probe_last_success_timestamp_seconds{%s} %d\n", lbl(p, d), s.lastOK.Unix())
		}
	})
}

// writePaths renders the path-probe series. Called with m.mu held.
func (m *Meter) writePaths(w io.Writer, own string, peerIDs []string) {
	lbl := func(peer string) string { return fmt.Sprintf(`cluster="%s",peer="%s"`, esc(own), esc(peer)) }
	head := func(name, typ, help string) { fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	gauge := func(name, help string, get func(*PathResult) (float64, bool)) {
		head(name, "gauge", help)
		for _, p := range peerIDs {
			if s := m.paths[p]; s != nil && s.last != nil {
				if v, ok := get(s.last); ok {
					fmt.Fprintf(w, "%s{%s} %g\n", name, lbl(p), v)
				}
			}
		}
	}
	b2f := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	gauge("continuum_link_path_mtu_bytes", "The largest packet that fits the whole way to the peer, as the routers on the path reported it. Absent until learned.", func(r *PathResult) (float64, bool) { return float64(r.MTU), r.MTU > 0 })
	gauge("continuum_link_path_reached", "1 if the probe's last step was answered by the peer's address. Networks that drop the answer report 0 without being broken.", func(r *PathResult) (float64, bool) { return b2f(r.Reached), true })
	gauge("continuum_link_path_mtu_blackhole", "1 if a router passed small packets and silently dropped full-size ones: the path's MTU is smaller than this host believes and nothing said so, so large packets vanish.", func(r *PathResult) (float64, bool) { return b2f(r.Blackhole), true })
	head("continuum_link_path_changes_total", "counter", "Times the route to the peer changed, counted when a new route has been seen on two probes in a row. The route itself is not exported.")
	for _, p := range peerIDs {
		v := uint64(0)
		if s := m.paths[p]; s != nil {
			v = s.changes
		}
		fmt.Fprintf(w, "continuum_link_path_changes_total{%s} %d\n", lbl(p), v)
	}
	head("continuum_link_path_probe_failures_total", "counter", "Path probes that could not be run.")
	for _, p := range peerIDs {
		v := uint64(0)
		if s := m.paths[p]; s != nil {
			v = s.failed
		}
		fmt.Fprintf(w, "continuum_link_path_probe_failures_total{%s} %d\n", lbl(p), v)
	}
	head("continuum_link_path_last_success_timestamp_seconds", "gauge", "When the path to the peer was last probed. Absent until the first time.")
	for _, p := range peerIDs {
		if s := m.paths[p]; s != nil && !s.lastOK.IsZero() {
			fmt.Fprintf(w, "continuum_link_path_last_success_timestamp_seconds{%s} %d\n", lbl(p), s.lastOK.Unix())
		}
	}
}
