package link

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Gate lets one measurement at a time use the link, whichever tool runs it: a throughput test would otherwise show up as
// loss and jitter in the one-way probe running beside it. Waiters are served in order. A nil Gate lets everyone through.
type Gate struct{ ch chan struct{} }

// NewGate makes a gate.
func NewGate() *Gate { return &Gate{ch: make(chan struct{}, 1)} }

// Acquire waits for the turn and returns what gives it back.
func (g *Gate) Acquire(ctx context.Context) (release func(), err error) {
	if g == nil {
		return func() {}, nil
	}
	select {
	case g.ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-g.ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Ledger counts the bytes the tests carried over a sliding window, to keep them within a budget.
type Ledger struct {
	Window time.Duration
	Limit  uint64 // 0: no limit

	mu      sync.Mutex
	entries []ledgerEntry
}

type ledgerEntry struct {
	at time.Time
	n  uint64
}

func (l *Ledger) trim(now time.Time) {
	i := 0
	for i < len(l.entries) && now.Sub(l.entries[i].at) >= l.Window {
		i++
	}
	l.entries = l.entries[i:]
}

// Used is what the window holds at now.
func (l *Ledger) Used(now time.Time) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trim(now)
	var t uint64
	for _, e := range l.entries {
		t += e.n
	}
	return t
}

// Charge adds bytes carried at now.
func (l *Ledger) Charge(now time.Time, n uint64) {
	if n == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trim(now)
	l.entries = append(l.entries, ledgerEntry{now, n})
}

// Allows says whether a test expected to carry est bytes fits what is left of the budget.
func (l *Ledger) Allows(now time.Time, est uint64) bool {
	if l.Limit == 0 {
		return true
	}
	return l.Used(now)+est <= l.Limit
}

// TimeWindow is a daily span (UTC) in which the tests may run; the zero value is always.
type TimeWindow struct {
	From, To time.Duration // offsets from midnight UTC; the span may cross midnight
	Set      bool
}

// ParseTimeWindow reads "HH:MM-HH:MM" (UTC); "" is always.
func ParseTimeWindow(s string) (TimeWindow, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return TimeWindow{}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return TimeWindow{}, fmt.Errorf("window %q is not HH:MM-HH:MM", s)
	}
	f, err1 := parseClock(a)
	t, err2 := parseClock(b)
	if err1 != nil || err2 != nil || f == t {
		return TimeWindow{}, fmt.Errorf("window %q is not HH:MM-HH:MM with two different times", s)
	}
	return TimeWindow{From: f, To: t, Set: true}, nil
}

func parseClock(s string) (time.Duration, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hh, e1 := strconv.Atoi(h)
	mm, e2 := strconv.Atoi(m)
	if !ok || e1 != nil || e2 != nil || len(h) != 2 || len(m) != 2 || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	return time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute, nil
}

func sinceMidnight(t time.Time) time.Duration {
	t = t.UTC()
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second
}

// Contains says whether t is inside the window.
func (w TimeWindow) Contains(t time.Time) bool {
	if !w.Set {
		return true
	}
	d := sinceMidnight(t)
	if w.From < w.To {
		return d >= w.From && d < w.To
	}
	return d >= w.From || d < w.To
}

// Opens is how long until the window next opens (zero if it is open).
func (w TimeWindow) Opens(t time.Time) time.Duration {
	if w.Contains(t) {
		return 0
	}
	d := w.From - sinceMidnight(t)
	if d < 0 {
		d += 24 * time.Hour
	}
	return d
}

// Load turns the agent's running byte counters per peer into the rate of the traffic now crossing each link, so that a
// throughput test does not start on top of it.
type Load struct {
	mu   sync.Mutex
	last map[string]loadSample
	rate map[string]LoadRate
}

type loadSample struct {
	at         time.Time
	sent, recv uint64
}

// LoadRate is the observed traffic with a peer, in bits per second, over the interval ending at At.
type LoadRate struct {
	Sent, Received float64
	At             time.Time
}

// Observe takes the counters of a peers document.
func (l *Load) Observe(d PeersDoc, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last, l.rate = map[string]loadSample{}, map[string]LoadRate{}
	}
	seen := map[string]bool{}
	for _, p := range d.Peers {
		seen[p.ID] = true
		prev, had := l.last[p.ID]
		cur := loadSample{now, p.BytesSent, p.BytesReceived}
		l.last[p.ID] = cur
		dt := now.Sub(prev.at).Seconds()
		// A counter that went backwards means the agent restarted: wait for the next sample.
		if !had || dt < 5 || cur.sent < prev.sent || cur.recv < prev.recv {
			continue
		}
		l.rate[p.ID] = LoadRate{Sent: float64(cur.sent-prev.sent) * 8 / dt, Received: float64(cur.recv-prev.recv) * 8 / dt, At: now}
	}
	for id := range l.last {
		if !seen[id] {
			delete(l.last, id)
			delete(l.rate, id)
		}
	}
}

// Rate returns the latest rate if it is not older than maxAge.
func (l *Load) Rate(peer string, now time.Time, maxAge time.Duration) (LoadRate, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.rate[peer]
	if !ok || now.Sub(r.At) > maxAge {
		return LoadRate{}, false
	}
	return r, true
}
