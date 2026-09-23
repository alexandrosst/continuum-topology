package server

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"continuum/internal/geoip"
)

// PublicIPFinder discovers this server's own public IP address by calling an operator-chosen echo
// service (a well known one: https://api.ipify.org, expected to answer with the caller's address as
// plain text - the common convention for these), and keeps the last answer cached in memory.
//
// It exists for exactly one purpose: Geo.Locate's fallback. When an agent's connecting address is not
// Locatable at all (private, loopback, CGNAT - not merely missing from the database), the only reason it
// looks that way to us is that the agent shares our own network, directly or through NAT - so our own
// public egress address is the closest honest guess at where that network actually is. A nil finder (no
// URL configured - the default) means the feature is off and Geo.Locate behaves exactly as it always did.
type PublicIPFinder struct {
	url    string
	client *http.Client // built once from a zero-value DeciderPolicy, so only public addresses are ever dialed
	ttl    time.Duration

	mu     sync.RWMutex
	cached netip.Addr
	have   bool
}

// NewPublicIPFinder builds a finder for url. An empty url disables the feature (returns nil). ttl <= 0
// defaults to an hour - the address is not expected to change often, and every refresh is one more
// outbound request an operator may not want to make more often than necessary.
func NewPublicIPFinder(url string, ttl time.Duration) *PublicIPFinder {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	// A zero-value DeciderPolicy has no allow-list of its own, so its client refuses to dial anything
	// private, loopback or cloud-metadata - reused here only for that SSRF-safe transport, not because
	// this has anything to do with the external decider feature.
	return &PublicIPFinder{url: url, client: (&DeciderPolicy{}).client(5 * time.Second), ttl: ttl}
}

// Start launches the background refresh loop and returns immediately: Current never blocks on the
// network, which matters because Geo.Locate is called from a synchronous request path (building the
// state document) on every poll. It stops when ctx is done.
func (f *PublicIPFinder) Start(ctx context.Context) {
	if f == nil {
		return
	}
	go f.run(ctx)
}

func (f *PublicIPFinder) run(ctx context.Context) {
	f.refresh(ctx)
	t := time.NewTicker(f.ttl)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.refresh(ctx)
		}
	}
}

// refresh makes one attempt and, only on success, replaces the cached address. A transient failure
// (the service is down, times out, or the network is briefly unreachable) leaves the last known-good
// answer in place rather than blanking out every location that depends on it until the next tick.
func (f *PublicIPFinder) refresh(ctx context.Context) {
	a, ok := f.fetch(ctx)
	if !ok {
		return
	}
	f.mu.Lock()
	f.cached, f.have = a, true
	f.mu.Unlock()
}

// Current returns the last address learned, without making a network call. ok is false before the
// first successful lookup completes (briefly, at startup) or when the feature is off (a nil finder).
func (f *PublicIPFinder) Current() (netip.Addr, bool) {
	if f == nil {
		return netip.Addr{}, false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.cached, f.have
}

// maxPublicIPResponse bounds what is read from the echo service's response: a bare address is at most
// 45 bytes (IPv6), so this is generous headroom, not an invitation to trust a large or hostile body.
const maxPublicIPResponse = 256

func (f *PublicIPFinder) fetch(ctx context.Context) (netip.Addr, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return netip.Addr{}, false
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return netip.Addr{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPublicIPResponse+1))
	if err != nil || len(body) > maxPublicIPResponse {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, false
	}
	a = a.Unmap().WithZone("")
	// The whole point of this call is a public address; an echo service that somehow answers with a
	// private one is not usable evidence of anything and must not be cached as if it were.
	if !geoip.Locatable(a) {
		return netip.Addr{}, false
	}
	return a, true
}
