package netid

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stubTXT(fn func(ctx context.Context, name string) ([]string, error)) func() {
	return SetLookupTXTForTest(fn)
}

func TestReverseV4(t *testing.T) {
	cases := []struct {
		ip     string
		want   string
		wantOK bool
	}{
		{"216.239.34.178", "178.34.239.216", true},
		{"1.2.3.4", "4.3.2.1", true},
		{"::1", "", false},
		{"2001:4860:4860::8888", "", false},
		{"not-an-ip", "", false},
		{"999.1.1.1", "", false}, // out of range octet
	}
	for _, c := range cases {
		got, ok := reverseV4(c.ip)
		if got != c.want || ok != c.wantOK {
			t.Errorf("reverseV4(%q) = %q, %v; want %q, %v", c.ip, got, ok, c.want, c.wantOK)
		}
	}
}

func TestSplitTXTFields(t *testing.T) {
	got := splitTXTFields("15169 | 216.239.32.0/19 | US | arin | 2000-11-19")
	want := []string{"15169", "216.239.32.0/19", "US", "arin", "2000-11-19"}
	if len(got) != len(want) {
		t.Fatalf("splitTXTFields = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitTXTFields()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveASNCachedTwoStageLookup(t *testing.T) {
	defer stubTXT(func(ctx context.Context, name string) ([]string, error) {
		switch {
		case strings.HasSuffix(name, ".origin.asn.cymru.com"):
			if name != "178.34.239.216.origin.asn.cymru.com" {
				t.Errorf("origin query name = %q, want the octet-reversed address as the prefix", name)
			}
			return []string{"15169 | 216.239.32.0/19 | US | arin | 2000-11-19"}, nil
		case strings.HasSuffix(name, ".asn.cymru.com"):
			if name != "AS15169.asn.cymru.com" {
				t.Errorf("org query name = %q, want AS15169.asn.cymru.com (from stage 1's asn number)", name)
			}
			return []string{"15169 | US | arin | 2000-11-19 | GOOGLE, US"}, nil
		}
		return nil, errors.New("unexpected query: " + name)
	})()

	org, asn, ok := ResolveASNCached("216.239.34.178")
	if ok {
		t.Fatalf("first call before the background lookup can possibly finish should not have an answer yet, got %q %q", org, asn)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if org, asn, ok = ResolveASNCached("216.239.34.178"); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ok || org != "GOOGLE, US" || asn != "AS15169" {
		t.Fatalf("ResolveASNCached = %q, %q, %v; want %q, %q, true", org, asn, ok, "GOOGLE, US", "AS15169")
	}
}

func TestResolveASNCachedMultiHomedTakesFirstASN(t *testing.T) {
	defer stubTXT(func(ctx context.Context, name string) ([]string, error) {
		if strings.HasSuffix(name, ".origin.asn.cymru.com") {
			return []string{"1234, 5678 | 10.0.0.0/24 | US | arin | 2000-01-01"}, nil
		}
		if name != "AS1234.asn.cymru.com" {
			t.Errorf("org query name = %q, want the FIRST comma-separated ASN (AS1234), not the second", name)
		}
		return []string{"1234 | US | arin | 2000-01-01 | FIRST-OPERATOR"}, nil
	})()

	deadline := time.Now().Add(2 * time.Second)
	var org string
	var ok bool
	for time.Now().Before(deadline) {
		if org, _, ok = ResolveASNCached("10.0.0.1"); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ok || org != "FIRST-OPERATOR" {
		t.Fatalf("ResolveASNCached = %q, %v; want the first ASN's operator name", org, ok)
	}
}

func TestResolveASNCachedNeverBlocksOnASlowLookup(t *testing.T) {
	release := make(chan struct{})
	defer stubTXT(func(ctx context.Context, name string) ([]string, error) {
		<-release // only unblocks when this test says so
		return []string{"1 | 0.0.0.0/0 | US | arin | 2000-01-01"}, nil
	})()
	defer close(release)

	done := make(chan struct{})
	go func() {
		ResolveASNCached("203.0.113.9")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ResolveASNCached blocked on a lookup that hadn't returned yet - it must return immediately on a cache miss")
	}
}

func TestResolveASNCachedCachesAFailedLookupNegatively(t *testing.T) {
	// Mirrors TestResolveCachedCachesAFailedLookupNegatively in resolve_test.go exactly, including why it's
	// shaped this way: a negative result also reports ok=false, same as "not resolved yet" - so completion
	// can't be polled for via the return value here (unlike the positive-result tests above, which poll
	// until ok flips true). The stub resolves instantly; give its one background goroutine a moment to run,
	// once, rather than hammering ResolveASNCached in a tight loop for the full timeout - which would just
	// be ~400 extra calls with nothing new to detect, since a negative answer never makes ok true.
	var calls int32
	defer stubTXT(func(ctx context.Context, name string) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("no TXT record")
	})()

	if _, _, ok := ResolveASNCached("198.51.100.7"); ok {
		t.Fatal("a fresh miss must not report ok=true before the background lookup has even run")
	}
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 5; i++ {
		if _, _, ok := ResolveASNCached("198.51.100.7"); ok {
			t.Fatalf("call %d: a negatively-cached IP must keep reporting ok=false", i)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("lookupTXT called %d times, want exactly 1 - a failed lookup should be cached, not retried on every call", n)
	}
}

func TestResolveASNCachedSkipsIPv6(t *testing.T) {
	called := false
	defer stubTXT(func(ctx context.Context, name string) ([]string, error) {
		called = true
		return []string{"1 | ::/0 | US | arin | 2000-01-01"}, nil
	})()

	// A v6 address can never resolve via this package yet (see reverseV4) - ResolveASNCached should report
	// ok=false immediately and never even start a background lookup for one.
	_, _, ok := ResolveASNCached("2001:4860:4860::8888")
	time.Sleep(20 * time.Millisecond)
	if ok {
		t.Fatal("ResolveASNCached should never succeed for an IPv6 address yet")
	}
	if called {
		t.Fatal("ResolveASNCached should not even attempt a DNS query for an IPv6 address")
	}
}
