package server

import (
	"context"
	"log/slog"
	"time"

	"ikhnos/internal/estate"
	"ikhnos/internal/fusionapi"
)

// selfMetricsEvery is how often the self series (fusionapi.SelfMetrics) are written into FUSION's Prometheus. The server's
// own sample is taken every 30 seconds and an agent's comes with its heartbeat, so a minute shows each figure about once.
const selfMetricsEvery = time.Minute

// selfFreshFor is how old a sample may be and still be said to be how things are. A cluster whose agent has gone quiet keeps
// its last sample in the hub for an hour (the history GET /telemetry/self shows), but a gauge that went on repeating it would
// be read as the agent being alive; past this the series is not written and Prometheus lets it go within five minutes.
const selfFreshFor = 3 * time.Minute

// selfMetricser is what an Extras that holds the hub adds to it: the self-telemetry of the server and of the agents.
type selfMetricser interface {
	SelfMetrics(ctx context.Context) (fusionapi.SelfMetrics, error)
}

// SelfMetrics is the newest self-telemetry of FUSION's organisation: the server process (its connected agents and the flow it
// takes in are the organisation's own, as the hub counts them) and the agent of each cluster, by cluster id. A sample older
// than selfFreshFor is left out, and so is an agent that has not said which cluster it is in. Of two agents of one cluster
// (one that was replaced is still in the hub until it is forgotten) the one heard from last is the cluster's.
func (e fusionExtras) SelfMetrics(ctx context.Context) (fusionapi.SelfMetrics, error) {
	t, err := e.tenant(ctx)
	if err != nil {
		return fusionapi.SelfMetrics{}, err
	}
	srv, agents, err := t.Hub.LatestSelfTelemetry(ctx)
	if err != nil {
		return fusionapi.SelfMetrics{}, &fusionapi.Error{Status: 503, Msg: "the organisation's self-telemetry is not available"}
	}
	return selfMetricsOf(srv, agents, e.a.C.Now()), nil
}

// selfMetricsOf turns the hub's newest samples into the series to write (see SelfMetrics).
func selfMetricsOf(srv *estate.SelfReading, agents []estate.SelfReading, now time.Time) fusionapi.SelfMetrics {
	fresh := func(r estate.SelfReading) bool { return !r.At.IsZero() && now.Sub(r.At) <= selfFreshFor }
	var out fusionapi.SelfMetrics
	if srv != nil && fresh(*srv) {
		s := fusionapi.SelfSample{RSSBytes: srv.Sample.RSSBytes, Goroutines: srv.Sample.Goroutines, CPUPercent: srv.Sample.CPUPct,
			ConnectedAgents: srv.Sample.ConnectedAgents, FlowIngestBytesPerSec: srv.Sample.FlowIngestBytesPerSec,
			ModelCacheHitPercent: srv.Sample.ModelCacheHitPct, GCPauseMsPerSec: srv.Sample.GCPauseMsPerSec}
		out.Server = &s
	}
	newest := map[string]estate.SelfReading{}
	for _, a := range agents {
		if a.ClusterID == "" || !fresh(a) {
			continue
		}
		if prev, ok := newest[a.ClusterID]; !ok || a.At.After(prev.At) || (a.At.Equal(prev.At) && a.AgentID < prev.AgentID) {
			newest[a.ClusterID] = a
		}
	}
	for id, a := range newest {
		out.Clusters = append(out.Clusters, fusionapi.ClusterSelf{Cluster: id, SelfSample: fusionapi.SelfSample{
			RSSBytes: a.Sample.RSSBytes, Goroutines: a.Sample.Goroutines, CPUPercent: a.Sample.CPUPct,
			BandwidthSharePercent: a.Sample.BandwidthSharePct, Watts: a.Sample.Watts,
			FlowIntervalSeconds: a.Sample.FlowIntervalSeconds, ProbeIntervalSeconds: a.Sample.ProbeIntervalSeconds}})
	}
	return out
}

// PushSelfMetrics writes the self series into FUSION's Prometheus and reports how many series it wrote. Like the info series it
// writes nothing, and says so with no error, when FUSION is not running or this server has nothing to give.
func (a *Admin) PushSelfMetrics(ctx context.Context) (int, error) {
	if a == nil || a.Fusion == nil || a.Fusion.Kube == nil {
		return 0, nil
	}
	if st := a.Fusion.TokenStatus(ctx); st.State != "running" {
		return 0, nil
	}
	sm, ok := a.fusionExtras().(selfMetricser)
	if !ok {
		return 0, nil
	}
	m, err := sm.SelfMetrics(ctx)
	if err != nil {
		return 0, err
	}
	return a.Fusion.DataClient().PushSelfMetrics(ctx, m)
}

// RunSelfMetrics keeps the self series written until ctx ends. A failure is logged when it starts and when it ends, not every
// minute.
func (a *Admin) RunSelfMetrics(ctx context.Context, log *slog.Logger) {
	failing := false
	for wait := 30 * time.Second; ; wait = selfMetricsEvery {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := a.PushSelfMetrics(c)
		cancel()
		switch {
		case err != nil && !failing:
			failing = true
			log.Warn("could not write FUSION's self series; the Ikhnos self dashboard in Grafana is empty until it can", "err", err)
		case err == nil && failing:
			failing = false
			log.Info("FUSION's self series are being written again", "series", n)
		}
	}
}
