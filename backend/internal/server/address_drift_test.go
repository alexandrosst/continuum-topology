package server

import (
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/accounts"
	"continuum/internal/operators"
)

// driftRig is an operator with one sender that chose an address, the sender connected and able to time connections.
func driftRig(t *testing.T) (a *adminRig, cookie, opID, agentID string, push chan *continuumv1.ServerMessage) {
	t.Helper()
	a = newAdminRig(t)
	_, cookie = a.user(t, "alex", accounts.RoleAdmin)
	opID, intentID := intentToOperator(t, a, cookie)
	agentID = a.do("GET", "/api/v1/telemetry-intents/"+intentID, nil, withCookie(cookie)).json(t)["agentId"].(string)
	scope := map[string]any{"namespaces": []string{}, "exclude": []string{}, "signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}}}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/scope", scope, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("scope: %d %s", r.Code, r.Body.String())
	}
	dest := map[string]any{"destination": map[string]any{"kind": "operator", "targetOperatorId": opID, "address": "10.20.0.14:31317"}}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/destination", dest, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("destination: %d %s", r.Code, r.Body.String())
	}
	push = make(chan *continuumv1.ServerMessage, 16)
	v := liveView(*a.now, "n1")
	v.connected = true
	a.hub().mu.Lock()
	a.hub().views[agentID] = v
	a.hub().sessions[agentID] = &session{push: push, cancel: func(endReason) {}}
	a.hub().mu.Unlock()
	return a, cookie, opID, agentID, push
}

func quietTests(a *adminRig, agentID string) map[string]string {
	a.hub().mu.Lock()
	defer a.hub().mu.Unlock()
	out := map[string]string{}
	for id, t := range a.hub().views[agentID].addrTests {
		if t.Quiet {
			out[t.Address] = id
		}
	}
	return out
}

func TestTheServerRetestsASendersAddressOnItsOwnAndKeepsTheResult(t *testing.T) {
	a, cookie, opID, agentID, push := driftRig(t)
	h := a.hub()

	h.driftSweep(a.ctx)
	q := quietTests(a, agentID)
	// The chosen address for metrics and the recorded one for the other signals are both dialled by this sender.
	if q["10.20.0.14:31317"] == "" {
		t.Fatalf("the address the sender chose was not tested: %v", q)
	}
	select {
	case m := <-push:
		if cfg := m.GetConfig(); cfg == nil || len(cfg.ProbeTargets) == 0 {
			t.Fatalf("the agent was not handed the address to time: %v", m)
		}
	default:
		t.Fatal("no Config was pushed")
	}
	// Quiet: a person's list of tests does not show it, and nothing is audited.
	if got := readTests(t, a, cookie, agentID); len(got) != 0 {
		t.Fatalf("a background test is listed to a person: %v", got)
	}
	evs, _ := a.st.ListAudit(a.ctx, "org-1", 50)
	for _, e := range evs {
		if e.Action == "address-test-started" {
			t.Fatalf("the server's own check was audited as a person's action: %+v", e)
		}
	}

	// Another sweep right away does not start it again.
	h.driftSweep(a.ctx)
	if again := quietTests(a, agentID); again["10.20.0.14:31317"] != q["10.20.0.14:31317"] {
		t.Fatalf("tested again at once: %v then %v", q, again)
	}

	// The agent reports that nothing answered: the result is kept and the senders list says so.
	h.noteMeasurements(agentID, &continuumv1.Measurements{Results: []*continuumv1.PathResult{{TargetId: q["10.20.0.14:31317"], Samples: 5, Failed: 5}}})
	if r := h.reachOf(agentID, "10.20.0.14:31317", a.h2now(t)); r == nil || r.State != "unreachable" {
		t.Fatalf("reach: %+v", r)
	}
	senders := a.do("GET", "/api/v1/operators/"+opID+"/senders", nil, withCookie(cookie)).json(t)["senders"].([]any)
	s := senders[0].(map[string]any)
	var chosen map[string]any
	for _, x := range s["addresses"].([]any) {
		if m := x.(map[string]any); m["source"] == "chosen" {
			chosen = m
		}
	}
	if r, _ := chosen["reach"].(map[string]any); r == nil || r["state"] != "unreachable" {
		t.Fatalf("the sender's address shows no failed test: %v", chosen)
	}
	if d, _ := s["diagnosis"].(map[string]any); d == nil || d["where"] != "sender-network" || !strings.Contains(d["title"].(string), "10.20.0.14:31317") {
		t.Fatalf("an address that stopped answering is explained before any exporter fails: %v", s["diagnosis"])
	}

	// Once the result is old enough it is tested again, and an old one is no longer shown.
	*a.now = a.now.Add(driftEvery + time.Minute)
	h.driftSweep(a.ctx)
	if q2 := quietTests(a, agentID); q2["10.20.0.14:31317"] == "" || q2["10.20.0.14:31317"] == q["10.20.0.14:31317"] {
		t.Fatalf("an old result was not refreshed: %v", q2)
	}
	*a.now = a.now.Add(2 * reachFresh)
	if r := h.reachOf(agentID, "10.20.0.14:31317", *a.now); r != nil {
		t.Fatalf("a result from hours ago is shown as current: %+v", r)
	}
}

func (a *adminRig) h2now(t *testing.T) time.Time { return *a.now }

func TestDriftTestsGiveWayToAPersonsAndSkipWhatCannotBeTested(t *testing.T) {
	a, cookie, _, agentID, _ := driftRig(t)
	h := a.hub()
	h.driftSweep(a.ctx)
	if len(quietTests(a, agentID)) == 0 {
		t.Fatal("no background test to give way")
	}
	// A person's test is accepted at the full limit: the background ones do not use up the room.
	code, started := startTests(t, a, cookie, agentID, "10.0.0.1:1", "10.0.0.2:1", "10.0.0.3:1", "10.0.0.4:1", "10.0.0.5:1", "10.0.0.6:1")
	if code != 202 || len(started) != 6 {
		t.Fatalf("a person's six tests: %d %v", code, started)
	}
	if len(quietTests(a, agentID)) != 0 {
		t.Fatalf("background tests kept the room: %v", quietTests(a, agentID))
	}

	// A cluster that is not connected is not tested, and nothing is said about it.
	a.hub().mu.Lock()
	delete(a.hub().sessions, agentID)
	a.hub().views[agentID].addrTests = nil
	a.hub().mu.Unlock()
	h.driftSweep(a.ctx)
	if q := quietTests(a, agentID); len(q) != 0 {
		t.Fatalf("a disconnected cluster was asked to test: %v", q)
	}
}

func TestDiagnosisUsesAnAddressTestFromTheSendersOwnCluster(t *testing.T) {
	base := diagnosisInput{ClusterName: "patras-edge", Connected: true, Address: "10.20.0.14:31317", AddrSource: "chosen", OperatorID: "op-1", OperatorState: operators.HealthOnline}
	down := &AddressReachDoc{State: "unreachable", At: "2026-10-12T10:00:00Z"}
	up := &AddressReachDoc{State: "reachable", At: "2026-10-12T10:00:00Z"}

	in := base
	in.Reach, in.Delivery = down, &SenderDeliveryDoc{State: "waiting"}
	if d := diagnoseSender(in); d == nil || d.Where != "sender-network" || !strings.Contains(d.Detail, "2026-10-12T10:00:00Z") {
		t.Fatalf("a failed test with nothing sent yet: %+v", d)
	}
	in.Delivery = nil
	if d := diagnoseSender(in); d == nil || d.Where != "sender-network" {
		t.Fatalf("a failed test with nothing reported: %+v", d)
	}
	// Data is getting through: one failed test is not allowed to contradict that.
	in.Delivery = &SenderDeliveryDoc{State: "exporting"}
	if d := diagnoseSender(in); d != nil {
		t.Fatalf("the sender is exporting: %+v", d)
	}
	// A failure that a test from the same cluster could connect to is said to be possibly passing.
	in.Reach, in.Delivery = up, failing("i/o timeout")
	if d := diagnoseSender(in); d == nil || !strings.Contains(d.Detail, "did connect") {
		t.Fatalf("a timeout with a good test: %+v", d)
	}
}
