package netid

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveCachedReturnsAnswerOnceBackgroundLookupCompletes(t *testing.T) {
	defer SetLookupAddrForTest(func(ctx context.Context, ip string) ([]string, error) {
		return []string{"lax17s79-in-f14.1e100.net."}, nil
	})()

	host, ok := ResolveCached("192.178.194.101")
	if ok {
		t.Fatalf("first call before the background lookup can possibly finish should not have an answer yet, got %q", host)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if host, ok = ResolveCached("192.178.194.101"); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ok || host != "lax17s79-in-f14.1e100.net" {
		t.Fatalf("ResolveCached = %q, %v, want the resolved (trailing-dot-trimmed) hostname once the background lookup lands", host, ok)
	}
}

func TestResolveCachedNeverBlocksOnASlowLookup(t *testing.T) {
	release := make(chan struct{})
	defer SetLookupAddrForTest(func(ctx context.Context, ip string) ([]string, error) {
		<-release // only unblocks when this test says so
		return []string{"slow.example.com."}, nil
	})()
	defer close(release)

	done := make(chan struct{})
	go func() {
		ResolveCached("203.0.113.9")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ResolveCached blocked on a lookup that hadn't returned yet - it must return immediately on a cache miss")
	}
}

func TestResolveCachedCachesAFailedLookupNegatively(t *testing.T) {
	var calls int32
	defer SetLookupAddrForTest(func(ctx context.Context, ip string) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("no PTR record")
	})()

	if _, ok := ResolveCached("198.51.100.7"); ok {
		t.Fatal("a fresh miss must not report ok=true before the background lookup has even run")
	}
	// A negative result also reports ok=false, same as "not resolved yet" - so completion can't be detected
	// by polling the return value here. The stub resolves instantly; give its goroutine a moment to run.
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 5; i++ {
		if _, ok := ResolveCached("198.51.100.7"); ok {
			t.Fatalf("call %d: a negatively-cached IP must keep reporting ok=false", i)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("lookupAddr called %d times, want exactly 1 - a failed lookup should be cached, not retried on every call", got)
	}
}

func TestResolveCachedDedupesConcurrentLookupsForTheSameIP(t *testing.T) {
	var calls int32
	defer SetLookupAddrForTest(func(ctx context.Context, ip string) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(30 * time.Millisecond)
		return []string{"shared.example.com."}, nil
	})()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ResolveCached("192.0.2.55")
		}()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("lookupAddr called %d times for 20 concurrent callers of the same IP, want exactly 1 (deduped)", got)
	}
}

func TestMatchHostSuffixMatching(t *testing.T) {
	cases := []struct {
		host   string
		want   string
		ok     bool
		shared bool // whether this match is expected to be a third-party CDN (Shared) or a single owner's own infra
	}{
		{"lax17s79-in-f14.1e100.net", "Google", true, false},       // Google's own infra: collapses to one node
		{"LAX17S79-IN-F14.1E100.NET.", "Google", true, false},      // case-insensitive, trailing dot trimmed
		{"1e100.net", "Google", true, false},                       // the bare zone apex itself also matches
		{"notreally1e100.net", "", false, false},                   // must match on a label boundary, not a raw substring
		{"ec2-1-2-3-4.compute-1.amazonaws.com", "AWS", true, true}, // a genuine third-party CDN: stays per-address
		{"d111111abcdef8.cloudfront.net", "Amazon CloudFront", true, true},
		{"raw-cdn-13.githubusercontent.com", "GitHub", true, true},
		{"api.github.com", "", false, false}, // covered by Lookup's CIDR table instead, not this suffix table
		{"example.com", "", false, false},
	}
	for _, c := range cases {
		m, ok := MatchHost(c.host)
		if ok != c.ok || (ok && m.Name != c.want) {
			t.Errorf("MatchHost(%q) = %+v, %v; want Name=%q, ok=%v", c.host, m, ok, c.want, c.ok)
		}
		if ok && m.Shared != c.shared {
			t.Errorf("MatchHost(%q) matched %q with Shared=%v, want %v - see hostSuffixes' own doc comment for why Google's own infra is the deliberate exception", c.host, m.Name, m.Shared, c.shared)
		}
	}
}

// Pins the fix for the cache's unbounded growth: nothing ever removed an entry just because the IP it
// belonged to stopped appearing in any topology, so a long-running server resolving a long tail of distinct
// external addresses would grow this map forever. Primes the cache directly past maxCacheEntries (real DNS
// lookups to get there would make this test absurdly slow) so the very next resolve has real eviction work
// to do, then asserts that work actually happens.
func TestResolveCachedBoundsCacheSizeUnderSustainedNewAddresses(t *testing.T) {
	defer SetLookupAddrForTest(func(ctx context.Context, ip string) ([]string, error) {
		return []string{"host.example.com."}, nil
	})()

	cacheMu.Lock()
	now := time.Now()
	for i := 0; i < maxCacheEntries+50; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		cache[ip] = cacheEntry{host: "old", ok: true, expires: now.Add(time.Duration(i) * time.Millisecond)}
	}
	cacheMu.Unlock()

	// One more resolution should trigger evictLocked and bring the cache back under the cap.
	_, _ = ResolveCached("192.0.2.1")

	deadline := time.Now().Add(2 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		cacheMu.Lock()
		n = len(cache)
		cacheMu.Unlock()
		if n <= maxCacheEntries {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n > maxCacheEntries {
		t.Fatalf("cache has %d entries after a resolve pushed it over the cap, want <= %d", n, maxCacheEntries)
	}
}
