package server

import (
	"container/list"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// Limiter is a small per-key token bucket. It protects the unauthenticated
// enrollment endpoints from token guessing and CSR spam.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	max     int // most keys remembered at once
	buckets map[string]*bucket
	order   *list.List // *bucket, most recently used first
	now     func() time.Time

	lastSweep time.Time
}

// maxBuckets bounds memory under a spray of source addresses. Past it the key used least recently is
// forgotten to make room, so a spray of one-off addresses can never lock a legitimate user out (a
// forgotten key simply starts again with a full bucket, which is what an unknown key gets anyway; an
// address that is being throttled is by definition recent and is the last to go).
const maxBuckets = 50000

type bucket struct {
	key    string
	tokens float64
	last   time.Time
	elem   *list.Element
}

func NewLimiter(perMinute int, burst int) *Limiter {
	return &Limiter{rate: float64(perMinute) / 60, burst: float64(burst), max: maxBuckets, buckets: map[string]*bucket{}, order: list.New(), now: time.Now}
}

// Allow consumes one token for key, reporting whether the call may proceed.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		l.sweep(now)
		for len(l.buckets) >= l.max {
			l.evictOldest()
		}
		b = &bucket{key: key, tokens: l.burst, last: now}
		b.elem = l.order.PushFront(b)
		l.buckets[key] = b
	} else {
		l.order.MoveToFront(b.elem)
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		Metrics.rateLimited.Add(1)
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) evictOldest() {
	if e := l.order.Back(); e != nil {
		l.remove(e.Value.(*bucket))
		return
	}
	l.buckets = map[string]*bucket{} // the list and the map disagree: start over rather than loop
}

func (l *Limiter) remove(b *bucket) {
	l.order.Remove(b.elem)
	delete(l.buckets, b.key)
}

// sweep drops buckets that have been idle long enough to be full again; a fresh bucket is
// identical, so forgetting them changes nothing. It runs at most once a second.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Second {
		return
	}
	l.lastSweep = now
	refill := time.Duration(l.burst / l.rate * float64(time.Second))
	// The list is ordered by last use, so the idle ones are at the back.
	for e := l.order.Back(); e != nil; e = l.order.Back() {
		b := e.Value.(*bucket)
		if now.Sub(b.last) <= refill {
			return
		}
		l.remove(b)
	}
}

// LimitKey is what a rate limit keyed by network address should use in place of the address itself. An
// IPv6 host controls a whole /64 (often a /48 or more), so counting single addresses would let one
// machine mint a fresh allowance for every request; IPv4 addresses, and IPv4-mapped IPv6 ones, are used as they are.
func LimitKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.WithZone("").Unmap()
	if a.Is6() {
		p, err := a.Prefix(64)
		if err != nil {
			return a.String()
		}
		return p.String()
	}
	return a.String()
}

// failureTracker slows down guessing of one account's password from many addresses at once, which the
// per-address limits cannot see. After failFree consecutive failures each further one doubles the wait
// (1 s, 2 s, 4 s, ...) up to failCap. The counter is per name as typed, existing or not, and forgets a
// name failFor after its last failure; a correct password resets it. Only the most recent maxFailureNames
// are kept, so memory stays bounded however many names are tried.
//
// The trade-off is that someone who knows a name can keep that account waiting by failing on purpose; the
// cap keeps that to a few minutes, and the wait never applies to a request that is not a sign-in.
type failureTracker struct {
	mu    sync.Mutex
	names map[string]*failure
	order *list.List
	max   int
}

type failure struct {
	name  string
	count int
	until time.Time
	last  time.Time
	elem  *list.Element
}

const (
	failFree         = 4
	failCap          = 5 * time.Minute
	failFor          = time.Hour
	maxFailureNames  = 100000
	failBaseInterval = time.Second
)

func newFailureTracker() *failureTracker {
	return &failureTracker{names: map[string]*failure{}, order: list.New(), max: maxFailureNames}
}

// blocked returns how long this name must still wait (0: not at all).
func (t *failureTracker) blocked(name string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.names[name]
	if f == nil {
		return 0
	}
	if now.Sub(f.last) > failFor {
		t.drop(f)
		return 0
	}
	if now.Before(f.until) {
		return f.until.Sub(now)
	}
	return 0
}

// fail records a failed attempt and returns the wait it earned.
func (t *failureTracker) fail(name string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.names[name]
	if f == nil || now.Sub(f.last) > failFor {
		if f != nil {
			t.drop(f)
		}
		for len(t.names) >= t.max {
			if e := t.order.Back(); e != nil {
				t.drop(e.Value.(*failure))
			}
		}
		f = &failure{name: name}
		f.elem = t.order.PushFront(f)
		t.names[name] = f
	} else {
		t.order.MoveToFront(f.elem)
	}
	f.count++
	f.last = now
	var wait time.Duration
	if f.count > failFree {
		shift := f.count - failFree - 1
		wait = failCap
		if shift < 20 {
			if w := failBaseInterval << shift; w < failCap {
				wait = w
			}
		}
	}
	f.until = now.Add(wait)
	return wait
}

func (t *failureTracker) succeed(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if f := t.names[name]; f != nil {
		t.drop(f)
	}
}

func (t *failureTracker) drop(f *failure) {
	t.order.Remove(f.elem)
	delete(t.names, f.name)
}

// roundWait words a wait for a person: whole seconds, or minutes once it is long.
func roundWait(d time.Duration) string {
	s := int(d.Seconds() + 0.999)
	if s < 1 {
		s = 1
	}
	if s >= 120 {
		return fmt.Sprintf("%d minutes", (s+59)/60)
	}
	return fmt.Sprintf("%d seconds", s)
}
