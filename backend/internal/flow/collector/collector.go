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

// Report builds the wire message for one window.
func Report(src Source, node string, window time.Duration, flows []*continuumv1.RawFlow, lost uint64) *continuumv1.FlowReport {
	if len(flows) > flow.MaxRawFlows {
		// Keep the busiest; the rest are counted as lost so the report says so.
		lost += uint64(len(flows) - flow.MaxRawFlows)
		sortByWeight(flows)
		flows = flows[:flow.MaxRawFlows]
	}
	return &continuumv1.FlowReport{
		Method:        src.Method(),
		Node:          node,
		WindowSeconds: int32(window / time.Second),
		BytesKnown:    src.BytesKnown(),
		Lost:          lost,
		Flows:         flows,
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
		// A window with nothing in it is still reported: it is how the agent, and then the dashboard,
		// know this node is being observed and simply quiet.
		rep := Report(src, node, time.Since(last), pending, pendingLost)
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
