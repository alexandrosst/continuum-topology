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
	// Jitter is the packet delay variation the tool reports (95th percentile minus median); HasJitter says it was present.
	Jitter    float64
	HasJitter bool
	// Synchronized is the tool's own judgement that both clocks were disciplined (its SYNC field), and ClockError the error
	// bound it derived from them, in seconds. Absolute delay is only meaningful when Synchronized.
	Synchronized bool
	ClockError   float64
	// Hops is how many routers the packets crossed, from the TTL they arrived with (they leave with 255); 0 when unknown.
	Hops int
}

// initialTTL is the TTL OWAMP sends its test packets with.
const initialTTL = 255

// ParseOwping reads the machine-readable summary `owping -M` prints (KEY, a tab, a value; bucket tables between tags).
// Lines the tool prints around it, such as the "results available" notice, are skipped. It fails when the summary is
// missing or does not say how many packets were sent, so a truncated run is never reported as a clean one.
func ParseOwping(r io.Reader) (OwampResult, error) {
	var res OwampResult
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	sc.Buffer(make([]byte, 0, 64<<10), 256<<10)
	var seenSummary, seenSent, inTable bool
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
		case "SENT":
			if f, ok := num(); ok && f >= 0 {
				res.Sent, seenSent = uint64(f), true
			}
		case "LOST":
			if f, ok := num(); ok && f >= 0 {
				res.Lost = uint64(f)
			}
		case "DUPS":
			if f, ok := num(); ok && f >= 0 {
				res.Duplicates = uint64(f)
			}
		case "SYNC":
			res.Synchronized = val == "1"
		case "MAXERR":
			if f, ok := num(); ok && f >= 0 {
				res.ClockError = f
			}
		case "MIN":
			if f, ok := num(); ok {
				res.Min, res.HasDelay = f, true
			}
		case "MEDIAN":
			if f, ok := num(); ok {
				res.Median = f
			}
		case "MAX":
			if f, ok := num(); ok {
				res.Max = f
			}
		case "PDV":
			if f, ok := num(); ok && f >= 0 {
				res.Jitter, res.HasJitter = f, true
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
	if res.Lost == res.Sent {
		res.HasDelay = false // nothing arrived; any delay printed is not about this run
	}
	if haveTTL {
		res.Hops = initialTTL - minTTL
	}
	return res, nil
}

// Probe failure reasons, in the order they are written.
var probeFailReasons = []string{"unreachable", "refused", "timeout", "auth", "other"}

type probeStats struct {
	sent, lost, dups uint64
	failed           map[string]uint64
	last             *OwampResult
	lastOK           time.Time
}

func probeKey(peer string, d Direction) string { return peer + "\x00" + string(d) }

// ObserveProbe records a finished one-way test towards (Forward) or from (Reverse) a peer. The packet counts are added to
// running totals; the rest is kept as the latest reading.
func (m *Meter) ObserveProbe(peer string, d Direction, r OwampResult) {
	if m == nil || m.Peers.Own() == "" {
		return
	}
	known := false
	for _, p := range m.Peers.List() {
		if p.ID == peer {
			known = true
			break
		}
	}
	if !known {
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
}

// ProbeFailed counts a test that could not be run or finished, by reason (one of the probe failure reasons; anything else
// is "other").
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
	m.probe(peer, d).failed[reason]++
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
	gauge("continuum_link_probe_jitter_seconds", "Packet delay variation in the most recent run (95th percentile minus median of the one-way delay). Valid whether or not the clocks agree, because a constant clock offset cancels out of it.", func(r *OwampResult) []sample { return one(r.Jitter, r.HasJitter) })
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
	gauge("continuum_link_probe_clock_error_seconds", "The bound on the clock error the tool derived for the most recent run.", func(r *OwampResult) []sample { return one(r.ClockError, true) })
	gauge("continuum_link_probe_path_hops", "Routers crossed by the test packets in the most recent run, from the TTL they arrived with.", func(r *OwampResult) []sample { return one(float64(r.Hops), r.Hops > 0) })
	head("continuum_link_probe_last_success_timestamp_seconds", "gauge", "When a probe run in this direction last completed. Absent until the first time.")
	each(func(p string, d Direction, s *probeStats) {
		if s != nil && !s.lastOK.IsZero() {
			fmt.Fprintf(w, "continuum_link_probe_last_success_timestamp_seconds{%s} %d\n", lbl(p, d), s.lastOK.Unix())
		}
	})
}
