package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/connectivity"
)

// connectedAgent is an approved agent with a live view and a stream the hub can push a Config on.
func connectedAgent(t *testing.T, a *adminRig) (agentID string, push chan *continuumv1.ServerMessage) {
	t.Helper()
	agentID = a.approvedAgentID(t, fp)
	push = make(chan *continuumv1.ServerMessage, 8)
	a.hub().Mu.Lock()
	a.hub().Views[agentID] = liveView(*a.now, "n1")
	a.hub().Sessions[agentID] = &Session{Push: push, Cancel: func(EndReason) {}}
	a.hub().Mu.Unlock()
	return agentID, push
}

func startTests(t *testing.T, a *adminRig, cookie, agentID string, addrs ...string) (code int, tests []any) {
	t.Helper()
	r := a.do("POST", "/api/v1/agents/"+agentID+"/address-tests", map[string]any{"addresses": addrs}, withCookie(cookie))
	if r.Code == 202 {
		tests, _ = r.json(t)["tests"].([]any)
	}
	return r.Code, tests
}

func readTests(t *testing.T, a *adminRig, cookie, agentID string) []map[string]any {
	t.Helper()
	r := a.do("GET", "/api/v1/agents/"+agentID+"/address-tests", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("list: %d %s", r.Code, r.Body.String())
	}
	var out []map[string]any
	for _, x := range r.json(t)["tests"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestAnAddressIsTestedFromTheAgentAndTheResultReadBack(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	_, viewer := a.user(t, "vera", accounts.RoleViewer)
	agentID, push := connectedAgent(t, a)

	if code, _ := startTests(t, a, viewer, agentID, "10.20.0.14:31317"); code != 403 {
		t.Fatalf("a viewer makes an agent dial an address: %d", code)
	}
	code, started := startTests(t, a, cookie, agentID, "10.20.0.14:31317", "10.20.0.15")
	if code != 202 || len(started) != 2 {
		t.Fatalf("start: %d %v", code, started)
	}
	select {
	case m := <-push:
		cfg := m.GetConfig()
		if cfg == nil || len(cfg.ProbeTargets) != 2 || cfg.MeasureSeconds == 0 {
			t.Fatalf("the agent was not handed the two addresses to time: %v", m)
		}
	default:
		t.Fatal("no new Config was pushed to the agent's stream")
	}
	// Pending until the agent reports; a bare host got the receiver's port.
	got := readTests(t, a, cookie, agentID)
	if len(got) != 2 || got[0]["state"] != "pending" || got[1]["state"] != "pending" {
		t.Fatalf("before the agent measures: %v", got)
	}

	ids := map[string]string{}
	for _, g := range got {
		ids[g["address"].(string)] = g["id"].(string)
	}
	if ids["10.20.0.15:4317"] == "" {
		t.Fatalf("the bare host was not given the receiver's port: %v", ids)
	}
	a.hub().NoteMeasurements(agentID, &continuumv1.Measurements{Results: []*continuumv1.PathResult{
		{TargetId: ids["10.20.0.14:31317"], Samples: 5, Failed: 0, RttMinMs: 3, RttP50Ms: 4, RttP95Ms: 6},
		{TargetId: ids["10.20.0.15:4317"], Samples: 5, Failed: 5},
	}})
	byAddr := map[string]map[string]any{}
	for _, g := range readTests(t, a, cookie, agentID) {
		byAddr[g["address"].(string)] = g
	}
	if g := byAddr["10.20.0.14:31317"]; g["state"] != "reachable" || g["rttP50Ms"] != 4.0 {
		t.Fatalf("reachable: %v", g)
	}
	if g := byAddr["10.20.0.15:4317"]; g["state"] != "unreachable" {
		t.Fatalf("unreachable: %v", g)
	}
	// A test is not a measured path: it must never show up among the paths the topology draws.
	a.hub().Mu.Lock()
	n := len(a.hub().Views[agentID].Paths)
	a.hub().Mu.Unlock()
	if n != 0 {
		t.Fatalf("a test result was added to the measured paths: %d", n)
	}
	// It is audited.
	evs, _ := a.st.ListAudit(a.ctx, "org-1", 50)
	var audited bool
	for _, e := range evs {
		audited = audited || (e.Action == "address-test-started" && e.Actor == "alex")
	}
	if !audited {
		t.Fatalf("no audit row: %+v", evs)
	}
}

func TestAnAddressTestIsHeldToTheSameRulesAsMeasuring(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	agentID, _ := connectedAgent(t, a)
	for _, bad := range []string{"", "localhost:4317", "127.0.0.1:4317", "169.254.169.254:80", "10.0.0.1:6443", "10.0.0.1:10250", "https://x.example.com:4317", "10.0.0.1:99999"} {
		if code, _ := startTests(t, a, cookie, agentID, bad); code != 400 {
			t.Errorf("address %q was accepted: %d", bad, code)
		}
	}
	// At most six at a time, in one call or over several.
	seven := []string{"10.0.0.1:1", "10.0.0.2:1", "10.0.0.3:1", "10.0.0.4:1", "10.0.0.5:1", "10.0.0.6:1", "10.0.0.7:1"}
	if code, _ := startTests(t, a, cookie, agentID, seven...); code != 400 {
		t.Errorf("seven at once: %d", code)
	}
	if code, _ := startTests(t, a, cookie, agentID, seven[:5]...); code != 202 {
		t.Fatalf("five: %d", code)
	}
	if code, _ := startTests(t, a, cookie, agentID, seven[5]); code != 202 {
		t.Fatalf("one more fits: %d", code)
	}
	if code, _ := startTests(t, a, cookie, agentID, "10.0.0.8:1"); code != 409 {
		t.Errorf("a seventh while six are held: %d", code)
	}
	// An address already under test replaces its own earlier test rather than using up another place.
	if code, _ := startTests(t, a, cookie, agentID, seven[0]); code != 202 {
		t.Errorf("testing an address again: %d", code)
	}
}

func TestAnAddressTestNeedsAnAgentThatCanMeasure(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	agentID := a.approvedAgentID(t, fp)
	// No stream: nothing can be asked of it.
	a.hub().Mu.Lock()
	a.hub().Views[agentID] = liveView(*a.now, "n1")
	a.hub().Mu.Unlock()
	if code, _ := startTests(t, a, cookie, agentID, "10.0.0.1:4317"); code != 409 {
		t.Fatalf("an agent that is not connected: %d", code)
	}
	agentID2, _ := connectedAgent2(t, a, agentID)
	if _, err := a.hub().SetConsent(a.ctx, "alex", agentID2, agents.Consent{Paused: []string{"measure"}}); err != nil {
		t.Fatal(err)
	}
	if code, _ := startTests(t, a, cookie, agentID2, "10.0.0.1:4317"); code != 409 {
		t.Fatalf("connection timing paused for the cluster: %d", code)
	}
}

// connectedAgent2 gives an agent that already exists a stream.
func connectedAgent2(t *testing.T, a *adminRig, agentID string) (string, chan *continuumv1.ServerMessage) {
	t.Helper()
	push := make(chan *continuumv1.ServerMessage, 8)
	a.hub().Mu.Lock()
	a.hub().Sessions[agentID] = &Session{Push: push, Cancel: func(EndReason) {}}
	a.hub().Mu.Unlock()
	return agentID, push
}

func TestAnAddressTestExpiresAndLeavesTheAgentsTargets(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	agentID, _ := connectedAgent(t, a)
	if code, _ := startTests(t, a, cookie, agentID, "10.20.0.14:31317"); code != 202 {
		t.Fatalf("start: %d", code)
	}
	ag, _ := a.st.GetAgent(a.ctx, agentID)
	a.hub().Mu.Lock()
	v := a.hub().Views[agentID]
	a.hub().Mu.Unlock()
	if cfg := a.hub().ConfigFor(ag, v); len(cfg.ProbeTargets) != 1 {
		t.Fatalf("targets while the test is live: %v", cfg.ProbeTargets)
	}
	*a.now = a.now.Add(connectivity.AddressTestTTL + time.Minute)
	if got := readTests(t, a, cookie, agentID); len(got) != 0 {
		t.Fatalf("an expired test is still listed: %v", got)
	}
	if cfg := a.hub().ConfigFor(ag, v); len(cfg.ProbeTargets) != 0 || cfg.MeasureSeconds != 0 {
		t.Fatalf("an expired test is still handed to the agent: %v", cfg)
	}
}

func TestTestingAnAddressAgainStartsFromScratch(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	agentID, _ := connectedAgent(t, a)
	startTests(t, a, cookie, agentID, "10.20.0.14:31317")
	first := readTests(t, a, cookie, agentID)[0]
	a.hub().NoteMeasurements(agentID, &continuumv1.Measurements{Results: []*continuumv1.PathResult{{TargetId: first["id"].(string), Samples: 5, Failed: 5}}})
	if g := readTests(t, a, cookie, agentID)[0]; g["state"] != "unreachable" {
		t.Fatalf("first result: %v", g)
	}
	*a.now = a.now.Add(time.Second)
	startTests(t, a, cookie, agentID, "10.20.0.14:31317")
	again := readTests(t, a, cookie, agentID)
	if len(again) != 1 || again[0]["state"] != "pending" || again[0]["id"] == first["id"] {
		t.Fatalf("a second test must not pass for the first one's result: %v", again)
	}
	// A late result for the first test is for a target that is no longer issued, and is ignored.
	a.hub().NoteMeasurements(agentID, &continuumv1.Measurements{Results: []*continuumv1.PathResult{{TargetId: first["id"].(string), Samples: 5, Failed: 0}}})
	if g := readTests(t, a, cookie, agentID)[0]; g["state"] != "pending" {
		t.Fatalf("a stale result counted: %v", g)
	}
}
