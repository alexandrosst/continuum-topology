package fusionapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func vec(rows ...map[string]any) map[string]any {
	res := []any{}
	for _, r := range rows {
		res = append(res, r)
	}
	return map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}}
}

func row(v string, kv ...string) map[string]any {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return map[string]any{"metric": m, "value": []any{1.0, v}}
}

func TestLinkReadingsReadEachFigureWithItsOwnQuery(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		var out map[string]any
		switch {
		case strings.Contains(q, "probe_failures_total"):
			out = vec(row("3", "cluster", "a", "peer", "b", "direction", "reverse", "reason", "other"))
		case strings.Contains(q, "tcp_connect_failures_total"):
			out = vec(row("2", "cluster", "a", "peer", "b", "reason", "refused"))
		case strings.HasPrefix(q, "max by (cluster, peer, direction) (ikhnos_link_probe_up)"):
			out = vec(row("1", "cluster", "a", "peer", "b", "direction", "forward"), row("0", "cluster", "a", "peer", "b", "direction", "reverse"))
		case strings.Contains(q, "probe_loss_ratio"):
			out = vec(row("0.02", "cluster", "a", "peer", "b", "direction", "forward"), row("0", "cluster", "a", "peer", "b", "direction", "reverse"))
		case strings.Contains(q, "delay_variation"):
			out = vec(row("0.0004", "cluster", "a", "peer", "b", "direction", "forward"))
		case strings.Contains(q, "tcp_connect_seconds_sum"):
			// no new samples: the rate is NaN, and a NaN is not a reading
			out = vec(row("0.012", "cluster", "a", "peer", "b"), row("NaN", "cluster", "b", "peer", "a"))
		case strings.Contains(q, "path_mtu"):
			out = vec(row("1420", "cluster", "a", "peer", "b"))
		case strings.Contains(q, "throughput_bits"):
			out = vec(row("9.2e+08", "cluster", "a", "peer", "b", "direction", "forward"))
		case strings.Contains(q, "probe_last_success"):
			out = vec(row("1791200000", "cluster", "a", "peer", "b", "direction", "forward"))
		default:
			out = vec(row("1791100000", "cluster", "a", "peer", "b"), row("5", "cluster", "", "peer", "b")) // the second names no cluster
		}
		json.NewEncoder(w).Encode(out)
	}
	got, err := f.client().LinkReadings(context.Background(), AllSignals())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Loss) != 2 || got.Loss[0].Value != 0.02 || got.Loss[0].Direction != "forward" || got.Loss[0].Cluster != "a" || got.Loss[0].Peer != "b" {
		t.Errorf("loss = %+v", got.Loss)
	}
	if len(got.RTT) != 1 || got.RTT[0].Value != 0.012 {
		t.Errorf("rtt = %+v", got.RTT)
	}
	if len(got.Jitter) != 1 || len(got.MTU) != 1 || got.MTU[0].Value != 1420 || len(got.Throughput) != 1 || len(got.Probed) != 1 {
		t.Errorf("%+v", got)
	}
	if len(got.Tested) != 1 || got.Tested[0].Value != 1791100000 {
		t.Errorf("tested = %+v", got.Tested)
	}
	if len(got.Up) != 2 || got.Up[1].Direction != "reverse" || got.Up[1].Value != 0 {
		t.Errorf("up = %+v", got.Up)
	}
	if len(got.ProbeFailure) != 1 || got.ProbeFailure[0].Reason != "other" || got.ProbeFailure[0].Direction != "reverse" || got.ProbeFailure[0].Value != 3 {
		t.Errorf("probe failures = %+v", got.ProbeFailure)
	}
	if len(got.ConnectFailure) != 1 || got.ConnectFailure[0].Reason != "refused" || got.ConnectFailure[0].Direction != "" {
		t.Errorf("connect failures = %+v", got.ConnectFailure)
	}
	// every figure is asked for by a fixed query: nothing the caller supplies reaches PromQL
	seen := 0
	f.mu.Lock()
	for _, r := range f.reqs {
		if strings.Contains(r.URL.Query().Get("query"), "ikhnos_link_") {
			seen++
		}
	}
	f.mu.Unlock()
	if seen != 10 {
		t.Errorf("%d queries", seen)
	}
}

// A direction whose latest run failed withdraws its per-run gauges from the scrape, but the store still returns their last value
// to a look-back such as last_over_time for the whole window. The figures are therefore read only where the direction is up
// (verified in promtool: with the loss series gone stale and probe_up 0 the gated query returns nothing, the bare one the old
// value), and the series that exist twice for a while are aggregated, so the answer does not depend on their order.
func TestLinkReadingsDropTheFiguresOfADirectionThatIsDownAndAggregateDuplicates(t *testing.T) {
	f := newFake(t)
	var mu sync.Mutex
	queries := map[string]string{}
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		mu.Lock()
		for _, k := range []string{"probe_loss_ratio", "delay_variation", "path_mtu", "probe_last_success", "probe_failures_total", "tcp_connect_failures_total", "throughput_bits"} {
			if strings.Contains(q, k) {
				queries[k] = q
			}
		}
		mu.Unlock()
		json.NewEncoder(w).Encode(vec())
	}
	if _, err := f.client().LinkReadings(context.Background(), AllSignals()); err != nil {
		t.Fatal(err)
	}
	gate := "and on (cluster, peer, direction) (ikhnos_link_probe_up == 1)"
	for _, k := range []string{"probe_loss_ratio", "delay_variation"} {
		if q := queries[k]; !strings.HasPrefix(q, "max by (cluster, peer, direction) (") || !strings.Contains(q, gate) {
			t.Errorf("%s is read ungated or unaggregated: %s", k, q)
		}
	}
	if q := queries["path_mtu"]; !strings.HasPrefix(q, "min by (cluster, peer) (") {
		t.Errorf("the MTU is not the smaller of the series: %s", q)
	}
	// the reasons: only those that happened, the commonest per direction, over a window of many runs
	if q := queries["probe_failures_total"]; !strings.HasPrefix(q, "topk by (cluster, peer, direction) (1, ") || !strings.Contains(q, "> 0") || !strings.Contains(q, "[15m]") {
		t.Errorf("probe failures: %s", q)
	}
	if q := queries["tcp_connect_failures_total"]; !strings.HasPrefix(q, "topk by (cluster, peer) (1, ") || !strings.Contains(q, "> 0") {
		t.Errorf("connect failures: %s", q)
	}
	// a probe that last finished days ago is still told apart from one that never did
	if q := queries["probe_last_success"]; !strings.Contains(q, "[1d]") {
		t.Errorf("probed lookback: %s", q)
	}
}

func TestLinkReadingsNeedAnUnrestrictedScopeAndReportAStoreError(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(vec()) }
	var e *Error
	if _, err := f.client().LinkReadings(context.Background(), Scope{Signals: []string{SignalMetrics}, Namespaces: []string{"shop"}}); !errors.As(err, &e) || e.Status != http.StatusForbidden {
		t.Fatalf("a limited scope read the link figures: %v", err)
	}
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "path_mtu") {
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(vec())
	}
	if _, err := f.client().LinkReadings(context.Background(), AllSignals()); err == nil {
		t.Fatal("one failed query was swallowed")
	}
}

func TestLinkAccessFollowsTheScope(t *testing.T) {
	all := AllSignals()
	if err := all.NeedLinks(); err != nil {
		t.Errorf("unlimited: %v", err)
	}
	if err := (Scope{Signals: []string{SignalLogs, SignalTraces}}).NeedLinks(); statusOf(err) != http.StatusForbidden {
		t.Errorf("without metrics: %v", err)
	}
	if err := (Scope{}).NeedLinks(); statusOf(err) != http.StatusForbidden {
		t.Errorf("the zero scope: %v", err)
	}
	// the figures belong to no namespace, which a namespace limit hides; a cluster limit alone is fine
	if err := (Scope{Signals: Signals, Namespaces: []string{"shop"}}).NeedLinks(); statusOf(err) != http.StatusForbidden || !strings.Contains(err.Error(), "namespace") {
		t.Errorf("namespaces: %v", err)
	}
	if err := (Scope{Signals: Signals, Clusters: []string{"a"}}).NeedLinks(); err != nil {
		t.Errorf("clusters: %v", err)
	}
	s := Scope{Signals: Signals, Clusters: []string{"a", "b"}}
	if !s.LinkPairVisible("a", "b") || s.LinkPairVisible("a", "c") || s.LinkPairVisible("c", "b") || s.LinkPairVisible("", "a") {
		t.Error("a pair is visible only when both clusters are")
	}
	if !all.LinkPairVisible("x", "y") {
		t.Error("no cluster limit sees every pair")
	}
}
