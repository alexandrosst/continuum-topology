package netid

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file adds a third, fully automatic identification tier on top of entries (hand-curated ranges) and
// MatchHost (reverse-DNS hostname suffixes): for an address neither of those covers, ask who originates it
// on the public internet via Team Cymru's IP-to-ASN DNS lookup service (a long-standing, free, no-API-key
// service - the same one most network tooling uses for this - reachable with two ordinary DNS TXT queries,
// so it costs nothing extra to grant beyond the outbound DNS access ResolveCached already uses for PTR
// lookups). Unlike entries, this needs no maintenance as new providers show up: an IP belonging to some
// provider we've never hand-curated still comes back with a real organization name instead of a bare
// address. What it CANNOT tell us is entries' Shared/not-Shared judgment (whether the matched range is one
// owner's own infrastructure or a CDN edge fronting many unrelated tenants) - callers should always treat
// an ASN-derived Match as Shared: true (the safe default: keep addresses as separate nodes rather than
// risk merging two unrelated services that just happen to sit behind the same network operator).

// lookupTXT does the actual DNS TXT query. A package-level var, mirroring lookupAddr in resolve.go, so
// tests can substitute a stub and never touch the real network.
var (
	lookupTXTMu sync.Mutex
	lookupTXT   = func(ctx context.Context, name string) ([]string, error) {
		return net.DefaultResolver.LookupTXT(ctx, name)
	}
)

const (
	asnResolveTimeout = 3 * time.Second  // bounds both queries together; see resolveTimeout in resolve.go
	asnPositiveTTL    = 24 * time.Hour   // ASN origin assignments rarely change; see positiveTTL
	asnNegativeTTL    = 10 * time.Minute // see negativeTTL
	asnMaxInFlight    = 8                // see maxInFlight
)

type asnCacheEntry struct {
	org     string
	asn     string
	ok      bool
	expires time.Time
}

var (
	asnCacheMu  sync.Mutex
	asnCache    = map[string]asnCacheEntry{}
	asnInFlight = map[string]bool{}
	asnSem      = make(chan struct{}, asnMaxInFlight)
)

// ResolveASNCached returns the network operator's name and ASN label (e.g. "AS15169") for ip if already
// known, never blocking on the network - same non-blocking, cached, deduplicated, bounded-concurrency shape
// as ResolveCached in resolve.go, for the same reason (this runs on every state poll from every connected
// browser session). A cache miss starts a background resolution and returns ok=false; the next poll picks
// up the answer once it lands. IPv6 isn't supported yet (Team Cymru's origin query needs nibble-reversed
// hex for v6, a different format than the dotted-quad reversal below) - those addresses always report ok=false.
func ResolveASNCached(ip string) (org string, asnLabel string, ok bool) {
	asnCacheMu.Lock()
	e, hit := asnCache[ip]
	stale := hit && time.Now().After(e.expires)
	alreadyRunning := asnInFlight[ip]
	needsStart := (!hit || stale) && !alreadyRunning
	if needsStart {
		asnInFlight[ip] = true
	}
	asnCacheMu.Unlock()

	if needsStart {
		select {
		case asnSem <- struct{}{}:
			go resolveASN(ip)
		default:
			// Already at asnMaxInFlight: drop this attempt rather than queue it unboundedly - retried
			// next time this IP shows up, same as ResolveCached's own default case.
			asnCacheMu.Lock()
			delete(asnInFlight, ip)
			asnCacheMu.Unlock()
		}
	}
	return e.org, e.asn, hit && e.ok
}

// reverseV4 turns a dotted-quad IPv4 address into Team Cymru's expected query order (octets reversed), or
// ("", false) for anything else (IPv6, malformed input) - the caller treats that as "can't resolve this one".
func reverseV4(ip string) (string, bool) {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return "", false
	}
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err != nil || n < 0 || n > 255 {
			return "", false
		}
	}
	return parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0], true
}

// splitTXTFields splits a Team Cymru TXT answer (pipe-separated, e.g.
// "15169 | 216.239.32.0/19 | US | arin | 2000-11-19") on "|" and trims each field.
func splitTXTFields(txt string) []string {
	raw := strings.Split(txt, "|")
	fields := make([]string, len(raw))
	for i, f := range raw {
		fields[i] = strings.TrimSpace(f)
	}
	return fields
}

func resolveASN(ip string) {
	defer func() { <-asnSem }()
	org, asnLabel, ok := "", "", false
	if rev, valid := reverseV4(ip); valid {
		ctx, cancel := context.WithTimeout(context.Background(), asnResolveTimeout)
		defer cancel()
		lookupTXTMu.Lock()
		lookup := lookupTXT
		lookupTXTMu.Unlock()

		// Stage 1: "<reversed-ip>.origin.asn.cymru.com" -> "<asn> | <bgp prefix> | <cc> | <registry> | <date>".
		// A multi-homed prefix can list more than one ASN, comma-separated ("1234, 5678 | ..."); the first
		// is as good a guess as this package makes anywhere else it picks "the" owner of a range.
		if answers, err := lookup(ctx, rev+".origin.asn.cymru.com"); err == nil && len(answers) > 0 {
			fields := splitTXTFields(answers[0])
			if len(fields) > 0 && fields[0] != "" {
				asnNum := strings.TrimSpace(strings.SplitN(fields[0], ",", 2)[0])
				if asnNum != "" {
					// Stage 2: "AS<num>.asn.cymru.com" -> "<asn> | <cc> | <registry> | <date> | <org name>".
					if answers2, err2 := lookup(ctx, "AS"+asnNum+".asn.cymru.com"); err2 == nil && len(answers2) > 0 {
						fields2 := splitTXTFields(answers2[0])
						if len(fields2) > 0 {
							if name := fields2[len(fields2)-1]; name != "" {
								org, asnLabel, ok = name, "AS"+asnNum, true
							}
						}
					}
				}
			}
		}
	}
	ttl := asnNegativeTTL
	if ok {
		ttl = asnPositiveTTL
	}
	asnCacheMu.Lock()
	asnCache[ip] = asnCacheEntry{org: org, asn: asnLabel, ok: ok, expires: time.Now().Add(ttl)}
	delete(asnInFlight, ip)
	asnCacheMu.Unlock()
}

// SetLookupTXTForTest swaps the function ResolveASNCached uses to perform a DNS TXT query, so tests can
// control results without touching the real network. Mirrors SetLookupAddrForTest in resolve.go: returns a
// restore function callers should always `defer`, and clears all cached/in-flight ASN state on both the
// swap and the restore so tests never leak state into one another.
func SetLookupTXTForTest(fn func(ctx context.Context, name string) ([]string, error)) (restore func()) {
	lookupTXTMu.Lock()
	prev := lookupTXT
	lookupTXT = fn
	lookupTXTMu.Unlock()
	resetASNForTest()
	return func() {
		lookupTXTMu.Lock()
		lookupTXT = prev
		lookupTXTMu.Unlock()
		resetASNForTest()
	}
}

// resetASNForTest clears all ASN resolver state between test cases. Test-only; unexported.
func resetASNForTest() {
	asnCacheMu.Lock()
	asnCache = map[string]asnCacheEntry{}
	asnInFlight = map[string]bool{}
	asnCacheMu.Unlock()
	for len(asnSem) > 0 {
		<-asnSem
	}
}
