package estate

import (
	"continuum/internal/agents"
	"continuum/internal/model"
	"testing"
	"time"
)

// TestRateBetweenDerivesAPlainPerSecondDiff covers rateBetween's own contract: a real diff over a real
// elapsed time is ok, and every "not meaningful" case (no elapsed time, the counter went backwards) is
// false, never a fabricated negative or infinite rate - see rateBetween's own doc comment.
func TestRateBetweenDerivesAPlainPerSecondDiff(t *testing.T) {
	if got, ok := rateBetween(10, 30, 2*time.Second); !ok || got != 10 {
		t.Fatalf("rate = %v, ok=%v, want 10, true", got, ok)
	}
	if _, ok := rateBetween(10, 30, 0); ok {
		t.Fatal("zero elapsed time must be false, not a divide-by-zero")
	}
	if _, ok := rateBetween(10, 30, -time.Second); ok {
		t.Fatal("negative elapsed time must be false")
	}
	if got, ok := rateBetween(30, 10, time.Second); ok || got != 0 {
		t.Fatalf("a counter that went backwards must be false/0, got %v, ok=%v", got, ok)
	}
}

// TestBandwidthSharePctOmitsOnZeroOrUnavailableDenominator covers the explicit "never divide by zero,
// never show a fake 100%" requirement: a zero or negative-rate input must come back nil, and a real
// computation must be a plain percentage of the cluster's own observed throughput.
func TestBandwidthSharePctOmitsOnZeroOrUnavailableDenominator(t *testing.T) {
	if got := bandwidthSharePct(500, 0); got != nil {
		t.Fatalf("zero denominator (never reported) must be nil, got %v", *got)
	}
	if got := bandwidthSharePct(-1, 1000); got != nil {
		t.Fatalf("a negative rate must be nil, got %v", *got)
	}
	got := bandwidthSharePct(250, 1000)
	if got == nil || *got != 25 {
		t.Fatalf("250/1000 = 25%%, got %v", got)
	}
}

// TestClusterTotalsSumsOnlyNodesInTheGivenCluster covers clusterTotals' own filtering (another
// cluster's nodes must never leak into this one's totals) and its wattsKnown distinction: false when
// not one node in the cluster has ever reported host_watts (RAPL absent everywhere in it), true as soon
// as even one has, with the sum covering only the ones that did.
func TestClusterTotalsSumsOnlyNodesInTheGivenCluster(t *testing.T) {
	w1, w2 := 100.0, 50.0
	nodes := []model.Node{
		{ClusterID: "a", LinkSaturation: []model.LinkSaturation{{Iface: "eth0", ThroughputBps: 1000}}, HostWatts: &w1},
		{ClusterID: "a", LinkSaturation: []model.LinkSaturation{{Iface: "eth0", ThroughputBps: 500}}}, // no RAPL on this node
		{ClusterID: "b", LinkSaturation: []model.LinkSaturation{{Iface: "eth0", ThroughputBps: 9999}}, HostWatts: &w2},
	}
	bps, watts, known := clusterTotals(nodes, "a")
	if bps != 1500 {
		t.Fatalf("throughputBps = %d, want 1500 (only cluster a's nodes)", bps)
	}
	if !known || watts != 100 {
		t.Fatalf("watts = %v, known=%v, want 100, true (only the one node in cluster a that reported it)", watts, known)
	}

	// A cluster where not one node has ever reported host_watts: known must be false, not a fabricated 0.
	_, watts, known = clusterTotals(nodes[1:2], "a")
	if known || watts != 0 {
		t.Fatalf("no node in this cluster reported watts: watts=%v known=%v, want 0, false", watts, known)
	}

	// An unknown cluster id matches nothing.
	bps, _, known = clusterTotals(nodes, "no-such-cluster")
	if bps != 0 || known {
		t.Fatalf("unknown cluster: bps=%d known=%v, want 0, false", bps, known)
	}
}

// TestAgentTelemetrySamplesOmitsRatesOnTheFirstSampleAndDerivesThemOnLater covers the shape
// agentTelemetrySamples builds: no cpuPct/bandwidthSharePct on the very first sample (nothing to diff
// yet), real ones derived from consecutive readings afterwards, and watts/the interval fields carried
// straight through on every sample.
func TestAgentTelemetrySamplesOmitsRatesOnTheFirstSampleAndDerivesThemOnLater(t *testing.T) {
	base := time.Unix(1000, 0)
	history := []agents.SelfStatsSample{
		{At: base, RSSBytes: 111, Goroutines: 10, CPUSeconds: 1, FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180, LinkBytesCumulative: 1000},
		{At: base.Add(10 * time.Second), RSSBytes: 222, Goroutines: 12, CPUSeconds: 3, FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180, LinkBytesCumulative: 6000},
	}
	watts := 42.0
	out := agentTelemetrySamples(history, 10000, &watts)
	if len(out) != 2 {
		t.Fatalf("%d samples, want 2", len(out))
	}
	if out[0].CPUPct != nil || out[0].BandwidthSharePct != nil {
		t.Fatalf("first sample must have no rate yet: %+v", out[0])
	}
	if out[0].Watts == nil || *out[0].Watts != 42 {
		t.Fatalf("watts must be carried through even on the first sample: %+v", out[0].Watts)
	}
	if out[0].FlowIntervalSeconds != 30 || out[0].ProbeIntervalSeconds != 180 {
		t.Fatalf("interval fields must be copied straight through: %+v", out[0])
	}
	// The four server-only fields (added alongside flow/bandwidth telemetry) must never be set on an
	// agent entity's samples - they have no agent-entity meaning.
	if out[0].ConnectedAgents != nil || out[0].FlowIngestBytesPerSec != nil || out[0].ModelCacheHitPct != nil || out[0].GCPauseMsPerSec != nil {
		t.Fatalf("server-only fields must stay unset on an agent entity's samples: %+v", out[0])
	}
	// 2 CPU-seconds over 10s = 20% CPU. 5000 more link bytes over 10s = 500 B/s, / 10000 Bps = 5%.
	if out[1].CPUPct == nil || *out[1].CPUPct != 20 {
		t.Fatalf("cpuPct = %v, want 20", out[1].CPUPct)
	}
	if out[1].BandwidthSharePct == nil || *out[1].BandwidthSharePct != 5 {
		t.Fatalf("bandwidthSharePct = %v, want 5", out[1].BandwidthSharePct)
	}
	if out[1].Watts == nil || *out[1].Watts != 42 {
		t.Fatalf("watts must still be carried through on a later sample: %+v", out[1].Watts)
	}

	// A nil watts (no RAPL anywhere in the cluster) must stay nil, never a fabricated reading.
	out = agentTelemetrySamples(history, 10000, nil)
	if out[0].Watts != nil || out[1].Watts != nil {
		t.Fatalf("nil watts in must mean nil watts out: %+v / %+v", out[0].Watts, out[1].Watts)
	}

	// A zero cluster throughput (no flow collector has ever reported LinkSaturation) must omit the share,
	// never divide by zero.
	out = agentTelemetrySamples(history, 0, &watts)
	if out[1].BandwidthSharePct != nil {
		t.Fatalf("zero cluster throughput must omit bandwidthSharePct, got %v", *out[1].BandwidthSharePct)
	}
}

// TestServerTelemetrySamplesNeverSetsAgentOnlyFields covers serverTelemetrySamples' own documented
// contract: bandwidthSharePct/watts/the interval fields never apply to the "server" entity and must stay
// unset (omitted), while cpuPct is still derived the same way agentTelemetrySamples derives its own.
func TestServerTelemetrySamplesNeverSetsAgentOnlyFields(t *testing.T) {
	base := time.Unix(2000, 0)
	history := []agents.ServerSelfStatsSample{
		{At: base, RSSBytes: 50 << 20, Goroutines: 20, CPUSeconds: 2},
		{At: base.Add(5 * time.Second), RSSBytes: 60 << 20, Goroutines: 21, CPUSeconds: 3},
	}
	out := serverTelemetrySamples(history)
	if len(out) != 2 {
		t.Fatalf("%d samples, want 2", len(out))
	}
	if out[0].CPUPct != nil {
		t.Fatalf("first sample must have no cpuPct yet: %+v", out[0])
	}
	if out[1].CPUPct == nil || *out[1].CPUPct != 20 {
		t.Fatalf("cpuPct = %v, want 20 (1 CPU-second over 5s)", out[1].CPUPct)
	}
	for i, s := range out {
		if s.BandwidthSharePct != nil || s.Watts != nil || s.FlowIntervalSeconds != 0 || s.ProbeIntervalSeconds != 0 {
			t.Fatalf("sample %d: agent-only fields must stay unset for the server entity: %+v", i, s)
		}
	}
}

// TestServerTelemetrySamplesDerivesConnectedAgentsFlowRateCacheHitPctAndGCPause covers
// serverTelemetrySamples' own four server-only fields: connectedAgents/flowIngestBytesPerSec are plain
// snapshots present even on the first sample, while modelCacheHitPct/gcPauseMsPerSec are diffed between
// consecutive samples the same way cpuPct already is, and so omitted on the first.
func TestServerTelemetrySamplesDerivesConnectedAgentsFlowRateCacheHitPctAndGCPause(t *testing.T) {
	base := time.Unix(3000, 0)
	history := []agents.ServerSelfStatsSample{
		{At: base, RSSBytes: 50 << 20, Goroutines: 20, CPUSeconds: 2, ConnectedAgents: 3, FlowIngestBytesPerSec: 0,
			ModelCacheHits: 10, ModelCacheMisses: 5, GCPauseTotalNs: 1_000_000},
		{At: base.Add(5 * time.Second), RSSBytes: 60 << 20, Goroutines: 21, CPUSeconds: 3, ConnectedAgents: 4,
			FlowIngestBytesPerSec: 500, ModelCacheHits: 18, ModelCacheMisses: 7, GCPauseTotalNs: 6_000_000},
	}
	out := serverTelemetrySamples(history)
	if len(out) != 2 {
		t.Fatalf("%d samples, want 2", len(out))
	}

	if out[0].ConnectedAgents == nil || *out[0].ConnectedAgents != 3 {
		t.Fatalf("first sample connectedAgents = %v, want 3", out[0].ConnectedAgents)
	}
	if out[0].FlowIngestBytesPerSec == nil || *out[0].FlowIngestBytesPerSec != 0 {
		t.Fatalf("first sample flowIngestBytesPerSec = %v, want 0", out[0].FlowIngestBytesPerSec)
	}
	if out[0].ModelCacheHitPct != nil || out[0].GCPauseMsPerSec != nil {
		t.Fatalf("first sample must omit the diffed fields, nothing to diff against yet: %+v", out[0])
	}

	if out[1].ConnectedAgents == nil || *out[1].ConnectedAgents != 4 {
		t.Fatalf("second sample connectedAgents = %v, want 4", out[1].ConnectedAgents)
	}
	if out[1].FlowIngestBytesPerSec == nil || *out[1].FlowIngestBytesPerSec != 500 {
		t.Fatalf("second sample flowIngestBytesPerSec = %v, want 500", out[1].FlowIngestBytesPerSec)
	}
	// 8 more hits, 2 more misses over 5s -> hit rate = 8/(8+2) = 80%.
	if out[1].ModelCacheHitPct == nil || *out[1].ModelCacheHitPct != 80 {
		t.Fatalf("modelCacheHitPct = %v, want 80", out[1].ModelCacheHitPct)
	}
	// 5,000,000 more pause-ns over 5s = 1,000,000 ns/s = 1 ms/s.
	if out[1].GCPauseMsPerSec == nil || *out[1].GCPauseMsPerSec != 1 {
		t.Fatalf("gcPauseMsPerSec = %v, want 1", out[1].GCPauseMsPerSec)
	}
}

// TestServerTelemetrySamplesOmitsModelCacheHitPctWhenNoLookupHappened covers the explicit "0/0 is no
// data, never a fabricated 0% or 100%" requirement: an interval with no cache hits or misses at all
// must omit modelCacheHitPct rather than divide by zero.
func TestServerTelemetrySamplesOmitsModelCacheHitPctWhenNoLookupHappened(t *testing.T) {
	base := time.Unix(4000, 0)
	history := []agents.ServerSelfStatsSample{
		{At: base, ModelCacheHits: 5, ModelCacheMisses: 5},
		{At: base.Add(time.Second), ModelCacheHits: 5, ModelCacheMisses: 5},
	}
	out := serverTelemetrySamples(history)
	if out[1].ModelCacheHitPct != nil {
		t.Fatalf("no lookups in the interval must omit modelCacheHitPct, got %v", *out[1].ModelCacheHitPct)
	}
}
