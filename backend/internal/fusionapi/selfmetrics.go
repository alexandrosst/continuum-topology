package fusionapi

import (
	"context"
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// The self series: what running Ikhnos costs, as gauges Grafana can draw. The server's own process and each cluster's
// agent report their memory, goroutines and CPU to the server (GET /telemetry/self shows the last hour of it), and the
// server writes the newest figure of each into FUSION's Prometheus every minute, with the info series (see AppInfoMetric):
//
//	ikhnos_self_rss_bytes                         the server process
//	ikhnos_agent_self_rss_bytes{ikhnos_cluster_id} the agent of one cluster
//
// A dashboard puts a cluster's name on the second by joining ikhnos_cluster_info (whose label is "cluster") on the id.
//
// A figure that does not exist is not written (a CPU rate needs two samples, the power of a cluster needs a node with RAPL,
// the cache hit rate needs a lookup in the interval), so a panel reads "no data" and never a zero that was not measured.
// The units are in the names, not in the OTLP unit field, so that Prometheus' name translation adds nothing to them.

// Names of the server's own series.
const (
	SelfRSSBytes        = "ikhnos_self_rss_bytes"
	SelfGoroutines      = "ikhnos_self_goroutines"
	SelfCPUPercent      = "ikhnos_self_cpu_percent"
	SelfConnectedAgents = "ikhnos_self_connected_agents"
	SelfFlowIngestBps   = "ikhnos_self_flow_ingest_bytes_per_second"
	SelfModelCacheHit   = "ikhnos_self_model_cache_hit_percent"
	SelfGCPauseMs       = "ikhnos_self_gc_pause_ms_per_second"
)

// Names of the series of each cluster's agent, which carry the label ikhnos_cluster_id.
const (
	AgentSelfRSSBytes      = "ikhnos_agent_self_rss_bytes"
	AgentSelfGoroutines    = "ikhnos_agent_self_goroutines"
	AgentSelfCPUPercent    = "ikhnos_agent_self_cpu_percent"
	AgentSelfBandwidth     = "ikhnos_agent_self_bandwidth_share_percent"
	AgentSelfWatts         = "ikhnos_agent_self_watts"
	AgentSelfFlowInterval  = "ikhnos_agent_self_flow_interval_seconds"
	AgentSelfProbeInterval = "ikhnos_agent_self_probe_interval_seconds"
)

// maxSelfClusters bounds the agents one push carries: seven series each, so at most 7,000 and the server's seven.
const maxSelfClusters = 1000

// SelfSample is the newest self-telemetry of one process. A pointer is nil when the figure does not exist for it (see above);
// the two intervals are 0 when the agent did not say.
type SelfSample struct {
	RSSBytes   uint64
	Goroutines uint32
	// CPUPercent is the process's CPU time since the previous sample, in percent of one CPU.
	CPUPercent *float64
	// Server only.
	ConnectedAgents       *int
	FlowIngestBytesPerSec *float64
	ModelCacheHitPercent  *float64
	GCPauseMsPerSec       *float64
	// Agent only.
	BandwidthSharePercent *float64
	Watts                 *float64
	FlowIntervalSeconds   uint32
	ProbeIntervalSeconds  uint32
}

// ClusterSelf is the self-telemetry of the agent of one cluster, by the cluster's id.
type ClusterSelf struct {
	Cluster string
	SelfSample
}

// SelfMetrics is what one push says: the server's own sample, if it has one, and the agents'.
type SelfMetrics struct {
	Server   *SelfSample
	Clusters []ClusterSelf
}

// SelfMetricsOf keeps the clusters that have an id, once each (the first of a repeated id), sorted by id and at most
// maxSelfClusters.
func SelfMetricsOf(in SelfMetrics) SelfMetrics {
	out := SelfMetrics{Server: in.Server}
	seen := map[string]bool{}
	for _, c := range in.Clusters {
		if c.Cluster == "" || seen[c.Cluster] {
			continue
		}
		seen[c.Cluster] = true
		out.Clusters = append(out.Clusters, c)
	}
	sort.Slice(out.Clusters, func(i, j int) bool { return out.Clusters[i].Cluster < out.Clusters[j].Cluster })
	if len(out.Clusters) > maxSelfClusters {
		out.Clusters = out.Clusters[:maxSelfClusters]
	}
	return out
}

// selfPoint is one figure of one sample: the series' name, what it is, and its value, if there is one.
type selfPoint struct {
	name, help string
	value      *float64
}

func f64(v float64) *float64 { return &v }

// nonZero is a figure that is 0 when it was not said (an interval an agent did not report): nil then.
func nonZero(v uint32) *float64 {
	if v == 0 {
		return nil
	}
	return f64(float64(v))
}

func (s SelfSample) serverPoints() []selfPoint {
	var connected *float64
	if s.ConnectedAgents != nil {
		connected = f64(float64(*s.ConnectedAgents))
	}
	return []selfPoint{
		{SelfRSSBytes, "Resident memory of the Ikhnos server process.", f64(float64(s.RSSBytes))},
		{SelfGoroutines, "Goroutines of the Ikhnos server process.", f64(float64(s.Goroutines))},
		{SelfCPUPercent, "CPU the Ikhnos server process used since the previous sample, in percent of one CPU.", s.CPUPercent},
		{SelfConnectedAgents, "Agents with an open stream to the server, in the organisation FUSION belongs to.", connected},
		{SelfFlowIngestBps, "Bytes per second of flow reports the server receives from its agents.", s.FlowIngestBytesPerSec},
		{SelfModelCacheHit, "Share of requests for the effective model served from the cache since the previous sample, in percent.", s.ModelCacheHitPercent},
		{SelfGCPauseMs, "Milliseconds per second the Go garbage collector stopped the server process.", s.GCPauseMsPerSec},
	}
}

func (s SelfSample) agentPoints() []selfPoint {
	return []selfPoint{
		{AgentSelfRSSBytes, "Resident memory of the Ikhnos agent of a cluster.", f64(float64(s.RSSBytes))},
		{AgentSelfGoroutines, "Goroutines of the Ikhnos agent of a cluster.", f64(float64(s.Goroutines))},
		{AgentSelfCPUPercent, "CPU the Ikhnos agent of a cluster used since the previous sample, in percent of one CPU.", s.CPUPercent},
		{AgentSelfBandwidth, "Share of the cluster's observed network throughput that is the agent's own traffic to the server, in percent.", s.BandwidthSharePercent},
		{AgentSelfWatts, "Power draw of the nodes of a cluster that report it (RAPL), summed.", s.Watts},
		{AgentSelfFlowInterval, "Seconds between the flow reports of the agent of a cluster.", nonZero(s.FlowIntervalSeconds)},
		{AgentSelfProbeInterval, "Seconds between the probe rounds of the agent of a cluster.", nonZero(s.ProbeIntervalSeconds)},
	}
}

// EncodeSelfMetrics writes the series as one OTLP/HTTP metrics request body (see EncodeAppInfo), a gauge for each name with a
// data point for each process that has the figure, stamped at. It returns nil, 0 when no process has any figure.
func EncodeSelfMetrics(m SelfMetrics, at time.Time) (body []byte, series int) {
	nano := uint64(at.UnixNano())
	point := func(v float64, cluster string) []byte {
		var dp []byte
		if cluster != "" {
			dp = protowire.AppendTag(dp, 7, protowire.BytesType) // attributes
			dp = protowire.AppendBytes(dp, keyValue(lblCluster, cluster))
		}
		dp = protowire.AppendTag(dp, 3, protowire.Fixed64Type) // time_unix_nano
		dp = protowire.AppendFixed64(dp, nano)
		dp = protowire.AppendTag(dp, 4, protowire.Fixed64Type) // as_double
		dp = protowire.AppendFixed64(dp, math.Float64bits(v))
		return dp
	}
	type gauge struct {
		help   string
		points []byte
	}
	byName := map[string]*gauge{}
	var order []string
	add := func(p selfPoint, cluster string) {
		if p.value == nil || math.IsNaN(*p.value) || math.IsInf(*p.value, 0) {
			return
		}
		g := byName[p.name]
		if g == nil {
			g = &gauge{help: p.help}
			byName[p.name] = g
			order = append(order, p.name)
		}
		g.points = protowire.AppendTag(g.points, 1, protowire.BytesType) // Gauge.data_points
		g.points = protowire.AppendBytes(g.points, point(*p.value, cluster))
		series++
	}
	if m.Server != nil {
		for _, p := range m.Server.serverPoints() {
			add(p, "")
		}
	}
	for _, c := range m.Clusters {
		for _, p := range c.agentPoints() {
			add(p, c.Cluster)
		}
	}
	if series == 0 {
		return nil, 0
	}
	var metrics [][]byte
	for _, n := range order {
		metrics = append(metrics, gaugeMetric(n, byName[n].help, byName[n].points))
	}
	return metricsRequest(metrics...), series
}

// PushSelfMetrics writes the self series and reports how many series it sent. With nothing to say it sends nothing: what was
// said before ages out of Prometheus within five minutes.
func (c *Client) PushSelfMetrics(ctx context.Context, m SelfMetrics) (int, error) {
	body, n := EncodeSelfMetrics(SelfMetricsOf(m), c.now())
	if n == 0 {
		return 0, nil
	}
	if err := c.pushOTLP(ctx, body, "the self series"); err != nil {
		return 0, err
	}
	return n, nil
}
