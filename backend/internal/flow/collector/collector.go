// Package collector is the node-side loop of traffic observation: pick the best method the node
// supports, read it every window, and report the counts to the agent.
package collector

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/flow"
	"continuum/internal/probe"

	"google.golang.org/protobuf/encoding/protojson"
)

// Source is one way of counting connections.
type Source interface {
	// Method is "ebpf" or "conntrack"; it travels with every report because it decides how far to trust the numbers.
	Method() string
	BytesKnown() bool
	// Collect returns what was counted since the previous call, and how many observations were dropped.
	Collect() ([]*continuumv1.RawFlow, uint64, error)
	Close() error
}

// SnatExhaustionSource is implemented by a Source that can also say how many connect() attempts have
// failed with EADDRNOTAVAIL (ephemeral port / SNAT exhaustion) - currently only the eBPF Observer;
// conntrack has no way to see a connect() attempt that failed before it ever created a conntrack entry.
// Checked with a type assertion rather than added to Source itself, so conntrack.Reader does not have
// to grow a method that would always just return 0 for it.
type SnatExhaustionSource interface {
	SnatExhaustion() uint64
}

// ThermalThrottleSource is implemented by a Source that can also say how many times the kernel's
// power:cpu_frequency/thermal:thermal_zone_trip tracepoints have fired - currently only the eBPF
// Observer; conntrack has no eBPF program loaded at all, so it has no tracepoint to attach either
// counter to. Checked the same way as SnatExhaustionSource above, and for the same reason.
type ThermalThrottleSource interface {
	ThermalThrottle() (cpuFreqChangeCount, thermalTripCount uint64)
}

// Opener tries to start one method.
type Opener func() (Source, error)

// Choose picks the method. mode is auto (eBPF, then conntrack), ebpf or conntrack. The reasons the
// better methods were not used are returned so the log can say why a node runs the fallback.
func Choose(mode string, ebpf, conntrack Opener) (Source, []string, error) {
	var why []string
	try := func(name string, open Opener) Source {
		s, err := open()
		if err != nil {
			why = append(why, fmt.Sprintf("%s: %v", name, err))
			return nil
		}
		return s
	}
	switch mode {
	case "ebpf":
		if s := try("ebpf", ebpf); s != nil {
			return s, why, nil
		}
	case "conntrack":
		if s := try("conntrack", conntrack); s != nil {
			return s, why, nil
		}
	case "", "auto":
		if s := try("ebpf", ebpf); s != nil {
			return s, why, nil
		}
		if s := try("conntrack", conntrack); s != nil {
			return s, why, nil
		}
	default:
		return nil, nil, fmt.Errorf("unknown method %q (auto, ebpf or conntrack)", mode)
	}
	return nil, why, fmt.Errorf("no way of observing traffic works on this node: %s", strings.Join(why, "; "))
}

// Report builds the wire message for one window. snatExhaustion is this node's current lifetime total
// (see SnatExhaustionSource), 0 when src does not implement it or has not attached either way of
// counting it.
func Report(src Source, node string, window time.Duration, flows []*continuumv1.RawFlow, lost uint64, snatExhaustion uint64, thermalThrottle *continuumv1.ThermalThrottle) *continuumv1.FlowReport {
	// Computed from every flow this window actually saw, before the busiest-first truncation below drops
	// the rest: a quiet, low-connection-count flow can still carry real bytes on an interface, and the
	// saturation rollup must not miss those just because the flow list itself got trimmed for size.
	linkSaturation := rollupSaturation(flows, window)
	if len(flows) > flow.MaxRawFlows {
		// Keep the busiest; the rest are counted as lost so the report says so.
		lost += uint64(len(flows) - flow.MaxRawFlows)
		sortByWeight(flows)
		flows = flows[:flow.MaxRawFlows]
	}
	return &continuumv1.FlowReport{
		Method:          src.Method(),
		Node:            node,
		WindowSeconds:   int32(window / time.Second),
		BytesKnown:      src.BytesKnown(),
		Lost:            lost,
		Flows:           flows,
		LinkSaturation:  linkSaturation,
		SnatExhaustion:  snatExhaustion,
		ThermalThrottle: thermalThrottle,
	}
}

// Run collects every interval and reports until ctx ends. A window that cannot be delivered is kept
// and merged into the next one, so a restarting agent does not lose what was counted meanwhile.
func Run(ctx context.Context, src Source, agentURL string, secret []byte, node string, every time.Duration, logf func(msg string, kv ...any)) {
	hc := &http.Client{Timeout: 15 * time.Second}
	var pending []*continuumv1.RawFlow
	var pendingLost uint64
	last := time.Now()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		flows, lost, err := src.Collect()
		if err != nil {
			logf("could not read the counters", "err", err)
			continue
		}
		pending = merge(pending, flows)
		pendingLost += lost
		// A lifetime total already (see SnatExhaustionSource's own doc comment), so this is read fresh
		// every window, never accumulated into a pending-like variable the way lost is above.
		var snatExhaustion uint64
		if s, ok := src.(SnatExhaustionSource); ok {
			snatExhaustion = s.SnatExhaustion()
		}
		// Like snatExhaustion above, a lifetime total read fresh every window, never accumulated. nil
		// (not a zeroed message) when src does not implement ThermalThrottleSource or neither
		// tracepoint ever attached - see ThermalThrottle's own doc comment for why that ambiguity is
		// accepted here the same way it already is for snatExhaustion.
		var thermalThrottle *continuumv1.ThermalThrottle
		if s, ok := src.(ThermalThrottleSource); ok {
			cpuFreqChangeCount, thermalTripCount := s.ThermalThrottle()
			if cpuFreqChangeCount > 0 || thermalTripCount > 0 {
				thermalThrottle = &continuumv1.ThermalThrottle{CpuFreqChangeCount: cpuFreqChangeCount, ThermalTripCount: thermalTripCount}
			}
		}
		// A window with nothing in it is still reported: it is how the agent, and then the dashboard,
		// know this node is being observed and simply quiet.
		rep := Report(src, node, time.Since(last), pending, pendingLost, snatExhaustion, thermalThrottle)
		body, err := protojson.Marshal(rep)
		if err != nil {
			logf("could not encode a report", "err", err)
			continue
		}
		if err := probe.PostSigned(ctx, hc, agentURL+flow.PathReport, secret, node, body, time.Now()); err != nil {
			logf("could not report to the agent, will retry with the next window", "err", err, "pending", len(pending))
			if len(pending) > 4*flow.MaxRawFlows {
				pendingLost += uint64(len(pending))
				pending = nil
			}
			continue
		}
		pending, pendingLost, last = nil, 0, time.Now()
	}
}

type rawKey struct {
	client            bool
	local, peer, prot string
	port              uint32
}

func merge(a, b []*continuumv1.RawFlow) []*continuumv1.RawFlow {
	if len(a) == 0 {
		return b
	}
	idx := make(map[rawKey]*continuumv1.RawFlow, len(a))
	for _, f := range a {
		idx[rawKey{f.Client, f.LocalIp, f.PeerIp, f.Protocol, f.Port}] = f
	}
	for _, f := range b {
		k := rawKey{f.Client, f.LocalIp, f.PeerIp, f.Protocol, f.Port}
		if cur, ok := idx[k]; ok {
			cur.Connections += f.Connections
			cur.BytesOut += f.BytesOut
			cur.BytesIn += f.BytesIn
			continue
		}
		idx[k] = f
		a = append(a, f)
	}
	return a
}

func sortByWeight(fl []*continuumv1.RawFlow) {
	w := func(f *continuumv1.RawFlow) uint64 { return f.Connections + (f.BytesOut+f.BytesIn)/1024 }
	sort.Slice(fl, func(i, j int) bool { return w(fl[i]) > w(fl[j]) })
}
