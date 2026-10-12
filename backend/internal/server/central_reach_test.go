package server

import (
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

// The central operator is reached the same way a regional one is: a sender can choose the address it dials, and when
// one cannot get through the page says which end is at fault. What differs is where the facts come from. Its health is
// FUSION's own state (it has no heartbeat), and its Service belongs to the server's install, not to continuum-system.

func TestDiagnosisOfASenderToTheCentralOperatorReadsFusionsOwnState(t *testing.T) {
	base := diagnosisInput{ClusterName: "patras-edge", Connected: true, Address: "203.0.113.50:4317", AddrSource: "recorded",
		OperatorID: CentralOperatorID, OperatorName: "FUSION", Central: true, Delivery: failing("rpc error: code = Unavailable desc = connection error: i/o timeout")}
	peerOK := []peerOutcome{{Name: "athens-edge", Address: "203.0.113.50:4317", State: "exporting"}}

	for _, c := range []struct {
		name   string
		state  string
		peers  []peerOutcome
		where  string
		phrase string
	}{
		{"off", HealthOff, nil, "operator", "FUSION is off"},
		{"starting", HealthStarting, nil, "operator", "still starting"},
		{"attention", HealthAttention, nil, "operator", "reports a problem"},
		// Another cluster getting through is stronger evidence than FUSION's own word: the path is what is wrong.
		{"off while a peer exports", HealthOff, peerOK, "sender-network", "athens-edge is exporting"},
		{"online, nobody to compare", HealthOnline, nil, "sender-network", "reports itself online"},
	} {
		in := base
		in.OperatorState, in.Peers = c.state, c.peers
		got := diagnoseSender(in)
		if got == nil || got.Where != c.where || !strings.Contains(got.Title+" "+got.Detail, c.phrase) {
			t.Errorf("%s: %+v, want %s mentioning %q", c.name, got, c.where, c.phrase)
		}
	}

	// A certificate refusal does not send anyone to a "Renew certificates" row the central operator does not have.
	in := base
	in.OperatorState = HealthOnline
	in.Delivery = failing("x509: certificate signed by unknown authority")
	got := diagnoseSender(in)
	if got == nil || got.Where != "certificate" || strings.Contains(got.Detail, "Renew certificates") {
		t.Errorf("certificate, central: %+v", got)
	}
}

func TestCentralOperatorAddressCandidatesAndSendersComeFromTheGatewayAndFusion(t *testing.T) {
	a := newIntentRig(t)
	f, k := newFusion(t, a.adminRig)
	ik := &inspectingKube{fakeKube: k, svc: KubeService{Type: "LoadBalancer", Port: 4317, LoadBalancer: "203.0.113.50:4317"}}
	f.Kube = ik
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()

	// The server reads the gateway's own Service, so a load balancer's address is offered with no agent in that cluster.
	path := "/api/v1/operators/" + CentralOperatorID + "/address-candidates"
	r := a.do("GET", path, nil, withCookie(a.cookie))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	got := r.json(t)
	list := got["candidates"].([]any)
	if got["found"] != true || len(list) == 0 || list[0].(map[string]any)["address"] != "203.0.113.50:4317" || list[0].(map[string]any)["kind"] != "load-balancer" {
		t.Fatalf("candidates for the gateway: %v", got)
	}

	// An agent in the server's own cluster that reports the gateway's Deployment adds the node ports of that cluster.
	agentID := a.agent(t)
	v := liveView(*a.now, "server-node-1")
	v.state.Nodes["server-node-1"].InternalIps = []string{"10.0.0.7"}
	key := f.Namespace + "/Deployment/" + f.ServiceName()
	v.state.Workloads[key] = &continuumv1.WorkloadFacts{Key: key, Namespace: f.Namespace, Kind: "Deployment", Name: f.ServiceName(), NodeNames: []string{"server-node-1"},
		Reachable: []*continuumv1.Address{{Port: 31317, Kind: "node-port"}}}
	a.hub().mu.Lock()
	a.hub().views[agentID] = v
	a.hub().mu.Unlock()
	list = a.do("GET", path, nil, withCookie(a.cookie)).json(t)["candidates"].([]any)
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.(map[string]any)["address"].(string)] = true
	}
	if !seen["203.0.113.50:4317"] || !seen["10.0.0.7:31317"] {
		t.Fatalf("the load balancer and the node port: %v", list)
	}

	// Who sends to it: judged by FUSION's state, which is running here.
	a.command(t, map[string]any{"agentId": agentID, "name": "to fusion", "signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}}, "destination": map[string]any{"kind": "operator", "targetOperatorId": CentralOperatorID, "address": "10.0.0.7:31317"}})
	s := a.do("GET", "/api/v1/operators/"+CentralOperatorID+"/senders", nil, withCookie(a.cookie))
	if s.Code != 200 {
		t.Fatalf("senders: %d %s", s.Code, s.Body.String())
	}
	doc := s.json(t)
	senders := doc["senders"].([]any)
	if doc["operatorState"] != HealthOnline || len(senders) != 1 {
		t.Fatalf("senders of the central operator: %v", doc)
	}
	addrs := senders[0].(map[string]any)["addresses"].([]any)
	if addrs[0].(map[string]any)["address"] != "10.0.0.7:31317" || addrs[0].(map[string]any)["source"] != "chosen" {
		t.Fatalf("the address this cluster chose: %v", addrs)
	}
}
