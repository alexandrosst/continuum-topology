package server

import (
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/fusion"
	"continuum/internal/kernel"
	"continuum/internal/operators"
)

// The central operator is reached the same way a regional one is: a sender can choose the address it dials, and when
// one cannot get through the page says which end is at fault. What differs is where the facts come from. Its health is
// FUSION's own state (it has no heartbeat), and its Service belongs to the server's install, not to continuum-system.

func TestCentralOperatorAddressCandidatesAndSendersComeFromTheGatewayAndFusion(t *testing.T) {
	a := newIntentRig(t)
	f, k := newFusion(t, a.adminRig)
	ik := &inspectingKube{fakeKube: k, svc: kernel.KubeService{Type: "LoadBalancer", Port: 4317, LoadBalancer: "203.0.113.50:4317"}}
	f.Kube = ik
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core.Fusion, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()

	// The server reads the gateway's own Service, so a load balancer's address is offered with no agent in that cluster.
	path := "/api/v1/operators/" + operators.CentralOperatorID + "/address-candidates"
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
	v.State.Nodes["server-node-1"].InternalIps = []string{"10.0.0.7"}
	key := f.Namespace + "/Deployment/" + f.ServiceName()
	v.State.Workloads[key] = &continuumv1.WorkloadFacts{Key: key, Namespace: f.Namespace, Kind: "Deployment", Name: f.ServiceName(), NodeNames: []string{"server-node-1"},
		Reachable: []*continuumv1.Address{{Port: 31317, Kind: "node-port"}}}
	a.hub().Mu.Lock()
	a.hub().Views[agentID] = v
	a.hub().Mu.Unlock()
	list = a.do("GET", path, nil, withCookie(a.cookie)).json(t)["candidates"].([]any)
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.(map[string]any)["address"].(string)] = true
	}
	if !seen["203.0.113.50:4317"] || !seen["10.0.0.7:31317"] {
		t.Fatalf("the load balancer and the node port: %v", list)
	}

	// Who sends to it: judged by FUSION's state, which is running here.
	a.command(t, map[string]any{"agentId": agentID, "name": "to fusion", "signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}}, "destination": map[string]any{"kind": "operator", "targetOperatorId": operators.CentralOperatorID, "address": "10.0.0.7:31317"}})
	s := a.do("GET", "/api/v1/operators/"+operators.CentralOperatorID+"/senders", nil, withCookie(a.cookie))
	if s.Code != 200 {
		t.Fatalf("senders: %d %s", s.Code, s.Body.String())
	}
	doc := s.json(t)
	senders := doc["senders"].([]any)
	if doc["operatorState"] != operators.HealthOnline || len(senders) != 1 {
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
	f.Kube = &inspectingKube{fakeKube: k, svc: kernel.KubeService{Type: "LoadBalancer", Port: 4317, NodePort: 31999, LoadBalancer: "203.0.113.50:4317"}}
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core.Fusion, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()

	// The agent reports nothing about the gateway itself, only that it runs in the server's namespace: that names the
	// cluster, and the Service the server read gives the port.
	agentID := a.agent(t)
	v := liveView(*a.now, "server-node-1")
	v.State.Nodes["server-node-1"].InternalIps = []string{"10.0.0.7"}
	v.State.Nodes["server-node-1"].Probe = &continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
		{Interface: "eth0", Address: "10.0.0.7/24"}, {Interface: "wg0", Kind: "wireguard", Address: "10.8.0.3/24"},
	}}
	key := f.Namespace + "/Deployment/continuum-server"
	v.State.Workloads[key] = &continuumv1.WorkloadFacts{Key: key, Namespace: f.Namespace, Kind: "Deployment", Name: "continuum-server"}
	a.hub().Mu.Lock()
	a.hub().Views[agentID] = v
	a.hub().Mu.Unlock()

	got := a.do("GET", "/api/v1/operators/"+operators.CentralOperatorID+"/address-candidates", nil, withCookie(a.cookie)).json(t)
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
			f.Kube = &inspectingKube{fakeKube: k, svc: kernel.KubeService{Type: "LoadBalancer", Port: 4317, NodePort: 31999, LoadBalancer: tc.lb}}
			a.a.Fusion = f
			if _, err := f.Enable(a.ctx, a.core.Fusion, "alex"); err != nil {
				t.Fatal(err)
			}
			k.allReady()
			agentID := a.agent(t)
			v := liveView(*a.now, "edge-node-1")
			v.State.Nodes["edge-node-1"].InternalIps = []string{"10.0.0.7"}
			v.State.Nodes["edge-node-1"].Probe = &continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
				{Interface: "eth0", Address: "10.0.0.7/24"}, {Interface: "wt0", Kind: "wireguard", Address: "100.64.0.139/24"},
			}}
			// Only something in the agent's own namespace is reported, nothing in the server's.
			v.State.Workloads = map[string]*continuumv1.WorkloadFacts{"continuum-system/Deployment/agent": {Key: "continuum-system/Deployment/agent", Namespace: "continuum-system", Kind: "Deployment", Name: "agent"}}
			a.hub().Mu.Lock()
			a.hub().Views[agentID] = v
			a.hub().Mu.Unlock()

			got := a.do("GET", "/api/v1/operators/"+operators.CentralOperatorID+"/address-candidates", nil, withCookie(a.cookie)).json(t)
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
	f.Kube = &inspectingKube{fakeKube: k, svc: kernel.KubeService{Type: "ClusterIP", Port: 4317}}
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core.Fusion, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()
	got := a.do("GET", "/api/v1/operators/"+operators.CentralOperatorID+"/address-candidates", nil, withCookie(a.cookie)).json(t)
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

// centralRig is a server whose gateway is a LoadBalancer Service, with one agent in the cluster whose kube-system UID is uid.
func centralRig(t *testing.T, uid string) (*intentRig, *fusion.FusionControl, string) {
	t.Helper()
	a := newIntentRig(t)
	f, k := newFusion(t, a.adminRig)
	f.Kube = &inspectingKube{fakeKube: k, svc: kernel.KubeService{Type: "LoadBalancer", Port: 4317, NodePort: 31999, LoadBalancer: "203.0.113.50:4317"}}
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core.Fusion, "alex"); err != nil {
		t.Fatal(err)
	}
	k.allReady()
	agentID := a.approvedAgentID(t, uid)
	v := liveView(*a.now, "nuc-1")
	v.State.Nodes["nuc-1"].InternalIps = []string{"10.0.0.7"}
	v.State.Nodes["nuc-1"].Probe = &continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
		{Interface: "eth0", Address: "10.0.0.7/24"}, {Interface: "wt0", Kind: "wireguard", Address: "100.64.0.139/24"},
	}}
	// Nothing in the server's namespace is reported: only the cluster's identity can say where the gateway is.
	v.State.Workloads = map[string]*continuumv1.WorkloadFacts{}
	a.hub().Mu.Lock()
	a.hub().Views[agentID] = v
	a.hub().Mu.Unlock()
	return a, f, agentID
}

func centralCandidates(t *testing.T, a *intentRig) map[string]any {
	t.Helper()
	return a.do("GET", "/api/v1/operators/"+operators.CentralOperatorID+"/address-candidates", nil, withCookie(a.cookie)).json(t)
}

func serverCluster(t *testing.T, got map[string]any) map[string]any {
	t.Helper()
	sc, _ := got["serverCluster"].(map[string]any)
	if sc == nil {
		t.Fatalf("no serverCluster in %v", got)
	}
	return sc
}

func TestTheServerKnowsItsClusterFromItsInstall(t *testing.T) {
	const uid = "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c"
	a, f, _ := centralRig(t, uid)
	f.ClusterUID = uid
	got := centralCandidates(t, a)
	if sc := serverCluster(t, got); sc["state"] != "known" || sc["source"] != "install" {
		t.Fatalf("server cluster: %v", sc)
	}
	by := map[string]bool{}
	for _, c := range got["candidates"].([]any) {
		by[c.(map[string]any)["address"].(string)] = true
	}
	if !by["100.64.0.139:31999"] || !by["10.0.0.7:31999"] || !by["203.0.113.50:4317"] {
		t.Fatalf("the node's mesh and LAN addresses on the node port, and the load balancer: %v", got["candidates"])
	}
}

func TestAKnownClusterWithoutAnAgentSaysSoInsteadOfGuessing(t *testing.T) {
	a, f, _ := centralRig(t, "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c")
	// The server is installed in another cluster than the one that has an agent; one node of this one even has the load
	// balancer's address, which would match by address, but the identity is known and decides.
	f.ClusterUID = "11111111-2222-4333-8444-555555555555"
	got := centralCandidates(t, a)
	if sc := serverCluster(t, got); sc["state"] != "noAgent" || sc["source"] != "install" {
		t.Fatalf("server cluster: %v", sc)
	}
	for _, c := range got["candidates"].([]any) {
		if c.(map[string]any)["kind"] == "node-port" {
			t.Fatalf("no cluster, so no node addresses: %v", got["candidates"])
		}
	}
	if got["found"] != true { // the load balancer's address is still known, from the Service
		t.Fatalf("found: %v", got)
	}
}

func TestTheClusterIsFoundByWhatAgentsReportWhenTheServerWasNotTold(t *testing.T) {
	a, _, _ := centralRig(t, "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c")
	// No identity from the install and no workload of the namespace reported: no match by address either (the load
	// balancer's address is no node's), so the server asks which cluster it is in, offering the registered ones.
	sc := serverCluster(t, centralCandidates(t, a))
	opts, _ := sc["options"].([]any)
	if sc["state"] != "unknown" || len(opts) != 1 {
		t.Fatalf("server cluster: %v", sc)
	}
}

func TestAPersonCanSayWhichClusterTheServerIsInAndItWins(t *testing.T) {
	const uid = "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c"
	a, f, _ := centralRig(t, uid)
	f.ClusterUID = "11111111-2222-4333-8444-555555555555" // the install names a cluster with no agent
	id, _ := kernel.ClusterIDForUID(a.core.OrgID, uid)

	if r := a.do("PUT", "/api/v1/operators/"+operators.CentralOperatorID+"/server-cluster", map[string]any{"clusterId": "cl-0000000000"}, withCookie(a.cookie)); r.Code != 400 {
		t.Fatalf("a cluster no agent is in: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("PUT", "/api/v1/operators/"+operators.CentralOperatorID+"/server-cluster", map[string]any{"clusterId": id}, withCookie(a.cookie)); r.Code != 200 {
		t.Fatalf("choose: %d %s", r.Code, r.Body.String())
	}
	if sc := serverCluster(t, centralCandidates(t, a)); sc["state"] != "known" || sc["source"] != "chosen" || sc["clusterId"] != id {
		t.Fatalf("after choosing: %v", sc)
	}
	// It survives a save of the settings page, which does not carry it.
	if r := a.do("PUT", "/api/v1/settings", a.core.Settings(), withCookie(a.cookie)); r.Code != 200 {
		t.Fatalf("settings: %d %s", r.Code, r.Body.String())
	}
	if sc := serverCluster(t, centralCandidates(t, a)); sc["source"] != "chosen" || sc["clusterId"] != id {
		t.Fatalf("the choice was lost: %v", sc)
	}
	// Taking it back falls to the install's.
	if r := a.do("PUT", "/api/v1/operators/"+operators.CentralOperatorID+"/server-cluster", map[string]any{"clusterId": ""}, withCookie(a.cookie)); r.Code != 200 {
		t.Fatalf("clear: %d", r.Code)
	}
	if sc := serverCluster(t, centralCandidates(t, a)); sc["state"] != "noAgent" {
		t.Fatalf("after clearing: %v", sc)
	}
	// Another operator has none.
	if r := a.do("PUT", "/api/v1/operators/op-123456/server-cluster", map[string]any{"clusterId": id}, withCookie(a.cookie)); r.Code != 404 {
		t.Fatalf("a regional operator: %d", r.Code)
	}
}
