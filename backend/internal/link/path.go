package link

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"
)

// PathResult is what a path probe learned about the way from this cluster to one address of a peer.
type PathResult struct {
	// MTU is the largest packet, in bytes, that fits the whole way, as the routers on the path said (0: not learned). It is
	// set when the destination answered a full-size probe, or when a router (or the kernel) asked for a smaller size. It is
	// never this host's own interface MTU: that says nothing about the path (see Blackhole).
	MTU int
	// Hops is how many routers the packets crossed to reach the address (set when Reached). Not exported as a metric (the
	// one-way probe's TTL-derived hop count is the one): it is kept for the log and the tests.
	Hops int
	// Reached says the destination answered. Many networks drop the answer (and with it the probe's last step), so a
	// path that is not reached is not necessarily broken.
	Reached bool
	// Blackhole means a hop answered small packets but swallowed full-size ones without saying so: the path's MTU is
	// smaller than this host believes and nothing told it, so large packets vanish. That looks like loss.
	Blackhole bool
	// Route is a fingerprint of the routers that answered, in order, of a trace that reached the destination (empty
	// otherwise: a trace cut short, or stopped at a black hole, is not a route to compare). It is not the addresses: nothing
	// about the path's routers leaves the cluster, only whether the route stayed the same.
	Route string
}

// ErrPathUnsupported says this address (or this platform) cannot be path-probed; callers skip it quietly.
var ErrPathUnsupported = errors.New("path probing is not available for this address")

// PathProbeFunc probes the path to dst.
type PathProbeFunc func(ctx context.Context, dst netip.Addr) (PathResult, error)

// routeFingerprint summarises the responding hops. Silent hops are left out: a router that answers one probe in three
// would otherwise look like a route change at every run.
func routeFingerprint(hops []netip.Addr) string {
	if len(hops) == 0 {
		return ""
	}
	h := sha256.New()
	var parts []string
	for _, a := range hops {
		if a.IsValid() {
			parts = append(parts, a.String())
		}
	}
	if len(parts) == 0 {
		return ""
	}
	h.Write([]byte(strings.Join(parts, ">")))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

type pathStats struct {
	last    *PathResult
	lastOK  time.Time
	stable  string // the route fingerprint accepted as the current one
	pending string // a different fingerprint seen once
	changes uint64
	failed  uint64
}

// ObservePath records a path probe towards peer. A route change is counted when a new fingerprint has been seen on two
// probes in a row, so one odd probe (a router that answered differently once) is not a change. Only the route of a trace that
// reached the destination counts: a truncated one (a black hole, a firewall) would be a different "route" at every hiccup.
func (m *Meter) ObservePath(peer string, r PathResult) {
	if m == nil || !m.knows(peer) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.path(peer)
	cp := r
	s.last, s.lastOK = &cp, m.now()
	if r.Route == "" || !r.Reached || r.Blackhole {
		return
	}
	switch {
	case s.stable == "":
		s.stable, s.pending = r.Route, ""
	case r.Route == s.stable:
		s.pending = ""
	case r.Route == s.pending:
		s.stable, s.pending = r.Route, ""
		s.changes++
	default:
		s.pending = r.Route
	}
}

// PathFailed counts a path probe that could not run.
func (m *Meter) PathFailed(peer string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path(peer).failed++
}

func (m *Meter) knows(peer string) bool {
	for _, p := range m.Peers.List() {
		if p.ID == peer {
			return true
		}
	}
	return false
}

func (m *Meter) path(peer string) *pathStats {
	s := m.paths[peer]
	if s == nil {
		if m.paths == nil {
			m.paths = map[string]*pathStats{}
		}
		s = &pathStats{}
		m.paths[peer] = s
	}
	return s
}
