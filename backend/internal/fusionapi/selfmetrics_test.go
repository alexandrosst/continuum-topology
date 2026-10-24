package fusionapi

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"
)

// decodedSelf is a request body read back: for each metric name, its description and its data points as label set to value.
type decodedSelf map[string]struct {
	help   string
	points map[string]float64 // the cluster id ("" for none) to the value
	labels map[string][]string
}

func decodeSelf(t *testing.T, body []byte) decodedSelf {
	t.Helper()
	out := decodedSelf{}
	req := readMsg(t, body)
	rm := readMsg(t, req[1][0])
	sm := readMsg(t, rm[2][0])
	for _, m := range sm[2] {
		metric := readMsg(t, m)
		name := string(metric[1][0])
		if _, dup := out[name]; dup {
			t.Fatalf("%s is written twice in one request", name)
		}
		d := out[name]
		d.help = string(metric[2][0])
		d.points = map[string]float64{}
		d.labels = map[string][]string{}
		for _, p := range readMsg(t, metric[5][0])[1] {
			dp := readMsg(t, p)
			cluster := ""
			for _, a := range dp[7] {
				kv := readMsg(t, a)
				k := string(kv[1][0])
				d.labels[name] = append(d.labels[name], k)
				if k == "ikhnos_cluster_id" {
					cluster = string(readMsg(t, kv[2][0])[1][0])
				}
			}
			if _, dup := d.points[cluster]; dup {
				t.Fatalf("%s{%q} is written twice", name, cluster)
			}
			d.points[cluster] = math.Float64frombits(binary.LittleEndian.Uint64(dp[4][0]))
		}
		out[name] = d
	}
	return out
}

func connected(n int) *int { return &n }

func fullServer() *SelfSample {
	return &SelfSample{RSSBytes: 200 << 20, Goroutines: 120, CPUPercent: f64(12.5), ConnectedAgents: connected(3),
		FlowIngestBytesPerSec: f64(2048), ModelCacheHitPercent: f64(97), GCPauseMsPerSec: f64(0.4)}
}

func TestTheServersOwnFiguresAreOneSeriesEachWithNoLabels(t *testing.T) {
	body, n := EncodeSelfMetrics(SelfMetrics{Server: fullServer()}, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if n != 7 {
		t.Fatalf("%d series, want the server's seven", n)
	}
	got := decodeSelf(t, body)
	want := map[string]float64{SelfRSSBytes: 200 << 20, SelfGoroutines: 120, SelfCPUPercent: 12.5, SelfConnectedAgents: 3,
		SelfFlowIngestBps: 2048, SelfModelCacheHit: 97, SelfGCPauseMs: 0.4}
	if len(got) != len(want) {
		t.Fatalf("%d metrics: %v", len(got), got)
	}
	for name, v := range want {
		d, ok := got[name]
		if !ok || d.points[""] != v || len(d.points) != 1 || len(d.labels[name]) != 0 || d.help == "" {
			t.Errorf("%s = %+v, want %v with no labels and a description", name, d, v)
		}
	}
}

func TestAnAgentsFiguresCarryTheClusterIdAndOnlyWhatWasMeasured(t *testing.T) {
	m := SelfMetrics{Clusters: []ClusterSelf{
		{Cluster: "cl-a", SelfSample: SelfSample{RSSBytes: 100 << 20, Goroutines: 40, FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180}}, // first sample: no CPU rate; no RAPL
		{Cluster: "cl-b", SelfSample: SelfSample{RSSBytes: 90 << 20, Goroutines: 38, CPUPercent: f64(0), BandwidthSharePercent: f64(0.8), Watts: f64(55)}},
	}}
	body, n := EncodeSelfMetrics(m, time.Now())
	got := decodeSelf(t, body)
	if n != 9 { // cl-a: rss, goroutines, flow, probe; cl-b: rss, goroutines, cpu, bandwidth, watts
		t.Fatalf("%d series, want 9", n)
	}
	for name, clusters := range map[string][]string{
		AgentSelfRSSBytes: {"cl-a", "cl-b"}, AgentSelfGoroutines: {"cl-a", "cl-b"},
		AgentSelfCPUPercent: {"cl-b"}, AgentSelfBandwidth: {"cl-b"}, AgentSelfWatts: {"cl-b"},
		AgentSelfFlowInterval: {"cl-a"}, AgentSelfProbeInterval: {"cl-a"},
	} {
		var have []string
		for c := range got[name].points {
			have = append(have, c)
		}
		sort.Strings(have)
		if !reflect.DeepEqual(have, clusters) {
			t.Errorf("%s is written for %v, want %v", name, have, clusters)
		}
		if !reflect.DeepEqual(got[name].labels[name], func() []string {
			l := make([]string, len(clusters))
			for i := range l {
				l[i] = "ikhnos_cluster_id"
			}
			return l
		}()) {
			t.Errorf("%s labels = %v", name, got[name].labels[name])
		}
	}
	// An honest zero is a figure: a CPU of 0 % is written, an interval of 0 s (not said) is not.
	if v, ok := got[AgentSelfCPUPercent].points["cl-b"]; !ok || v != 0 {
		t.Errorf("a measured 0 %% CPU was left out: %v", got[AgentSelfCPUPercent])
	}
}

func TestAFigureThatIsNotANumberIsNotWritten(t *testing.T) {
	s := SelfSample{RSSBytes: 1, CPUPercent: f64(math.NaN()), Watts: f64(math.Inf(1))}
	_, n := EncodeSelfMetrics(SelfMetrics{Server: &s}, time.Now())
	if n != 2 { // rss and goroutines
		t.Fatalf("%d series", n)
	}
}

func TestNothingToSayWritesNothing(t *testing.T) {
	if b, n := EncodeSelfMetrics(SelfMetrics{}, time.Now()); b != nil || n != 0 {
		t.Fatalf("%d %d", len(b), n)
	}
}

// The count of series is bounded by the number of clusters, not by anything an agent says: one id is one cluster, an id-less
// agent has no series, and more than maxSelfClusters are cut.
func TestTheSeriesOfClustersAreSortedOnceEachAndBounded(t *testing.T) {
	in := SelfMetrics{Server: fullServer(), Clusters: []ClusterSelf{
		{Cluster: "cl-b", SelfSample: SelfSample{RSSBytes: 2}}, {Cluster: "", SelfSample: SelfSample{RSSBytes: 9}},
		{Cluster: "cl-a", SelfSample: SelfSample{RSSBytes: 1}}, {Cluster: "cl-b", SelfSample: SelfSample{RSSBytes: 3}}}}
	out := SelfMetricsOf(in)
	if out.Server != in.Server || len(out.Clusters) != 2 || out.Clusters[0].Cluster != "cl-a" || out.Clusters[1].Cluster != "cl-b" || out.Clusters[1].RSSBytes != 2 {
		t.Fatalf("%+v", out.Clusters)
	}
	var many []ClusterSelf
	for i := 0; i < maxSelfClusters+50; i++ {
		many = append(many, ClusterSelf{Cluster: fmt.Sprintf("cl-%05d", i), SelfSample: SelfSample{RSSBytes: 1, Goroutines: 1, CPUPercent: f64(1), BandwidthSharePercent: f64(1),
			Watts: f64(1), FlowIntervalSeconds: 1, ProbeIntervalSeconds: 1}})
	}
	capped := SelfMetricsOf(SelfMetrics{Server: fullServer(), Clusters: many})
	if len(capped.Clusters) != maxSelfClusters || capped.Clusters[maxSelfClusters-1].Cluster != fmt.Sprintf("cl-%05d", maxSelfClusters-1) {
		t.Fatalf("%d clusters", len(capped.Clusters))
	}
	if _, n := EncodeSelfMetrics(capped, time.Now()); n != 7+7*maxSelfClusters {
		t.Fatalf("%d series, want the bound %d", n, 7+7*maxSelfClusters)
	}
}

func TestPushSelfMetricsSendsOnlyWhenThereIsAFigure(t *testing.T) {
	var sent int
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent += len(b)
		path = r.URL.Path
	}))
	defer srv.Close()
	c := &Client{Prometheus: srv.URL}
	if n, err := c.PushSelfMetrics(context.Background(), SelfMetrics{Server: fullServer()}); err != nil || n != 7 || sent == 0 || path != "/api/v1/otlp/v1/metrics" {
		t.Fatalf("%d %v %d %q", n, err, sent, path)
	}
	sent = 0
	if n, err := c.PushSelfMetrics(context.Background(), SelfMetrics{}); err != nil || n != 0 || sent != 0 {
		t.Fatalf("nothing to say sends nothing: %d %v %d", n, err, sent)
	}
	if _, err := (&Client{}).PushSelfMetrics(context.Background(), SelfMetrics{Server: fullServer()}); !IsUnavailable(err) {
		t.Fatalf("no Prometheus: %v", err)
	}
}

// The self series are the system's own: the data API files them under System with the link series, not among the
// applications' metrics.
func TestTheSelfSeriesAreSystemMetrics(t *testing.T) {
	for _, n := range []string{SelfRSSBytes, SelfGoroutines, SelfCPUPercent, SelfConnectedAgents, SelfFlowIngestBps, SelfModelCacheHit, SelfGCPauseMs,
		AgentSelfRSSBytes, AgentSelfGoroutines, AgentSelfCPUPercent, AgentSelfBandwidth, AgentSelfWatts, AgentSelfFlowInterval, AgentSelfProbeInterval} {
		if MetricCategory(n) != CategorySystem {
			t.Errorf("%s is %s", n, MetricCategory(n))
		}
	}
}
