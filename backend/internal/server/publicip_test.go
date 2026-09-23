package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// permissiveFinder builds a *PublicIPFinder that trusts loopback (an httptest.Server listens on
// 127.0.0.1, which the real default policy - correctly - refuses to dial at all: see
// TestPublicIPFinderDefaultPolicyRefusesLoopback). Constructing the struct directly, rather than through
// NewPublicIPFinder, is what lets a test open exactly that one hole without weakening the real default.
func permissiveFinder(url string, ttl time.Duration) *PublicIPFinder {
	allow, _ := NewDeciderPolicy("127.0.0.0/8, ::1/128")
	return &PublicIPFinder{url: url, client: allow.client(5 * time.Second), ttl: ttl}
}

func TestPublicIPFinderNilWhenURLEmpty(t *testing.T) {
	if f := NewPublicIPFinder("", time.Hour); f != nil {
		t.Fatalf("empty URL should disable the feature, got %#v", f)
	}
	// And a nil *PublicIPFinder is safe to use exactly like a nil *Geo is: Start and Current are no-ops.
	var f *PublicIPFinder
	f.Start(context.Background())
	if _, ok := f.Current(); ok {
		t.Fatal("nil finder should never report ok")
	}
}

func TestPublicIPFinderFetchParsesPlainTextAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.9\n")) // echo services commonly trail a newline
	}))
	defer srv.Close()
	f := permissiveFinder(srv.URL, time.Hour)
	a, ok := f.fetch(context.Background())
	if !ok || a.String() != "203.0.113.9" {
		t.Fatalf("fetch() = %v, %v", a, ok)
	}
}

func TestPublicIPFinderFetchRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("203.0.113.9"))
	}))
	defer srv.Close()
	f := permissiveFinder(srv.URL, time.Hour)
	if _, ok := f.fetch(context.Background()); ok {
		t.Fatal("a non-200 response should not be trusted")
	}
}

func TestPublicIPFinderFetchRejectsGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not an ip address at all</html>"))
	}))
	defer srv.Close()
	f := permissiveFinder(srv.URL, time.Hour)
	if _, ok := f.fetch(context.Background()); ok {
		t.Fatal("a body that isn't an address should not be trusted")
	}
}

func TestPublicIPFinderFetchRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("9", maxPublicIPResponse+100)))
	}))
	defer srv.Close()
	f := permissiveFinder(srv.URL, time.Hour)
	if _, ok := f.fetch(context.Background()); ok {
		t.Fatal("an oversized response should not be read in full or trusted")
	}
}

func TestPublicIPFinderFetchRejectsAPrivateAnswer(t *testing.T) {
	// An echo service that claims the caller's address is a private one is not usable evidence of
	// anything - accepting it would let a compromised or misconfigured service hand back an address
	// that then gets treated as this server's own public location.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("10.1.2.3"))
	}))
	defer srv.Close()
	f := permissiveFinder(srv.URL, time.Hour)
	if _, ok := f.fetch(context.Background()); ok {
		t.Fatal("a private address in the response body should be refused")
	}
}

func TestPublicIPFinderDefaultPolicyRefusesLoopback(t *testing.T) {
	// The security property that matters: without a test opening loopback, the real constructor's
	// client must refuse to dial an httptest.Server at all (it listens on 127.0.0.1), the same way the
	// external decider refuses loopback by default. This is what stops --geoip-public-ip-service from
	// becoming an SSRF vector against the server's own network if it's ever misconfigured.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.9"))
	}))
	defer srv.Close()
	f := NewPublicIPFinder(srv.URL, time.Hour)
	if _, ok := f.fetch(context.Background()); ok {
		t.Fatal("the default policy should refuse to dial a loopback address")
	}
}

func TestPublicIPFinderRefreshKeepsLastGoodValueOnFailure(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("203.0.113.9"))
	}))
	defer srv.Close()
	f := permissiveFinder(srv.URL, time.Hour)

	f.refresh(context.Background())
	a, ok := f.Current()
	if !ok || a.String() != "203.0.113.9" {
		t.Fatalf("first refresh: %v, %v", a, ok)
	}

	fail.Store(true)
	f.refresh(context.Background())
	a, ok = f.Current()
	if !ok || a.String() != "203.0.113.9" {
		t.Fatalf("a failed refresh should keep the last known-good address, got %v, %v", a, ok)
	}
}

func TestPublicIPFinderStartRefreshesInTheBackground(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("203.0.113.9"))
	}))
	defer srv.Close()
	f := permissiveFinder(srv.URL, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n < 3 {
		t.Fatalf("expected at least 3 background refreshes within the deadline, got %d", n)
	}
	if a, ok := f.Current(); !ok || a.String() != "203.0.113.9" {
		t.Fatalf("Current() = %v, %v", a, ok)
	}
}
