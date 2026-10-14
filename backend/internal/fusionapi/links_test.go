package fusionapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
	// every figure is asked for by a fixed query: nothing the caller supplies reaches PromQL
	seen := 0
	f.mu.Lock()
	for _, r := range f.reqs {
		if strings.Contains(r.URL.Query().Get("query"), "continuum_link_") {
			seen++
		}
	}
	f.mu.Unlock()
	if seen != 7 {
		t.Errorf("%d queries", seen)
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
