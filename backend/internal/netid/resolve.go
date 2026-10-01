package netid

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// lookupAddr does the actual reverse-DNS (PTR) query. A package-level var, not a direct net.DefaultResolver
// call, so tests can substitute a stub and never touch the real network or a real timeout. Guarded by its
// own mutex (not cacheMu) since it's read from a background goroutine (resolve) that must not hold cacheMu
// for the duration of a network call, and written by SetLookupAddrForTest, potentially while such a
// goroutine is still in flight from a previous test.
var (
	lookupAddrMu sync.Mutex
	lookupAddr   = func(ctx context.Context, ip string) ([]string, error) {
		return net.DefaultResolver.LookupAddr(ctx, ip)
	}
)

const (
	resolveTimeout = 3 * time.Second  // bounds one query; a hung/slow resolver can't leak a goroutine forever
	positiveTTL    = 24 * time.Hour   // a PTR record rarely changes; no need to re-ask often
	negativeTTL    = 10 * time.Minute // a failed/empty lookup is retried sooner, in case it was transient
	maxInFlight    = 8                // caps concurrent outbound DNS queries across all orgs sharing this process

	// maxCacheEntries bounds the cache's total memory. Nothing else shrinks it: an entry is kept forever
	// once resolved, even long after the IP that earned it stops appearing in any topology, because
	// ResolveCached has no signal for "nobody asks about this address anymore" - only resolve() ever
	// touches an entry again, and only if the same IP comes back. A server that runs for weeks and sees a
	// long tail of distinct external addresses would otherwise grow this map without limit. evictLocked
	// enforces the cap instead; evictTargetFrac is how far under the cap it brings things back down to, so
	// a server sitting right at the cap doesn't re-scan the whole map on every single resolve().
	maxCacheEntries = 10000
	evictTargetFrac = 0.9
)

type cacheEntry struct {
	host    string
	ok      bool
	expires time.Time
}

var (
	cacheMu  sync.Mutex
	cache    = map[string]cacheEntry{}
	inFlight = map[string]bool{}
	sem      = make(chan struct{}, maxInFlight)
)

// ResolveCached returns the reverse-DNS hostname for ip if it is already known, and never blocks on the
// network to find out: observedTopology (this package's only caller, via ExternalHost below) runs on every
// state poll from every connected browser session, so a live DNS round-trip on that path would add
// unpredictable, shared latency to a request several people are waiting on. A cache miss instead starts a
// resolution in the background - bounded by maxInFlight so a burst of unfamiliar IPs can't spawn unbounded
// goroutines or queries, and deduplicated per IP so repeated polls for the same still-resolving address
// don't pile up redundant lookups - and returns ok=false for this call. The next poll (typically a few
// seconds later, per this app's existing poll-driven design) picks up the answer once it lands. A stale
// cache entry is still served immediately while a background refresh runs, so an expiring TTL never
// reintroduces a blocking wait either.
func ResolveCached(ip string) (host string, ok bool) {
	cacheMu.Lock()
	e, hit := cache[ip]
	stale := hit && time.Now().After(e.expires)
	alreadyRunning := inFlight[ip]
	needsStart := (!hit || stale) && !alreadyRunning
	if needsStart {
		inFlight[ip] = true
	}
	cacheMu.Unlock()

	if needsStart {
		select {
		case sem <- struct{}{}:
			// Captured here, not inside resolve: resolve runs in its own goroutine that this call doesn't
			// wait for, and SetLookupAddrForTest can swap the package-level lookupAddr (and reset cache/
			// inFlight) for a *later* test while this goroutine is still scheduled but hasn't run yet under
			// heavy load. Reading the package var fresh inside resolve would let that stale goroutine fire
			// a later test's stub instead of the one active when it was spawned, inflating that later
			// test's call count - this is exactly what a real, intermittent CI failure under -race traced
			// back to. Snapshotting the function reference at spawn time ties each goroutine permanently to
			// whichever lookupAddr was current when ResolveCached decided to start it.
			lookupAddrMu.Lock()
			lookup := lookupAddr
			lookupAddrMu.Unlock()
			go resolve(ip, lookup)
		default:
			// Already at maxInFlight: drop this attempt rather than queue it unboundedly. It'll be retried
			// next time this IP shows up, which - given the poll cadence - is soon.
			cacheMu.Lock()
			delete(inFlight, ip)
			cacheMu.Unlock()
		}
	}
	return e.host, hit && e.ok
}

func resolve(ip string, lookup func(ctx context.Context, ip string) ([]string, error)) {
	defer func() { <-sem }()
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	names, err := lookup(ctx, ip)
	host, ok := "", false
	if err == nil && len(names) > 0 && names[0] != "" {
		host, ok = strings.TrimSuffix(names[0], "."), true
	}
	ttl := negativeTTL
	if ok {
		ttl = positiveTTL
	}
	cacheMu.Lock()
	cache[ip] = cacheEntry{host: host, ok: ok, expires: time.Now().Add(ttl)}
	delete(inFlight, ip)
	evictLocked()
	cacheMu.Unlock()
}

// evictLocked keeps the cache under maxCacheEntries. Called with cacheMu already held, and cheap to call on
// every resolve() since it does nothing at all until the cache actually reaches the cap - this is a rare
// cold path, not a per-request cost: a DNS lookup just completed a few lines above, so one occasional O(n
// log n) sort afterward is immaterial next to it.
//
// Already-expired entries are removed first: they're the cheapest, least controversial win, since a stale
// entry past its TTL is going to be re-resolved from scratch next time it's asked for anyway. If that alone
// isn't enough, the remaining entries are sorted oldest-expiring-first and trimmed down to evictTargetFrac
// of the cap - "oldest expiry" is a reasonable proxy for "resolved longest ago and least likely to still be
// relevant", since nothing here tracks true last-access time.
func evictLocked() {
	if len(cache) <= maxCacheEntries {
		return
	}
	now := time.Now()
	for ip, e := range cache {
		if now.After(e.expires) {
			delete(cache, ip)
		}
	}
	target := int(float64(maxCacheEntries) * evictTargetFrac)
	if len(cache) <= target {
		return
	}
	ips := make([]string, 0, len(cache))
	for ip := range cache {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool { return cache[ips[i]].expires.Before(cache[ips[j]].expires) })
	for _, ip := range ips[:len(ips)-target] {
		delete(cache, ip)
	}
}

// SetLookupAddrForTest swaps the function ResolveCached uses to perform a reverse-DNS query, so tests (in
// this package or another) can control DNS results without ever touching the real network. It returns a
// function that restores the previous behavior and clears all cached/in-flight state; callers should always
// `defer` it. Test-only - never call this from non-test code.
func SetLookupAddrForTest(fn func(ctx context.Context, ip string) ([]string, error)) (restore func()) {
	lookupAddrMu.Lock()
	prev := lookupAddr
	lookupAddr = fn
	lookupAddrMu.Unlock()
	resetForTest()
	return func() {
		lookupAddrMu.Lock()
		lookupAddr = prev
		lookupAddrMu.Unlock()
		resetForTest()
	}
}

// resetForTest clears all resolver state (cache, in-flight tracking, semaphore) between test cases so they
// don't leak into one another. Test-only; unexported.
func resetForTest() {
	cacheMu.Lock()
	cache = map[string]cacheEntry{}
	inFlight = map[string]bool{}
	cacheMu.Unlock()
	for len(sem) > 0 {
		<-sem
	}
}
