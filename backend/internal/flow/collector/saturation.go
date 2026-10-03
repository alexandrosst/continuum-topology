package collector

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// sysClassNet is where every physical and virtual network interface's sysfs directory lives - a plain
// package variable (not a constant) purely so tests can point it at a fixture directory instead of the
// real /sys/class/net, the same "swap the root for a test" shape probe/read.go already uses for its own
// sysfs reads.
var sysClassNet = "/sys/class/net"

// rollupSaturation sums each flow's bytes (both directions: a flow's bytes_out/bytes_in are from the
// caller's own point of view, but a link carries both regardless of who is the client) onto the
// physical interface the kernel actually routed it over (RawFlow.iface - put there by flow.c's
// put_iface, from the socket's own route, never guessed from an address), divides by the window's real
// elapsed duration for a throughput figure, and pairs each unique interface with its own rated speed
// (read fresh from sysfs, once per interface, not cached across windows: a link can renegotiate, and
// this is cheap enough - at most a handful of interfaces - that caching would save nothing worth the
// staleness). Flows with no resolved interface (RawFlow.iface empty - the kernel had not routed the
// socket yet, or it raced into TCP_CLOSE first) contribute nothing: there is no interface to attribute
// them to. Returns nil when there was nothing to report (no window, or no flow carried an interface).
func rollupSaturation(flows []*continuumv1.RawFlow, window time.Duration) []*continuumv1.LinkSaturation {
	if window <= 0 {
		return nil
	}
	totals := map[string]uint64{}
	for _, f := range flows {
		if f == nil || f.Iface == "" {
			continue
		}
		totals[f.Iface] += f.BytesOut + f.BytesIn
	}
	if len(totals) == 0 {
		return nil
	}
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order: tests, and diffing one report against the next, both benefit

	out := make([]*continuumv1.LinkSaturation, 0, len(names))
	for _, name := range names {
		bps := uint64(float64(totals[name]) * 8 / window.Seconds())
		ls := &continuumv1.LinkSaturation{Iface: name, ThroughputBps: bps}
		if mbps, ok := readIfaceSpeedMbps(name); ok {
			capacityBps := mbps * 1_000_000
			pct := float64(bps) / capacityBps * 100
			// Clamped above 100 only (see LinkSaturation.saturation_pct's own doc comment on why);
			// never below 0, which bps being a uint64 already rules out.
			if pct > 100 {
				pct = 100
			}
			ls.SaturationPct = &pct
		}
		out = append(out, ls)
	}
	return out
}

// readIfaceSpeedMbps reads one interface's own rated link speed from sysfs - the same file, and the
// same "absent, unreadable, or -1 (no link) all mean not known" rule, that probe/read.go's own
// interfaces() already uses for the node probe's unrelated NetworkInterface.speed_mbps (see
// LinkSaturation.saturation_pct's doc comment for why this is still a separate read rather than sharing
// that one: the two run in different processes, on different schedules, and a flow collector must not
// depend on a node probe that may not even be deployed). A virtual interface (veth, bridge, overlay
// tunnel) and a physical one with the driver not reporting speed both simply have no such file, or have
// -1 in it, and both are treated identically here: the saturation percentage is omitted for that
// interface, never defaulted to a guessed capacity.
func readIfaceSpeedMbps(iface string) (float64, bool) {
	b, err := os.ReadFile(filepath.Join(sysClassNet, iface, "speed"))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 0 {
		return 0, false
	}
	return float64(n), true
}
