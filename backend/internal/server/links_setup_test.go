package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

var setupNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// caps is a fresh report: Cluster links and probing on, telemetry sent onward, the prober seen a minute ago.
func caps(f ...func(*agents.LinkCaps)) agents.LinkCaps {
	c := agents.LinkCaps{Reported: setupNow.Add(-30 * time.Second), LinkMetrics: true, ProbeEnabled: true, TelemetryExport: true, ProberSeen: setupNow.Add(-time.Minute)}
	for _, g := range f {
		g(&c)
	}
	return c
}

func without(what string) func(*agents.LinkCaps) {
	return func(c *agents.LinkCaps) {
		switch what {
		case "links":
			c.LinkMetrics = false
		case "probe":
			c.ProbeEnabled = false
		case "export":
			c.TelemetryExport = false
		case "prober":
			c.ProberSeen = time.Time{}
		}
	}
}

func with(what string) func(*agents.LinkCaps) {
	return func(c *agents.LinkCaps) {
		switch what {
		case "capacity":
			c.CapacityEnabled = true
		}
	}
}

func prober(ago time.Duration) func(*agents.LinkCaps) {
	return func(c *agents.LinkCaps) { c.ProberSeen = setupNow.Add(-ago) }
}

func reported(ago time.Duration) func(*agents.LinkCaps) {
	return func(c *agents.LinkCaps) { c.Reported = setupNow.Add(-ago) }
}

func endOf(id string, c agents.LinkCaps) setupEnd {
	return setupEnd{ID: id, Name: strings.ToUpper(id), HasAgent: true, Tier: 2, Caps: c}
}

// states writes the steps as "code/cluster=state", in order.
func states(steps []LinkSetupStep) string {
	var out []string
	for _, s := range steps {
		k := s.Code
		if s.Cluster != "" {
			k += "/" + s.Cluster
		}
		out = append(out, k+"="+s.State)
	}
	return strings.Join(out, " ")
}

func step(t *testing.T, steps []LinkSetupStep, code, cluster string) LinkSetupStep {
	t.Helper()
	for _, s := range steps {
		if s.Code == code && s.Cluster == cluster {
			return s
		}
	}
	t.Fatalf("no %s step for %q in %s", code, cluster, states(steps))
	return LinkSetupStep{}
}

func TestPairChecklistStatesTable(t *testing.T) {
	plain := kernel.LinkSetting{A: "a", B: "b"}
	tput := kernel.LinkSetting{A: "a", B: "b", Throughput: &kernel.LinkThroughput{}}
	passive := kernel.LinkSetting{A: "a", B: "b", NoProbe: true}
	old := setupEnd{ID: "b", Name: "B", HasAgent: true, Tier: 2}  // an agent that never reported
	oldA := setupEnd{ID: "a", Name: "A", HasAgent: true, Tier: 2} // the same, as the first cluster
	noAgent := setupEnd{ID: "b", Name: "B"}
	noAgentA := setupEnd{ID: "a", Name: "A"}
	tier1 := func(e setupEnd) setupEnd {
		e.Tier, e.Note = 1, &LinkNote{Cluster: e.ID, Code: "tier"}
		return e
	}
	paused := func(e setupEnd) setupEnd {
		e.Note = &LinkNote{Cluster: e.ID, Code: "paused"}
		return e
	}
	silentPeer := func(e setupEnd) setupEnd {
		e.Note = &LinkNote{Cluster: e.ID, Peer: "x", Code: "peer-silent", Text: "The agent of x has not reported any node address yet."}
		return e
	}
	ok := caps()
	for _, tc := range []struct {
		name   string
		l      kernel.LinkSetting
		a, b   setupEnd
		fusion bool
		want   string
	}{
		{"everything in place", plain, endOf("a", ok), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=ok peer-export/b=ok"},
		{"the runner has no telemetry sent onward", plain, endOf("a", caps(without("export"))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=todo prober/a=ok peer-export/b=ok"},
		{"the other cluster sending telemetry is optional", plain, endOf("a", ok), endOf("b", caps(without("export"))), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=ok peer-export/b=optional"},
		{"the runner is the one whose id sorts first, whichever side it is listed on", plain, endOf("b", ok), endOf("a", caps(without("export"))), true,
			"agent/b=ok agent/a=ok links-on/b=ok links-on/a=ok export/a=todo prober/a=ok peer-export/b=ok"},
		{"no FUSION on this server is said once, as unknown", plain, endOf("a", ok), endOf("b", ok), false,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=unknown prober/a=ok"},
		{"no FUSION is not a failure even when the runner sends nothing", plain, endOf("a", caps(without("export"))), endOf("b", ok), false,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=unknown prober/a=ok"},
		{"Cluster links off in one install", plain, endOf("a", ok), endOf("b", caps(without("links"))), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=todo export/a=ok prober/a=ok peer-export/b=ok"},
		{"probing off in the runner", plain, endOf("a", caps(without("probe"))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=todo links-on/b=ok export/a=ok peer-export/b=ok"},
		{"probing off does not matter for a pair that is passive only", passive, endOf("a", caps(without("probe"))), endOf("b", caps(without("probe"))), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok export/b=ok"},
		{"the prober never asked", plain, endOf("a", caps(without("prober"))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=todo peer-export/b=ok"},
		{"the prober asked nine minutes ago", plain, endOf("a", caps(prober(9*time.Minute))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=ok peer-export/b=ok"},
		{"the prober asked eleven minutes ago", plain, endOf("a", caps(prober(11*time.Minute))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=todo peer-export/b=ok"},
		{"throughput needs iperf3 in both clusters", tput, endOf("a", caps(with("capacity"))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=ok capacity/a=ok capacity/b=todo peer-export/b=ok"},
		{"throughput with iperf3 in both", tput, endOf("a", caps(with("capacity"))), endOf("b", caps(with("capacity"))), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=ok capacity/a=ok capacity/b=ok peer-export/b=ok"},
		{"iperf3 is not asked about when the pair wants no throughput tests", plain, endOf("a", ok), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=ok peer-export/b=ok"},
		{"throughput tests on a pair without probing still need the prober and iperf3", kernel.LinkSetting{A: "a", B: "b", NoProbe: true, Throughput: &kernel.LinkThroughput{}}, endOf("a", caps(with("capacity"))), endOf("b", caps(with("capacity"))), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok prober/a=ok capacity/a=ok capacity/b=ok peer-export/b=ok"},

		// agents too old to say what their install can do: unknown, never todo
		{"an agent that reports nothing", plain, endOf("a", ok), old, true,
			"agent/a=ok agent/b=ok links-on/a=ok capabilities/b=unknown export/a=ok prober/a=ok"},
		{"the runner reports nothing", plain, oldA, endOf("b", ok), true,
			"agent/a=ok agent/b=ok capabilities/a=unknown links-on/b=ok peer-export/b=ok"},
		{"two old agents", tput, endOf("a", agents.LinkCaps{}), endOf("b", agents.LinkCaps{}), true,
			"agent/a=ok agent/b=ok capabilities/a=unknown capabilities/b=unknown"},
		{"two old agents, no FUSION", plain, endOf("a", agents.LinkCaps{}), endOf("b", agents.LinkCaps{}), false,
			"agent/a=ok agent/b=ok capabilities/a=unknown capabilities/b=unknown export/a=unknown"},
		{"a report that is too old is not relied on", plain, endOf("a", caps(reported(20*time.Minute), without("export"))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok capabilities/a=unknown links-on/b=ok peer-export/b=ok"},
		{"a report from fourteen minutes ago still counts", plain, endOf("a", caps(reported(14*time.Minute), without("export"))), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=todo prober/a=ok peer-export/b=ok"},

		// the agents themselves
		{"no approved agent at one end", plain, endOf("a", ok), noAgent, true,
			"agent/a=ok agent/b=todo links-on/a=ok export/a=ok prober/a=ok"},
		{"no approved agent at the runner: the other one probes, so it is the one that has to send telemetry", plain, noAgentA, endOf("b", caps(without("export"))), true,
			"agent/a=todo agent/b=ok links-on/b=ok export/b=todo prober/b=ok"},
		{"access tier 1 at the second cluster", plain, endOf("a", ok), tier1(endOf("b", ok)), true,
			"agent/a=ok agent/b=todo links-on/a=ok links-on/b=ok export/a=ok prober/a=ok peer-export/b=ok"},
		{"access tier 1 at the first cluster: the second probes instead", plain, tier1(endOf("a", ok)), endOf("b", caps(without("export"))), true,
			"agent/a=todo agent/b=ok links-on/a=ok links-on/b=ok export/b=todo prober/b=ok peer-export/a=ok"},
		{"flow collector paused at the first cluster", plain, paused(endOf("a", ok)), endOf("b", ok), true,
			"agent/a=todo agent/b=ok links-on/a=ok links-on/b=ok export/b=ok prober/b=ok peer-export/a=ok"},
		{"the first cluster cannot measure and neither can the second: the first stays the runner", plain, tier1(endOf("a", ok)), tier1(endOf("b", ok)), true,
			"agent/a=todo agent/b=todo links-on/a=ok links-on/b=ok export/a=ok prober/a=ok peer-export/b=ok"},
		{"throughput tests are the first cluster's alone, even when it cannot measure", kernel.LinkSetting{A: "a", B: "b", Throughput: &kernel.LinkThroughput{}}, tier1(endOf("a", caps(with("capacity")))), endOf("b", caps(with("capacity"))), true,
			"agent/a=todo agent/b=ok links-on/a=ok links-on/b=ok export/b=ok prober/b=ok capacity/a=ok capacity/b=ok peer-export/a=ok"},
		{"the other cluster has not reported an address", plain, silentPeer(endOf("a", ok)), endOf("b", ok), true,
			"agent/a=ok agent/b=ok addresses/a=todo links-on/a=ok links-on/b=ok export/b=ok prober/b=ok peer-export/a=ok"},

		// a pair that is passive only: each cluster's own traffic series leave through its own telemetry
		{"passive, both send", passive, endOf("a", ok), endOf("b", ok), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok export/b=ok"},
		{"passive, only one sends: the other is optional", passive, endOf("a", ok), endOf("b", caps(without("export"))), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=ok export/b=optional"},
		{"passive, neither sends", passive, endOf("a", caps(without("export"))), endOf("b", caps(without("export"))), true,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export/a=todo export/b=todo"},
		{"passive, one sends nothing and the other cannot say", passive, endOf("a", caps(without("export"))), endOf("b", agents.LinkCaps{}), true,
			"agent/a=ok agent/b=ok links-on/a=ok capabilities/b=unknown export/a=unknown"},
		{"passive, no FUSION", passive, endOf("a", ok), endOf("b", ok), false,
			"agent/a=ok agent/b=ok links-on/a=ok links-on/b=ok export=unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := linkSetup(tc.l, tc.a, tc.b, tc.fusion, setupNow)
			if g := states(got); g != tc.want {
				t.Fatalf("\n got  %s\n want %s", g, tc.want)
			}
			for _, s := range got {
				if s.Text == "" {
					t.Errorf("%s has no text", s.Code)
				}
				if s.State != stepOK && s.State != stepTodo && s.State != stepUnknown && s.State != stepOptional {
					t.Errorf("%s has state %q", s.Code, s.State)
				}
			}
		})
	}
}

// A step about an agent that said nothing is never a failure, whatever the pair asks for: it is "unknown", and the agent behaves
// exactly as it did before it was asked.
func TestAnAgentThatReportsNothingNeverMakesAStepFail(t *testing.T) {
	for _, l := range []kernel.LinkSetting{{A: "a", B: "b"}, {A: "a", B: "b", NoProbe: true}, {A: "a", B: "b", Throughput: &kernel.LinkThroughput{}}} {
		for _, fusion := range []bool{true, false} {
			for _, tc := range []struct {
				a, b setupEnd
				mute map[string]bool // the clusters that said nothing
			}{
				{endOf("a", agents.LinkCaps{}), endOf("b", agents.LinkCaps{}), map[string]bool{"a": true, "b": true}},
				{endOf("a", agents.LinkCaps{}), endOf("b", caps()), map[string]bool{"a": true}},
				{endOf("a", caps()), endOf("b", agents.LinkCaps{}), map[string]bool{"b": true}},
			} {
				for _, s := range linkSetup(l, tc.a, tc.b, fusion, setupNow) {
					if s.State == stepTodo && tc.mute[s.Cluster] && s.Code != stepAgent {
						t.Errorf("%+v: %s/%s is todo for an agent that reported nothing: %s", l, s.Code, s.Cluster, s.Text)
					}
				}
				// and what nobody has said is not claimed either: a pair-level step needs someone who has
				if tc.mute["a"] && tc.mute["b"] {
					for _, s := range linkSetup(l, tc.a, tc.b, fusion, setupNow) {
						if s.State == stepTodo || s.State == stepOptional {
							t.Errorf("%+v: %s/%s is %s with two mute agents", l, s.Code, s.Cluster, s.State)
						}
					}
				}
			}
		}
	}
}

func TestChecklistSentencesSayWhatIsWrongNamingTheClusterAndWhatToDo(t *testing.T) {
	a := setupEnd{ID: "cl-a", Name: "sunrise-cluster", HasAgent: true, Tier: 2, Caps: caps(without("export"))}
	b := setupEnd{ID: "cl-b", Name: "harbour", HasAgent: true, Tier: 2, Caps: caps(without("links"))}
	steps := linkSetup(kernel.LinkSetting{A: "cl-a", B: "cl-b", Throughput: &kernel.LinkThroughput{}}, a, b, true, setupNow)
	for code, cluster := range map[string]string{stepExport: "cl-a", stepLinksOn: "cl-b", stepCapacity: "cl-a"} {
		s := step(t, steps, code, cluster)
		if s.State != stepTodo {
			t.Errorf("%s/%s = %s", code, cluster, s.State)
		}
	}
	if got := step(t, steps, stepExport, "cl-a").Text; got != "sunrise-cluster runs the probes for this pair but has no telemetry sent to FUSION, so what it measures stays in the cluster. Turn on telemetry with Cluster links there and point it at a regional operator or backend." {
		t.Errorf("export: %s", got)
	}
	if got := step(t, steps, stepLinksOn, "cl-b").Text; !strings.Contains(got, "Cluster links is not switched on in the install of harbour") || !strings.Contains(got, "Turn on Cluster links") {
		t.Errorf("links-on: %s", got)
	}
	if got := step(t, steps, stepCapacity, "cl-a").Text; !strings.Contains(got, "iperf3 is not switched on in sunrise-cluster") || !strings.Contains(got, "runs the throughput tests") {
		t.Errorf("capacity: %s", got)
	}
	old := linkSetup(kernel.LinkSetting{A: "cl-a", B: "cl-b"}, setupEnd{ID: "cl-a", Name: "sunrise-cluster", HasAgent: true, Tier: 2}, b, true, setupNow)
	if got := step(t, old, stepCapabilities, "cl-a").Text; !strings.Contains(got, "The agent of sunrise-cluster is too old to say") {
		t.Errorf("capabilities: %s", got)
	}
	// the prober step says how long it has been
	p := linkSetup(kernel.LinkSetting{A: "cl-a", B: "cl-b"}, setupEnd{ID: "cl-a", Name: "sunrise-cluster", HasAgent: true, Tier: 2, Caps: caps(prober(3 * time.Hour))}, b, true, setupNow)
	if got := step(t, p, stepProber, "cl-a").Text; !strings.Contains(got, "last asked its agent for the clusters to measure 3 hours ago") {
		t.Errorf("prober: %s", got)
	}
	// a cluster without a name reads by its id
	if got := step(t, linkSetup(kernel.LinkSetting{A: "cl-a", B: "cl-b"}, setupEnd{ID: "cl-a"}, b, true, setupNow), stepAgent, "cl-a").Text; !strings.HasPrefix(got, "cl-a has no approved agent") {
		t.Errorf("agent: %s", got)
	}
	// no FUSION: the one sentence, not a failure
	nf := linkSetup(kernel.LinkSetting{A: "cl-a", B: "cl-b"}, a, b, false, setupNow)
	if s := step(t, nf, stepExport, "cl-a"); s.State != stepUnknown || !strings.Contains(s.Text, "This server has no FUSION") {
		t.Errorf("no FUSION: %+v", s)
	}
}

// ---- the capability report ----

func TestTheServerKeepsWhatAnAgentReportedAndNeverReadsSilenceAsOff(t *testing.T) {
	v := newView()
	v.NoteLinkCaps(nil, setupNow, 0)
	if !v.Ext.LinkCaps.Reported.IsZero() {
		t.Fatal("a heartbeat without the report made one up")
	}
	v.NoteLinkCaps(&continuumv1.LinkCapabilities{LinkMetrics: true, ProbeEnabled: true, CapacityEnabled: true, TelemetryExport: true, ProberSeenUnix: setupNow.Add(-time.Minute).Unix()}, setupNow, 0)
	c := v.Ext.LinkCaps
	if !c.LinkMetrics || !c.ProbeEnabled || !c.CapacityEnabled || !c.TelemetryExport || !c.Reported.Equal(setupNow) || !c.ProberSeen.Equal(setupNow.Add(-time.Minute)) {
		t.Fatalf("%+v", c)
	}
	// a later heartbeat from the same agent that carries none (it restarted as an older version) changes nothing; it goes stale instead
	v.NoteLinkCaps(nil, setupNow.Add(time.Minute), 0)
	if !v.Ext.LinkCaps.Reported.Equal(setupNow) {
		t.Fatal("silence replaced the report")
	}
	// an agent that says Cluster links is off is a report too
	v.NoteLinkCaps(&continuumv1.LinkCapabilities{}, setupNow, 0)
	if v.Ext.LinkCaps.Reported.IsZero() || v.Ext.LinkCaps.LinkMetrics {
		t.Fatalf("%+v", v.Ext.LinkCaps)
	}
}

func TestWhatAnAgentSaysAboutTheProberIsBounded(t *testing.T) {
	at := func(unix int64, skewMs int64) agents.LinkCaps {
		v := newView()
		v.NoteLinkCaps(&continuumv1.LinkCapabilities{ProberSeenUnix: unix}, setupNow, skewMs)
		return v.Ext.LinkCaps
	}
	for name, tc := range map[string]struct {
		unix, skew int64
		want       time.Time
	}{
		"never":                       {0, 0, time.Time{}},
		"negative":                    {-5, 0, time.Time{}},
		"the epoch":                   {1, 0, time.Time{}},
		"an absurd value":             {1 << 62, 0, time.Time{}},
		"in the future is now":        {setupNow.Add(time.Hour).Unix(), 0, setupNow},
		"a clock ahead is corrected":  {setupNow.Add(-time.Minute).Add(time.Hour).Unix(), time.Hour.Milliseconds(), setupNow.Add(-time.Minute)},
		"a clock behind is corrected": {setupNow.Add(-time.Minute).Add(-time.Hour).Unix(), -time.Hour.Milliseconds(), setupNow.Add(-time.Minute)},
	} {
		if got := at(tc.unix, tc.skew).ProberSeen; !got.Equal(tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// ---- from the hub's own state ----

func TestTheHubBuildsTheChecklistFromAgentsAndTheirReports(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*view{
		"ag-a": {View: agents.View{Ext: agents.ViewExt{LinkCaps: caps(without("export"))}}, estView: estView{state: nodeState("10.0.0.1")}},
		"ag-b": {estView: estView{state: nodeState("10.1.0.1")}}, // an agent that never reported
	})
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-z"}}
	got := h.linkSetups(links, []store.Agent{a, b}, nil, true, setupNow)
	if len(got) != 2 || got[0].A != "cl-a" || got[0].B != "cl-b" || got[1].B != "cl-z" {
		t.Fatalf("%+v", got)
	}
	if s := states(got[0].Steps); s != "agent/cl-a=ok agent/cl-b=ok links-on/cl-a=ok capabilities/cl-b=unknown export/cl-a=todo prober/cl-a=ok" {
		t.Errorf("a-b: %s", s)
	}
	if s := step(t, got[0].Steps, stepAgent, "cl-a").Text; !strings.Contains(s, "alpha") {
		t.Errorf("the cluster is named by the agent's name: %s", s)
	}
	if s := states(got[1].Steps); !strings.HasPrefix(s, "agent/cl-a=ok agent/cl-z=todo") {
		t.Errorf("a-z: %s", s)
	}
	// the end that is not told about its peer carries the reason
	low := a
	low.AccessTier = 1
	got = h.linkSetups(links[:1], []store.Agent{low, b}, nil, true, setupNow)
	if s := step(t, got[0].Steps, stepAgent, "cl-a"); s.State != stepTodo || !strings.Contains(s.Text, "access tier 1") {
		t.Errorf("%+v", s)
	}
	got = h.linkSetups(links[:1], []store.Agent{a, b}, map[string]bool{"ag-b": true}, true, setupNow)
	if s := step(t, got[0].Steps, stepAgent, "cl-b"); s.State != stepTodo || !strings.Contains(s.Text, "paused") {
		t.Errorf("%+v", s)
	}
}

// ---- over the stream ----

func TestTheHeartbeatCarriesWhatTheInstallCanDo(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	s := r.connect(t, ctx, key, leaf, &continuumv1.Hello{AgentVersion: "9.9", InstalledAccessTier: 2})

	caps := func() agents.LinkCaps {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		if v := r.hub.views[id]; v != nil {
			return v.Ext.LinkCaps
		}
		return agents.LinkCaps{}
	}
	wait := func(what string, ok func(agents.LinkCaps) bool) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			if ok(caps()) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s: %+v", what, caps())
	}
	// an old agent's heartbeat says nothing about links
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if !caps().Reported.IsZero() {
		t.Fatalf("an old agent's heartbeat produced a report: %+v", caps())
	}
	seen := time.Now().Add(-2 * time.Minute).Unix()
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{
		LinkCapabilities: &continuumv1.LinkCapabilities{LinkMetrics: true, ProbeEnabled: true, TelemetryExport: true, ProberSeenUnix: seen}}}}); err != nil {
		t.Fatal(err)
	}
	wait("the report is kept", func(c agents.LinkCaps) bool {
		return c.LinkMetrics && c.ProbeEnabled && c.TelemetryExport && !c.CapacityEnabled
	})
	if got := caps().ProberSeen.Unix(); got != seen {
		t.Errorf("prober seen %d, want %d", got, seen)
	}
}

// ---- the endpoint ----

func TestLatestFiguresCarryTheChecklistWhetherOrNotFusionCanBeRead(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	if r := a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b", Throughput: &kernel.LinkThroughput{IntervalMinutes: 60}}}}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	// no FUSION: the checklist is there, with the one reason
	doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t)
	if doc["reason"] != "no-fusion" {
		t.Fatalf("%v", doc)
	}
	setup, _ := doc["setup"].([]any)
	if len(setup) != 1 {
		t.Fatalf("setup = %v", doc["setup"])
	}
	p := setup[0].(map[string]any)
	if p["a"] != "cl-a" || p["b"] != "cl-b" {
		t.Fatalf("%v", p)
	}
	steps := p["steps"].([]any)
	if len(steps) == 0 {
		t.Fatalf("no steps: %v", p)
	}
	first := steps[0].(map[string]any)
	if first["code"] != "agent" || first["cluster"] != "cl-a" || first["state"] != "todo" || !strings.Contains(first["text"].(string), "cl-a has no approved agent") {
		t.Errorf("first step = %v", first)
	}
	for _, s := range steps {
		m := s.(map[string]any)
		for _, k := range []string{"code", "state", "text"} {
			if _, ok := m[k].(string); !ok {
				t.Errorf("step %v lacks %s", m, k)
			}
		}
	}
	// no pairs: an empty list, not null
	a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7}, withCookie(admin))
	raw := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).Body.Bytes()
	var shape struct {
		Setup []json.RawMessage `json:"setup"`
		Notes []json.RawMessage `json:"notes"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil || shape.Setup == nil || shape.Notes == nil {
		t.Errorf("setup and notes must be [] without pairs: %s", raw)
	}
}
