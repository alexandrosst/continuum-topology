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

func TestCentralNodeAddressesNeedOnlyTheClusterAndTheServicesNodePort(t *testing.T) {
	a := newIntentRig(t)
	f, k := newFusion(t, a.adminRig)
	f.Kube = &inspectingKube{fakeKube: k, svc: KubeService{Type: "LoadBalancer", Port: 4317, NodePort: 31999, LoadBalancer: "203.0.113.50:4317"}}
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()

	// The agent reports nothing about the gateway itself, only that it runs in the server's namespace: that names the
	// cluster, and the Service the server read gives the port.
	agentID := a.agent(t)
	v := liveView(*a.now, "server-node-1")
	v.state.Nodes["server-node-1"].InternalIps = []string{"10.0.0.7"}
	v.state.Nodes["server-node-1"].Probe = &continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
		{Interface: "eth0", Address: "10.0.0.7/24"}, {Interface: "wg0", Kind: "wireguard", Address: "10.8.0.3/24"},
	}}
	key := f.Namespace + "/Deployment/continuum-server"
	v.state.Workloads[key] = &continuumv1.WorkloadFacts{Key: key, Namespace: f.Namespace, Kind: "Deployment", Name: "continuum-server"}
	a.hub().mu.Lock()
	a.hub().views[agentID] = v
	a.hub().mu.Unlock()

	got := a.do("GET", "/api/v1/operators/"+CentralOperatorID+"/address-candidates", nil, withCookie(a.cookie)).json(t)
	by := map[string]map[string]any{}
	for _, c := range got["candidates"].([]any) {
		m := c.(map[string]any)
		by[m["address"].(string)] = m
	}
	if m := by["10.8.0.3:31999"]; m == nil || m["network"] != "mesh" || m["interface"] != "wg0" {
		t.Fatalf("the node's mesh address on the Service's node port: %v", got["candidates"])
	}
	if by["10.0.0.7:31999"] == nil || by["203.0.113.50:4317"] == nil {
		t.Fatalf("the LAN address and the load balancer too: %v", got["candidates"])
	}
	// The gateway's own Deployment is not reported, so which node holds its pod is not known, and it says so.
	if h, _ := got["hints"].([]any); len(h) != 1 || !strings.Contains(h[0].(string), "which node runs the pod") {
		t.Fatalf("hints: %v", got["hints"])
	}
}

// k3s's built-in load balancer hands the Service the node's own address, and the agent may report nothing of the server's
// namespace (it is another namespace than its own, or not read): the node that has that address still names the cluster.
func TestCentralClusterIsFoundByTheNodeThatHasTheLoadBalancersAddress(t *testing.T) {
	for _, tc := range []struct {
		name, lb string
		found    bool
	}{{"a node's own address", "10.0.0.7:4317", true}, {"an address no node has", "203.0.113.50:4317", false}} {
		t.Run(tc.name, func(t *testing.T) {
			a := newIntentRig(t)
			f, k := newFusion(t, a.adminRig)
			f.Kube = &inspectingKube{fakeKube: k, svc: KubeService{Type: "LoadBalancer", Port: 4317, NodePort: 31999, LoadBalancer: tc.lb}}
			a.a.Fusion = f
			if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
				t.Fatal(err)
			}
			k.allReady()
			agentID := a.agent(t)
			v := liveView(*a.now, "edge-node-1")
			v.state.Nodes["edge-node-1"].InternalIps = []string{"10.0.0.7"}
			v.state.Nodes["edge-node-1"].Probe = &continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
				{Interface: "eth0", Address: "10.0.0.7/24"}, {Interface: "wt0", Kind: "wireguard", Address: "100.64.0.139/24"},
			}}
			// Only something in the agent's own namespace is reported, nothing in the server's.
			v.state.Workloads = map[string]*continuumv1.WorkloadFacts{"continuum-system/Deployment/agent": {Key: "continuum-system/Deployment/agent", Namespace: "continuum-system", Kind: "Deployment", Name: "agent"}}
			a.hub().mu.Lock()
			a.hub().views[agentID] = v
			a.hub().mu.Unlock()

			got := a.do("GET", "/api/v1/operators/"+CentralOperatorID+"/address-candidates", nil, withCookie(a.cookie)).json(t)
			by := map[string]map[string]any{}
			for _, c := range got["candidates"].([]any) {
				m := c.(map[string]any)
				by[m["address"].(string)] = m
			}
			if tc.found {
				if got["found"] != true || got["hostClusterId"] == "" {
					t.Fatalf("the cluster that has the address: %v", got)
				}
				if m := by["100.64.0.139:31999"]; m == nil || m["network"] != "mesh" || m["interface"] != "wt0" {
					t.Fatalf("the mesh address on the Service's node port: %v", got["candidates"])
				}
				return
			}
			if got["found"] == true && got["hostClusterId"] != nil && got["hostClusterId"] != "" {
				t.Fatalf("an address no node has must not name a cluster: %v", got)
			}
			if by["100.64.0.139:31999"] != nil {
				t.Fatalf("no cluster, so no node addresses: %v", got["candidates"])
			}
		})
	}
}

func TestAnUnexposedGatewayComesWithTheCommandThatExposesIt(t *testing.T) {
	a := newIntentRig(t)
	f, k := newFusion(t, a.adminRig)
	f.Kube = &inspectingKube{fakeKube: k, svc: KubeService{Type: "ClusterIP", Port: 4317}}
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()
	got := a.do("GET", "/api/v1/operators/"+CentralOperatorID+"/address-candidates", nil, withCookie(a.cookie)).json(t)
	e, _ := got["expose"].(map[string]any)
	if e == nil || e["service"] != f.ServiceName() || e["namespace"] != f.Namespace || e["value"] != "fusion.central.service.type" {
		t.Fatalf("expose: %v", got["expose"])
	}
	cmds := e["commands"].(map[string]any)
	if !strings.Contains(cmds["nodePort"].(string), `patch svc `+f.ServiceName()) || !strings.Contains(cmds["nodePort"].(string), `"type":"NodePort"`) || !strings.Contains(cmds["loadBalancer"].(string), `"type":"LoadBalancer"`) {
		t.Fatalf("commands: %v", cmds)
	}
	if h, _ := got["hints"].([]any); len(h) != 0 {
		t.Fatalf("the card replaces the sentence: %v", h)
	}
}
