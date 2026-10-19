package server

import (
	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/testkit"
	"testing"
	"time"
)

// TestSelfTelemetryEndpointShapeAndRBAC covers the HTTP handler end to end: RBAC (a viewer may read it,
// an anonymous caller may not), an agent with no self-stats history yet is left out entirely rather than
// listed with an empty samples array, and the server entity is always present once it has sampled itself
// at least once.
func TestSelfTelemetryEndpointShapeAndRBAC(t *testing.T) {
	a := newAdminRig(t)
	_, viewer := a.user(t, "vera", accounts.RoleViewer)
	h := a.hub()

	// Before anything has ever reported SelfStats or sampled itself: an empty array, not an error.
	r := a.do("GET", "/api/v1/telemetry/self", nil, withCookie(viewer))
	if r.Code != 200 {
		t.Fatalf("a viewer may read this endpoint: %d %s", r.Code, r.Body.String())
	}
	if s := r.Body.String(); s != "[]\n" && s != "[]" {
		t.Fatalf("with nothing sampled yet, want an empty array, got %q", s)
	}

	// Anonymous must be refused, the same as every other httpkit.MemberRole route.
	if c := a.do("GET", "/api/v1/telemetry/self", nil).Code; c != 401 {
		t.Fatalf("anonymous: %d, want 401", c)
	}

	ag := twinAgent(t, a.env, h, testkit.FP)
	h.ApplySync(ag, testkit.TwinFull(testkit.FP, []*continuumv1.NodeFacts{testkit.TwinNode("n1", "")}), false)

	// This agent has an approved cluster but has never sent a heartbeat carrying SelfStats: still absent,
	// not listed with an empty samples list.
	r = a.do("GET", "/api/v1/telemetry/self", nil, withCookie(viewer))
	body := r.jsonArray(t)
	for _, em := range body {
		if em["id"] == ag.ID {
			t.Fatalf("an agent with no SelfStats history yet must be left out entirely: %v", em)
		}
	}

	// Give the agent two SelfStats samples directly (the same path noteSelfStats takes from a real
	// heartbeat - see TestNoteSelfStatsCopiesEveryField for that plumbing on its own).
	h.Mu.Lock()
	v := h.Views[ag.ID]
	t0 := time.Unix(5000, 0)
	v.Self.Add(agents.SelfStatsSample{At: t0, RSSBytes: 111 << 20, Goroutines: 9, CPUSeconds: 1, FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180, LinkBytesCumulative: 1000}, t0)
	t1 := t0.Add(10 * time.Second)
	v.Self.Add(agents.SelfStatsSample{At: t1, RSSBytes: 115 << 20, Goroutines: 9, CPUSeconds: 2, FlowIntervalSeconds: 30, ProbeIntervalSeconds: 180, LinkBytesCumulative: 2000}, t1)
	h.Mu.Unlock()
	h.SampleSelfStats(t1)

	r = a.do("GET", "/api/v1/telemetry/self", nil, withCookie(viewer))
	body = r.jsonArray(t)
	var agentEntity, serverEntity map[string]any
	for _, em := range body {
		switch em["kind"] {
		case "agent":
			if em["id"] == ag.ID {
				agentEntity = em
			}
		case "server":
			serverEntity = em
		}
	}
	if agentEntity == nil {
		t.Fatalf("the agent must now appear, body=%v", body)
	}
	if agentEntity["clusterName"] != ag.Name {
		t.Fatalf("clusterName = %v, want %v", agentEntity["clusterName"], ag.Name)
	}
	samples := agentEntity["samples"].([]any)
	if len(samples) != 2 {
		t.Fatalf("%d samples, want 2", len(samples))
	}
	first := samples[0].(map[string]any)
	if _, has := first["cpuPct"]; has {
		t.Fatalf("the first sample must omit cpuPct, got %v", first)
	}
	if _, has := first["watts"]; has {
		t.Fatalf("no node in this cluster ever reported host_watts: watts must be omitted, got %v", first)
	}
	if _, has := first["bandwidthSharePct"]; has {
		t.Fatalf("no flow collector has reported LinkSaturation for this cluster: bandwidthSharePct must be omitted, got %v", first)
	}
	if _, has := first["continuumWattsEstimate"]; has {
		t.Fatalf("continuumWattsEstimate is deferred in this pass and must always be omitted, got %v", first)
	}
	second := samples[1].(map[string]any)
	if pct, ok := second["cpuPct"].(float64); !ok || pct != 10 {
		t.Fatalf("second sample cpuPct = %v, want 10 (1 CPU-second over 10s)", second["cpuPct"])
	}

	if serverEntity == nil {
		t.Fatal("the server entity must appear once it has sampled itself at least once")
	}
	if _, has := serverEntity["clusterName"]; has {
		t.Fatalf("the server entity has no cluster, clusterName must be omitted, got %v", serverEntity)
	}
	serverSamples := serverEntity["samples"].([]any)
	if len(serverSamples) == 0 {
		t.Fatal("the server entity must carry at least one sample")
	}
	lastServerSample := serverSamples[len(serverSamples)-1].(map[string]any)
	if ca, ok := lastServerSample["connectedAgents"].(float64); !ok || ca < 0 {
		t.Fatalf("connectedAgents must be present and sane on the server entity, got %v", lastServerSample["connectedAgents"])
	}
	if _, ok := lastServerSample["flowIngestBytesPerSec"].(float64); !ok {
		t.Fatalf("flowIngestBytesPerSec must be present on the server entity, got %v", lastServerSample["flowIngestBytesPerSec"])
	}
}
