package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agents"
	"ikhnos/internal/estate"
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/testkit"
)


func reading(agent, cluster string, at time.Time, rss uint64) estate.SelfReading {
	return estate.SelfReading{AgentID: agent, ClusterID: cluster, At: at, Sample: estate.SelfTelemetrySample{RSSBytes: rss, Goroutines: 7, CPUPct: ptr(3.5)}}
}

// What is written is the newest figure of each process, and only one that is still how things are.
func TestSelfMetricsAreTheFreshNewestSampleOfEachCluster(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	srv := estate.SelfReading{At: now.Add(-20 * time.Second), Sample: estate.SelfTelemetrySample{RSSBytes: 1 << 30, Goroutines: 90, ConnectedAgents: ptr(4),
		FlowIngestBytesPerSec: ptr(1000.0), ModelCacheHitPct: ptr(88.0), GCPauseMsPerSec: ptr(0.2)}}
	got := selfMetricsOf(&srv, []estate.SelfReading{
		reading("ag-old", "cl-1", now.Add(-time.Minute), 100),
		reading("ag-new", "cl-1", now.Add(-10*time.Second), 200), // the cluster's agent was replaced
		reading("ag-quiet", "cl-2", now.Add(-selfFreshFor-time.Second), 300),
		reading("ag-nocluster", "", now, 400),
		reading("ag-3", "cl-3", now.Add(-selfFreshFor), 500),
	}, now)
	if got.Server == nil || got.Server.RSSBytes != 1<<30 || *got.Server.ConnectedAgents != 4 || *got.Server.ModelCacheHitPercent != 88 || *got.Server.GCPauseMsPerSec != 0.2 || *got.Server.FlowIngestBytesPerSec != 1000 {
		t.Fatalf("server %+v", got.Server)
	}
	byID := map[string]uint64{}
	for _, c := range got.Clusters {
		byID[c.Cluster] = c.RSSBytes
	}
	if len(byID) != 2 || byID["cl-1"] != 200 || byID["cl-3"] != 500 {
		t.Fatalf("clusters %v: want cl-1 by its newest agent and cl-3 on the edge of fresh; not cl-2 (quiet), not the agent with no cluster", byID)
	}
	// A server that has not sampled for a while is not said to be fine.
	stale := srv
	stale.At = now.Add(-time.Hour)
	if s := selfMetricsOf(&stale, nil, now); s.Server != nil || len(s.Clusters) != 0 {
		t.Fatalf("%+v", s)
	}
	if s := selfMetricsOf(nil, nil, now); s.Server != nil {
		t.Fatalf("%+v", s)
	}
}

// capture is a Prometheus that keeps what is pushed to its OTLP endpoint.
type capture struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newCapture(t *testing.T) *capture {
	c := &capture{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, b)
		c.mu.Unlock()
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *capture) all() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Join(c.bodies, nil)
}

// End to end: the hub's samples (as GET /telemetry/self derives them) become the series pushed to FUSION's Prometheus, only while
// FUSION runs, and the figures that do not exist are not written.
func TestTheSelfSeriesAreWrittenFromTheHubWhileFusionRuns(t *testing.T) {
	d := newDataRig(t)
	prom := newCapture(t)
	d.a.Fusion.Data = &fusionapi.Client{Prometheus: prom.URL}
	h := d.hub()
	ag := twinAgent(t, d.env, h, testkit.FP)
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*ikhnosv1.NodeFacts{testkit.TwinNode("n1", "")}), false)
	now := d.a.C.Now()
	h.Mu.Lock()
	v := h.Views[ag.ID]
	t0 := now.Add(-30 * time.Second)
	v.Self.Add(agents.SelfStatsSample{At: t0, RSSBytes: 111 << 20, Goroutines: 9, CPUSeconds: 1, FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180, LinkBytesCumulative: 1000}, t0)
	v.Self.Add(agents.SelfStatsSample{At: now, RSSBytes: 115 << 20, Goroutines: 9, CPUSeconds: 4, FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180, LinkBytesCumulative: 2000}, now)
	h.Mu.Unlock()
	h.SampleSelfStats(now)

	// FUSION off: nothing is written, and that is not an error.
	if n, err := d.a.PushSelfMetrics(context.Background()); n != 0 || err != nil || len(prom.all()) != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	k := d.a.Fusion.Kube.(*fakeKube)
	for _, n := range fusionNames {
		k.replicas[n] = 1
	}
	k.allReady()
	n, err := d.a.PushSelfMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := prom.all()
	for _, want := range []string{fusionapi.SelfRSSBytes, fusionapi.SelfConnectedAgents, fusionapi.SelfFlowIngestBps, fusionapi.AgentSelfRSSBytes,
		fusionapi.AgentSelfCPUPercent, fusionapi.AgentSelfFlowInterval, "ikhnos_cluster_id"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("%q is not in what was pushed (%d series)", want, n)
		}
	}
	// This cluster has no RAPL and the bandwidth share has no throughput to be a share of; the server's first sample has no rates yet.
	for _, not := range []string{fusionapi.AgentSelfWatts, fusionapi.AgentSelfBandwidth} {
		if bytes.Contains(body, []byte(not)) {
			t.Errorf("%q was written though it was not measured", not)
		}
	}
	if n, err := (*Admin)(nil).PushSelfMetrics(context.Background()); n != 0 || err != nil {
		t.Fatal("no admin, no series")
	}
}
