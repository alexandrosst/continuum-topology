package exporthealth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// The counters as the real collector (0.160.0) serves them: no _total suffix, the exporter id as the label,
// and the address an OTLP exporter talks to as extra labels.
const sample = `# HELP otelcol_exporter_sent_metric_points Number of metric points successfully sent to destination.
# TYPE otelcol_exporter_sent_metric_points counter
otelcol_exporter_sent_metric_points{exporter="otlphttp/metrics",otel_scope_name="x",server_address="127.0.0.1",server_port="9999",url_path="/v1/metrics"} 126
otelcol_exporter_send_failed_metric_points{exporter="otlphttp/bad",server_address="127.0.0.1",server_port="9998",url_path="/v1/metrics"} 7
otelcol_exporter_sent_log_records{exporter="otlphttp/logs"} 1
otelcol_exporter_sent_spans{exporter="zipkin/traces"} 3
otelcol_exporter_sent_spans{exporter="debug"} 99
otelcol_exporter_queue_size{exporter="otlp",data_type="logs"} 0
otelcol_exporter_in_flight_requests{exporter="otlp"} 0
otelcol_processor_batch_batch_send_size_sum{processor="batch"} 12
`

func TestParseReadsExportCountersOnly(t *testing.T) {
	got, err := parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	want := map[route]counts{
		{"otlphttp/metrics", "metrics"}: {sent: 126},
		{"otlphttp/bad", "metrics"}:     {failed: 7},
		{"otlphttp/logs", "logs"}:       {sent: 1},
		{"zipkin/traces", "traces"}:     {sent: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%v = %+v, want %+v", k, got[k], v)
		}
	}
}

func TestParseAcceptsTotalSuffixSumsSeriesAndHandlesAwkwardLabels(t *testing.T) {
	got, err := parse(strings.NewReader(`otelcol_exporter_sent_spans_total{exporter="otlp",server_address="a",url_path="/v1/{x},\"y\""} 5 1700000000000
otelcol_exporter_sent_spans_total{exporter="otlp",server_address="b"} 7
otelcol_exporter_send_failed_spans_total{exporter="otlp"} 1.0
otelcol_exporter_sent_spans{exporter="otlp",bad
otelcol_exporter_sent_spans{exporter="otlp"} NaN
otelcol_exporter_sent_spans{exporter="otlp"} -3
otelcol_exporter_sent_spans{exporter=""} 4
`))
	if err != nil {
		t.Fatal(err)
	}
	if c := got[route{"otlp", "traces"}]; c.sent != 12 || c.failed != 1 || len(got) != 1 {
		t.Errorf("got %+v, want one route with 12 sent and 1 failed", got)
	}
}

// fleet is a set of fake collector pods whose counters a test moves.
type fleet struct {
	mu   sync.Mutex
	dns  map[string][]string // name -> addresses
	body map[string]string   // address -> metrics text
	err  map[string]error    // address -> failure
	now  time.Time
}

func newFleet() *fleet {
	return &fleet{dns: map[string][]string{}, body: map[string]string{}, err: map[string]error{}, now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
}

func (f *fleet) monitor(targets ...string) *Monitor {
	return New(Config{
		Targets: targets, Window: 3 * time.Minute,
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		Lookup: func(_ context.Context, host string) ([]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			ips, ok := f.dns[host]
			if !ok {
				return nil, fmt.Errorf("no such host")
			}
			return ips, nil
		},
		Get: func(_ context.Context, url string) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			addr := strings.TrimSuffix(strings.TrimPrefix(url, "http://"), "/metrics")
			if err := f.err[addr]; err != nil {
				return nil, err
			}
			b, ok := f.body[addr]
			if !ok {
				return nil, fmt.Errorf("connection refused")
			}
			return []byte(b), nil
		},
	})
}

func (f *fleet) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }
func (f *fleet) set(addr, text string)   { f.mu.Lock(); f.body[addr] = text; f.mu.Unlock() }

func counter(exporter, signal string, sent, failed int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "otelcol_exporter_sent_%s{exporter=%q} %d\n", signal, exporter, sent)
	fmt.Fprintf(&b, "otelcol_exporter_send_failed_%s{exporter=%q} %d\n", signal, exporter, failed)
	return b.String()
}

func routeOf(t *testing.T, h *continuumv1.ExportHealth, exporter, signal string) *continuumv1.ExportRouteHealth {
	t.Helper()
	if h == nil {
		t.Fatal("no report")
	}
	for _, r := range h.Routes {
		if r.Exporter == exporter && r.Signal == signal {
			return r
		}
	}
	t.Fatalf("no route %s/%s in %+v", exporter, signal, h.Routes)
	return nil
}

func TestStateFollowsGrowthNotCounterValues(t *testing.T) {
	f := newFleet()
	f.dns["host.svc"] = []string{"10.0.0.1"}
	m := f.monitor("host.svc:8888")
	ctx := context.Background()
	if m.Report() != nil {
		t.Fatal("a report before any reading")
	}

	// The first reading of a pod is a baseline: counters already above zero say nothing about now.
	f.set("10.0.0.1:8888", counter("otlp", "log_records", 500, 0))
	m.Scrape(ctx)
	if got := routeOf(t, m.Report(), "otlp", "logs"); got.State != continuumv1.ExportRouteHealth_WAITING || got.Sent != 500 {
		t.Fatalf("after the first reading: %+v, want waiting with 500 sent", got)
	}

	// It grows: exporting.
	f.advance(30 * time.Second)
	f.set("10.0.0.1:8888", counter("otlp", "log_records", 520, 0))
	m.Scrape(ctx)
	if got := routeOf(t, m.Report(), "otlp", "logs"); got.State != continuumv1.ExportRouteHealth_EXPORTING || got.LastSentAt == nil {
		t.Fatalf("after growth: %+v, want exporting", got)
	}

	// It stops, and stays stopped past the window: silent, never an error by itself.
	for i := 0; i < 8; i++ {
		f.advance(30 * time.Second)
		m.Scrape(ctx)
	}
	if got := routeOf(t, m.Report(), "otlp", "logs"); got.State != continuumv1.ExportRouteHealth_SILENT {
		t.Fatalf("after 4 minutes without growth: %+v, want silent", got)
	}

	// Sends start failing: failing, with the count.
	f.advance(30 * time.Second)
	f.set("10.0.0.1:8888", counter("otlp", "log_records", 520, 4))
	m.Scrape(ctx)
	got := routeOf(t, m.Report(), "otlp", "logs")
	if got.State != continuumv1.ExportRouteHealth_FAILING || got.Failed != 4 || got.LastFailedAt == nil {
		t.Fatalf("after failures: %+v, want failing", got)
	}

	// A send gets through again after the failures: exporting again, even though the failures stay counted.
	f.advance(30 * time.Second)
	f.set("10.0.0.1:8888", counter("otlp", "log_records", 530, 4))
	m.Scrape(ctx)
	if got := routeOf(t, m.Report(), "otlp", "logs"); got.State != continuumv1.ExportRouteHealth_EXPORTING {
		t.Fatalf("after recovery: %+v, want exporting", got)
	}
}

func TestFailuresWithNothingEverSentAreFailing(t *testing.T) {
	f := newFleet()
	f.dns["c.svc"] = []string{"10.0.0.9"}
	m := f.monitor("c.svc:8888")
	f.set("10.0.0.9:8888", "")
	m.Scrape(context.Background())
	// Nothing has been counted yet at all: waiting, not failing.
	if h := m.Report(); len(h.Routes) != 0 || h.PodsReached != 1 {
		t.Fatalf("report = %+v", h)
	}
	f.advance(30 * time.Second)
	f.set("10.0.0.9:8888", counter("zipkin/traces", "spans", 0, 2))
	m.Scrape(context.Background())
	if got := routeOf(t, m.Report(), "zipkin/traces", "traces"); got.State != continuumv1.ExportRouteHealth_FAILING {
		t.Fatalf("got %+v, want failing", got)
	}
}

func TestRestartedPodCountsFromZeroAndIsNotMistakenForSilence(t *testing.T) {
	f := newFleet()
	f.dns["host.svc"] = []string{"10.0.0.1"}
	m := f.monitor("host.svc:8888")
	f.set("10.0.0.1:8888", counter("otlp", "metric_points", 9000, 0))
	m.Scrape(context.Background())
	f.advance(30 * time.Second)
	f.set("10.0.0.1:8888", counter("otlp", "metric_points", 40, 0)) // the collector restarted
	m.Scrape(context.Background())
	if got := routeOf(t, m.Report(), "otlp", "metrics"); got.State != continuumv1.ExportRouteHealth_EXPORTING {
		t.Fatalf("got %+v, want exporting after a counter reset", got)
	}
}

func TestSumsOverPodsAndSurvivesOneUnreachablePod(t *testing.T) {
	f := newFleet()
	f.dns["host.svc"] = []string{"10.0.0.1", "10.0.0.2"}
	m := f.monitor("host.svc:8888")
	f.set("10.0.0.1:8888", counter("otlp", "metric_points", 10, 0))
	f.set("10.0.0.2:8888", counter("otlp", "metric_points", 20, 0))
	m.Scrape(context.Background())
	if got := routeOf(t, m.Report(), "otlp", "metrics"); got.Sent != 30 {
		t.Fatalf("sent = %d, want 30 summed over both pods", got.Sent)
	}

	// One pod stops answering: it is counted and named, and the other still tells.
	f.advance(30 * time.Second)
	f.mu.Lock()
	f.err["10.0.0.2:8888"] = fmt.Errorf("connection reset")
	f.mu.Unlock()
	f.set("10.0.0.1:8888", counter("otlp", "metric_points", 15, 0))
	m.Scrape(context.Background())
	h := m.Report()
	if h.PodsReached != 1 || h.PodsFailed != 1 || !strings.Contains(h.LastError, "10.0.0.2") {
		t.Fatalf("report = %+v", h)
	}
	if got := routeOf(t, m.Report(), "otlp", "metrics"); got.State != continuumv1.ExportRouteHealth_EXPORTING {
		t.Fatalf("got %+v, want exporting from the pod that answered", got)
	}

	// The second pod comes back with the same counter: not new growth, and no bogus jump from a lost baseline.
	f.advance(30 * time.Second)
	f.mu.Lock()
	delete(f.err, "10.0.0.2:8888")
	f.mu.Unlock()
	m.Scrape(context.Background())
	if h := m.Report(); h.PodsFailed != 0 || h.PodsReached != 2 {
		t.Fatalf("report = %+v", h)
	}
}

func TestUnreadableCollectorsAreReportedNotMistakenForSilence(t *testing.T) {
	f := newFleet()
	m := f.monitor("gone.svc:8888", "bad-target")
	m.Scrape(context.Background())
	h := m.Report()
	if h == nil || h.PodsReached != 0 || h.PodsFailed != 2 || h.LastError == "" || len(h.Routes) != 0 {
		t.Fatalf("report = %+v, want nothing reached and the reasons said", h)
	}
}

func TestParseTargets(t *testing.T) {
	got := ParseTargets(" a:1, b:2 ,,a:1")
	if len(got) != 2 || got[0] != "a:1" || got[1] != "b:2" {
		t.Errorf("got %v", got)
	}
	if ParseTargets("") != nil {
		t.Error("no targets must be nil")
	}
}
