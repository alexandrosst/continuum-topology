package fusionapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestANonFiniteDoubleAttributeDoesNotBreakTheTrace(t *testing.T) {
	for in, want := range map[string]any{`{"doubleValue":1.5}`: 1.5, `{"doubleValue":"NaN"}`: "NaN", `{"doubleValue":"-Infinity"}`: "-Infinity"} {
		var v otlpValue
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := v.any(); got != want {
			t.Errorf("%s = %#v", in, got)
		}
		if _, err := json.Marshal(v.any()); err != nil {
			t.Errorf("%s cannot be written back: %v", in, err)
		}
	}
}

func TestAFocusedUnrestrictedCallerStillSeesTheRootOfATrace(t *testing.T) {
	f := newFake(t)
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"traces": []any{map[string]any{"traceID": traceHex, "rootServiceName": "gateway", "rootTraceName": "GET /",
			"startTimeUnixNano": "1791200000000000000", "durationMs": 120}}})
	}
	fs, err := AllSignals().FocusOn(&testGroups()[0])
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.client().SearchTraces(context.Background(), fs, TraceFilter{}, rangeAll, 20)
	if err != nil || len(got) != 1 || got[0].RootService != "gateway" || got[0].DurationMs != 120 {
		t.Fatalf("a choice of application must not hide what an unrestricted caller may see: %+v %v", got, err)
	}
	if q := f.last("/api/search").Get("q"); !strings.Contains(q, `resource.service.name`) || !strings.Contains(q, `resource.k8s.namespace.name = "shop"`) {
		t.Fatalf("but the search is still narrowed to the application: %s", q)
	}
}

func TestTimesAndDurationsOutOfRangeAreRefusedPlainly(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, from := range []string{"1696000000000", "Inf", "1e30", "NaN"} {
		_, err := ParseRange(from, "", now)
		if statusOf(err) != http.StatusBadRequest {
			t.Errorf("from=%s: %v", from, err)
		}
	}
	if _, err := ParseRange("1696000000000", "", now); err == nil || !strings.Contains(err.Error(), "milliseconds") {
		t.Errorf("epoch milliseconds are named as such: %v", err)
	}
	for _, d := range []string{"1e30", "1e10", "NaN", "-1"} {
		if v, err := DurationParam(d); err == nil || v != 0 {
			t.Errorf("duration %s = %v, %v", d, v, err)
		}
	}
	if _, err := ChooseStep(TimeRange{From: now.Add(-time.Hour), To: now}, 500*time.Millisecond, 120); err == nil || !strings.Contains(err.Error(), "at least one second") {
		t.Errorf("a step under a second: %v", err)
	}
	if v, err := DurationParam("1.5"); err != nil || v != 1500*time.Millisecond {
		t.Errorf("%v %v", v, err)
	}
}

func TestFusedMetricsAndContextLogsStayInTheClustersOfTheResource(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
	}
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{}}})
	}
	r := &Resource{Service: "cart", Namespace: "shop", Pod: "cart-0", Cluster: "cl-1"}
	c := f.client()
	if _, _, err := resourceMetrics(context.Background(), r, FuseOptions{MaxSeries: 5, MetricViews: MetricViews{App: true, Pod: true}}, newMetricReads(c, AllSignals(), rangeAll, time.Minute, 5)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	var queries []string
	for _, req := range f.reqs {
		queries = append(queries, req.URL.Query().Get("query"))
	}
	f.mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("%v", queries)
	}
	for _, q := range queries {
		if !strings.Contains(q, `continuum_cluster_id="cl-1"`) {
			t.Errorf("a resource of cl-1 read the series of every cluster: %s", q)
		}
	}
	if _, _, err := c.Logs(context.Background(), AllSignals(), LogFilter{Service: r.Service, Namespace: r.Namespace, Pod: r.Pod, Cluster: r.Cluster, NoTrace: true}, rangeAll, 10); err != nil {
		t.Fatal(err)
	}
}

func TestTheInfoSeriesIsNotCountedAsTelemetry(t *testing.T) {
	m, err := MetricFilter{Service: "cart"}.matchers(AllSignals())
	if err != nil || !strings.Contains(strings.Join(m, ","), `__name__!="ikhnos_application_info"`) {
		t.Fatalf("%v %v", m, err)
	}
	m, _ = MetricFilter{Name: AppInfoMetric}.matchers(AllSignals())
	if strings.Contains(strings.Join(m, ","), `!=`) {
		t.Fatalf("asked for by name, it is readable: %v", m)
	}
}

func TestAVolumeNameWithADotMakesValidPromQL(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
	}
	if _, err := f.client().VolumeUsage(context.Background(), AllSignals(), []string{"data.vol-0"}); err != nil {
		t.Fatal(err)
	}
	q := f.last("/api/v1/query").Get("query")
	if strings.Contains(q, `"data\.vol-0"`) || !strings.Contains(q, `data\\.vol-0`) {
		t.Fatalf("a backslash in a PromQL string must itself be escaped: %s", q)
	}
}
