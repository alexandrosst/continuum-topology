package server

import (
	"net/http"
	"time"

	"continuum/internal/model"
)

// SelfTelemetrySample is one point in one entity's self-telemetry history - see (*Admin).selfTelemetry's
// own doc comment for the full response shape this appears in.
type SelfTelemetrySample struct {
	T          string `json:"t"`
	RSSBytes   uint64 `json:"rssBytes"`
	Goroutines uint32 `json:"goroutines"`
	// CPUPct is this process's own average CPU percent since the previous sample - omitted on an
	// entity's very first sample, since there is nothing yet to derive a rate from (see rateBetween).
	CPUPct *float64 `json:"cpuPct,omitempty"`
	// BandwidthSharePct/Watts are agent entities only (always omitted for the "server" entity, which has
	// no cluster of its own) - see (*Admin).selfTelemetry's own doc comment for exactly what each is,
	// including why both are cluster-wide figures rather than one specific node's own reading.
	BandwidthSharePct *float64 `json:"bandwidthSharePct,omitempty"`
	Watts             *float64 `json:"watts,omitempty"`
	// ContinuumWattsEstimate is always omitted in this pass - see (*Admin).selfTelemetry's own "Deferred"
	// paragraph for why. Kept in the shape now so a client written against it today needs no change
	// once a later pass fills it in.
	ContinuumWattsEstimate *float64 `json:"continuumWattsEstimate,omitempty"`
	// FlowIntervalSeconds/ProbeIntervalSeconds are agent entities only, straight from that sample's own
	// SelfStats - 0 (and so omitted) for the "server" entity, which has no such intervals of its own.
	FlowIntervalSeconds  uint32 `json:"flowIntervalSeconds,omitempty"`
	ProbeIntervalSeconds uint32 `json:"probeIntervalSeconds,omitempty"`
	// ConnectedAgents/FlowIngestBytesPerSec/ModelCacheHitPct/GCPauseMsPerSec are "server" entity only
	// (always omitted for an agent entity, which has none of these of its own) - see
	// serverTelemetrySamples' own doc comment for exactly what each is. Pointers, not plain values, for
	// the same reason FlowIntervalSeconds/ProbeIntervalSeconds are agent-only above: a plain 0 could
	// never be told apart from "not applicable to this entity", and ConnectedAgents in particular has a
	// real, meaningful 0 (no agent currently connected) that must stay visible rather than disappear
	// behind omitempty.
	ConnectedAgents *int `json:"connectedAgents,omitempty"`
	// FlowIngestBytesPerSec is a live snapshot already expressed as a rate (sampleSelfStats derives it
	// itself from its own internal cumulative counters - see ServerSelfStatsSample's own doc comment), so
	// unlike CPUPct/ModelCacheHitPct/GCPauseMsPerSec it needs no further diffing here and is present on
	// every sample, including the first (0 there, same "never a fabricated large rate" contract
	// FlowIngestBytesPerSec's own doc comment already documents).
	FlowIngestBytesPerSec *float64 `json:"flowIngestBytesPerSec,omitempty"`
	// ModelCacheHitPct is this interval's own cache hit rate - diffed hits over diffed (hits+misses)
	// between consecutive samples, not the all-time cumulative ratio, so a recent change in workload
	// shows up promptly instead of being diluted by the server's whole uptime. Omitted on an entity's
	// first sample (nothing to diff yet) and whenever no cache lookup happened at all in the interval
	// (0/0 is "no data", never a fabricated 0% or 100%).
	ModelCacheHitPct *float64 `json:"modelCacheHitPct,omitempty"`
	// GCPauseMsPerSec is milliseconds of Go garbage-collector stop-the-world pause time per second of
	// wall-clock time since the previous sample - runtime.MemStats.PauseTotalNs diffed the same way
	// CPUSeconds already is (rateBetween), then converted from ns to ms. Omitted on an entity's first
	// sample, same as CPUPct.
	GCPauseMsPerSec *float64 `json:"gcPauseMsPerSec,omitempty"`
}

// SelfTelemetryEntity is one agent (one organisation's cluster) or the server itself, with its own
// self-telemetry history - see (*Admin).selfTelemetry's own doc comment for the full response shape.
type SelfTelemetryEntity struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // agent | server
	// ClusterName is the agent's own display name (AgentDoc.Name) - agent entities only.
	ClusterName string                `json:"clusterName,omitempty"`
	Samples     []SelfTelemetrySample `json:"samples"`
}

// rateBetween derives a plain "change per second" from two cumulative readings a real elapsed time
// apart - the same diff-over-elapsed-time treatment every cumulative counter in this package already
// gets from its own caller (compare sampleSelfStats' own FlowIngestBytesPerSec, or oomKillTotal's doc in
// the probe package for the general pattern this mirrors). ok is false whenever that is not meaningful:
// no real elapsed time, or the counter went backwards (a reconnect that restarted it from a lower base,
// the same case sampleSelfStats already guards against) - never a fabricated negative or infinite rate.
func rateBetween(prevCumulative, curCumulative float64, elapsed time.Duration) (float64, bool) {
	secs := elapsed.Seconds()
	if secs <= 0 {
		return 0, false
	}
	d := curCumulative - prevCumulative
	if d < 0 {
		return 0, false
	}
	return d / secs, true
}

// bandwidthSharePct is "Continuum's share of observed throughput" for one agent's cluster - bytesPerSec
// (this agent's own AgentMessage traffic, derived from consecutive SelfStatsSample.LinkBytesCumulative
// readings via rateBetween) divided by clusterThroughputBps (that cluster's total observed throughput,
// every node's own LinkSaturation entries summed - see clusterTotals' own doc comment for why this sums
// across the cluster rather than matching one specific node). Always labeled a *share of* observed
// throughput, never "overhead on top of" it: LinkSaturation already counts every byte on the wire,
// Continuum's own included (see model.Node.LinkSaturation's own doc comment), so this never double-
// counts. nil whenever the denominator is zero/unavailable (no flow collector has ever reported
// LinkSaturation for this cluster) - never a divide-by-zero, never a fabricated 100%.
func bandwidthSharePct(bytesPerSec float64, clusterThroughputBps uint64) *float64 {
	if clusterThroughputBps == 0 || bytesPerSec < 0 {
		return nil
	}
	pct := bytesPerSec / float64(clusterThroughputBps) * 100
	return &pct
}

// clusterTotals sums, across every node model.Topology reports for one cluster, that cluster's total
// observed network throughput (every node's own LinkSaturation entries' ThroughputBps) and total host-
// wide power (every node's own HostWatts, when RAPL exposes one) - see bandwidthSharePct/
// SelfTelemetrySample.Watts' own doc comments for what these are used for. Both are cluster-wide sums
// rather than one specific node's own reading because nothing in this product's facts says which node a
// cluster's own Continuum agent pod happens to be scheduled on (no NODE_NAME-equivalent fact is reported
// anywhere for the agent itself, unlike the node probe's own --node flag) - the cluster-wide total is
// the honest, defensible figure, rather than an unsupported single-node guess. wattsKnown is false only
// when not one node in the cluster has ever reported host_watts (RAPL absent everywhere in it, the
// overwhelmingly common case) - distinguishing that from "every node's watts happened to sum to zero",
// which cannot actually happen (sanitizeWatts in the probe package already rejects a reading of exactly
// the implausible range, but a genuine positive reading can round-trip as a very small, never literal
// zero, figure - the distinction still matters for "no RAPL at all" vs "RAPL everywhere, oddly idle").
func clusterTotals(nodes []model.Node, clusterID string) (throughputBps uint64, watts float64, wattsKnown bool) {
	for _, n := range nodes {
		if n.ClusterID != clusterID {
			continue
		}
		for _, ls := range n.LinkSaturation {
			throughputBps += ls.ThroughputBps
		}
		if n.HostWatts != nil {
			watts += *n.HostWatts
			wattsKnown = true
		}
	}
	return
}

// agentTelemetrySamples turns one agent's raw self-stats ring into the API's own sample shape,
// deriving cpuPct/bandwidthSharePct from consecutive readings (rateBetween) and attaching the cluster's
// current watts total, if any, as a live snapshot on every sample - not a historical reading of its own,
// since this pass keeps no separate power time series (see (*Admin).selfTelemetry's own doc comment).
func agentTelemetrySamples(history []SelfStatsSample, clusterThroughputBps uint64, watts *float64) []SelfTelemetrySample {
	out := make([]SelfTelemetrySample, 0, len(history))
	for i, s := range history {
		d := SelfTelemetrySample{
			T: s.At.UTC().Format(time.RFC3339), RSSBytes: s.RSSBytes, Goroutines: s.Goroutines,
			FlowIntervalSeconds: s.FlowIntervalSeconds, ProbeIntervalSeconds: s.ProbeIntervalSeconds,
			Watts: watts,
		}
		if i > 0 {
			prev := history[i-1]
			elapsed := s.At.Sub(prev.At)
			if cpuRate, ok := rateBetween(prev.CPUSeconds, s.CPUSeconds, elapsed); ok {
				pct := cpuRate * 100
				d.CPUPct = &pct
			}
			if bwRate, ok := rateBetween(float64(prev.LinkBytesCumulative), float64(s.LinkBytesCumulative), elapsed); ok {
				d.BandwidthSharePct = bandwidthSharePct(bwRate, clusterThroughputBps)
			}
		}
		out = append(out, d)
	}
	return out
}

// serverTelemetrySamples is agentTelemetrySamples' counterpart for the "server" entity: the server has
// no cluster, so bandwidthSharePct/watts/the two interval fields never apply and are left unset. It does
// carry four fields of its own that have no agent-entity counterpart - connectedAgents (a plain
// snapshot, always present), flowIngestBytesPerSec (already a rate when sampled, also always present),
// and modelCacheHitPct/gcPauseMsPerSec (both derived here from consecutive cumulative readings via
// rateBetween, the same way cpuPct already is - so omitted on the first sample).
func serverTelemetrySamples(history []ServerSelfStatsSample) []SelfTelemetrySample {
	out := make([]SelfTelemetrySample, 0, len(history))
	for i, s := range history {
		connected, flowRate := s.ConnectedAgents, s.FlowIngestBytesPerSec
		d := SelfTelemetrySample{
			T: s.At.UTC().Format(time.RFC3339), RSSBytes: s.RSSBytes, Goroutines: s.Goroutines,
			ConnectedAgents: &connected, FlowIngestBytesPerSec: &flowRate,
		}
		if i > 0 {
			prev := history[i-1]
			elapsed := s.At.Sub(prev.At)
			if cpuRate, ok := rateBetween(prev.CPUSeconds, s.CPUSeconds, elapsed); ok {
				pct := cpuRate * 100
				d.CPUPct = &pct
			}
			hitsRate, hitsOK := rateBetween(float64(prev.ModelCacheHits), float64(s.ModelCacheHits), elapsed)
			missRate, missOK := rateBetween(float64(prev.ModelCacheMisses), float64(s.ModelCacheMisses), elapsed)
			if hitsOK && missOK {
				if total := hitsRate + missRate; total > 0 {
					pct := hitsRate / total * 100
					d.ModelCacheHitPct = &pct
				}
			}
			if pauseRate, ok := rateBetween(float64(prev.GCPauseTotalNs), float64(s.GCPauseTotalNs), elapsed); ok {
				msPerSec := pauseRate / 1e6
				d.GCPauseMsPerSec = &msPerSec
			}
		}
		out = append(out, d)
	}
	return out
}

// selfTelemetry serves GET /api/v1/orgs/{org}/telemetry/self: what running Continuum itself costs in
// this organisation, over the last hour (selfStatsWindow) at whatever cadence samples actually arrived -
// a heartbeat's own cadence for each agent, selfStatsSampleEvery for the server itself. Meant to be
// genuinely useful read directly, programmatically, not only as a UI data source.
//
// Response: a JSON array of entities, each shaped:
//
//	{
//	  "id": "<agent id, or \"server\">",
//	  "kind": "agent" | "server",
//	  "clusterName": "<the agent's own display name - agent entities only>",
//	  "samples": [{
//	    "t": "<RFC3339, UTC>",
//	    "rssBytes": <uint64>,
//	    "goroutines": <uint32>,
//	    "cpuPct": <float64, omitted on an entity's very first sample>,
//	    "bandwidthSharePct": <float64, agent entities only, omitted when the cluster's own observed
//	       throughput is zero/unknown or there is no previous sample yet - "Continuum's share of
//	       observed throughput", never "overhead on top of" it: LinkSaturation already counts every
//	       byte on the wire, Continuum's own included, so this is a share of the whole, not an addition>,
//	    "watts": <float64, agent entities only, omitted when no node in the cluster exposes RAPL (the
//	       common case on this product's actual target hardware) - that cluster's current total host-
//	       wide power draw, summed across every node that does; a live snapshot attached to every
//	       sample, not a historical reading of its own>,
//	    "continuumWattsEstimate": always omitted in this pass - see "Deferred" below,
//	    "flowIntervalSeconds": <uint32, agent entities only>,
//	    "probeIntervalSeconds": <uint32, agent entities only>,
//	    "connectedAgents": <int, server entity only, always present - len(sessions) at sample time>,
//	    "flowIngestBytesPerSec": <float64, server entity only, always present (0 when there is nothing
//	       yet to derive a rate from) - total AgentMessage_Flows bytes/sec across every agent>,
//	    "modelCacheHitPct": <float64, server entity only, omitted on the first sample or when no cache
//	       lookup happened in the interval - this interval's own twinRT effective-model cache hit rate>,
//	    "gcPauseMsPerSec": <float64, server entity only, omitted on the first sample - Go garbage
//	       collector stop-the-world pause time, in milliseconds per second of wall-clock time>
//	  }, ...]
//	}
//
// An entity with no samples yet (an agent that has never sent a heartbeat carrying SelfStats - an older
// agent build, or one that has not connected since this server started) is left out entirely, rather
// than included with an empty list.
//
// Deferred, both left out of this pass on purpose:
//   - continuumWattsEstimate (Continuum's own estimated share of watts: hostWatts times Continuum's own
//     cgroup CPU share of the node) needs Continuum's own cgroup CPU time summed across the agent, flow-
//     observer and node-probe's own pods - today only the agent's own process CPU time
//     (SelfStats.cpu_seconds) is reported anywhere; the other two have no self-telemetry channel in this
//     pass. Adding one for each, correctly, is a separate piece of work, not a small addition here -
//     left out as a clearly-labeled gap rather than forced as a fragile guess from incomplete data.
//   - Regional-operator self-telemetry: a regional operator deliberately never dials this server at all
//     (an explicit trust-model invariant - see the continuum-regional-operator chart's own comments), so
//     there is currently no channel for one to report anything back over, and this pass does not build one.
func (a *Admin) selfTelemetry(w http.ResponseWriter, r *http.Request) {
	h := a.tn(r).Hub
	doc, err := h.StateFor(r.Context(), false)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []SelfTelemetryEntity{}
	for _, ag := range doc.Agents {
		history := h.SelfStatsFor(ag.ID)
		if len(history) == 0 {
			continue
		}
		throughputBps, wattsSum, wattsKnown := clusterTotals(doc.Topology.Nodes, ag.ClusterID)
		var watts *float64
		if wattsKnown {
			watts = &wattsSum
		}
		out = append(out, SelfTelemetryEntity{ID: ag.ID, Kind: "agent", ClusterName: ag.Name,
			Samples: agentTelemetrySamples(history, throughputBps, watts)})
	}
	if serverHistory := h.ServerSelfStats(); len(serverHistory) > 0 {
		out = append(out, SelfTelemetryEntity{ID: "server", Kind: "server", Samples: serverTelemetrySamples(serverHistory)})
	}
	writeJSON(w, 200, out)
}
