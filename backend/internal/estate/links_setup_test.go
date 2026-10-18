package estate

import (
	"context"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

func TestTheServerKeepsWhatAnAgentReportedAndNeverReadsSilenceAsOff(t *testing.T) {
	v := NewView()
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
		v := NewView()
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

func TestTheHubBuildsTheChecklistFromAgentsAndTheirReports(t *testing.T) {
	a, b := ag("ag-a", "cl-a", "alpha", "203.0.113.1", 2), ag("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	h := linkHub(map[string]*View{
		"ag-a": {View: agents.View{Ext: agents.ViewExt{LinkCaps: caps(without("export"))}}, EstView: EstView{State: nodeState("10.0.0.1")}},
		"ag-b": {EstView: EstView{State: nodeState("10.1.0.1")}}, // an agent that never reported
	})
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-z"}}
	got := h.LinkSetups(links, []store.Agent{a, b}, nil, true, setupNow)
	if len(got) != 2 || got[0].A != "cl-a" || got[0].B != "cl-b" || got[1].B != "cl-z" {
		t.Fatalf("%+v", got)
	}
	if s := states(got[0].Steps); s != "agent/cl-a=ok agent/cl-b=ok links-on/cl-a=ok capabilities/cl-b=unknown export/cl-a=todo prober/cl-a=ok" {
		t.Errorf("a-b: %s", s)
	}
	if s := step(t, got[0].Steps, connectivity.StepAgent, "cl-a").Text; !strings.Contains(s, "alpha") {
		t.Errorf("the cluster is named by the agent's name: %s", s)
	}
	if s := states(got[1].Steps); !strings.HasPrefix(s, "agent/cl-a=ok agent/cl-z=todo") {
		t.Errorf("a-z: %s", s)
	}
	// the end that is not told about its peer carries the reason
	low := a
	low.AccessTier = 1
	got = h.LinkSetups(links[:1], []store.Agent{low, b}, nil, true, setupNow)
	if s := step(t, got[0].Steps, connectivity.StepAgent, "cl-a"); s.State != connectivity.StepTodo || !strings.Contains(s.Text, "access tier 1") {
		t.Errorf("%+v", s)
	}
	got = h.LinkSetups(links[:1], []store.Agent{a, b}, map[string]bool{"ag-b": true}, true, setupNow)
	if s := step(t, got[0].Steps, connectivity.StepAgent, "cl-b"); s.State != connectivity.StepTodo || !strings.Contains(s.Text, "paused") {
		t.Errorf("%+v", s)
	}
}

func TestTheHeartbeatCarriesWhatTheInstallCanDo(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	s := r.connect(t, ctx, key, leaf, &continuumv1.Hello{AgentVersion: "9.9", InstalledAccessTier: 2})

	caps := func() agents.LinkCaps {
		r.hub.Mu.Lock()
		defer r.hub.Mu.Unlock()
		if v := r.hub.Views[id]; v != nil {
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
