package server

import (
	"testing"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/accounts"
	"continuum/internal/operators"
	"continuum/internal/testkit"
)

func TestOperatorAddressCandidatesComeFromWhatDiscoveryReportsForItsCluster(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", accounts.RoleAdmin)
	_, viewer := a.user(t, "vera", accounts.RoleViewer)
	agentID := a.env.approvedAgentID(t, testkit.FP)
	ag, err := a.env.st.GetAgent(a.env.ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	cl := ag.ClusterID
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "site-athens", "sourceClusterIds": []string{cl},
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"}, "exposure": "nodeport"}, withCookie(admin))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	id := r.json(t)["operator"].(map[string]any)["id"].(string)
	path := "/api/v1/operators/" + id + "/address-candidates"

	if r := a.do("GET", path, nil, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer reads node addresses: %d", r.Code)
	}

	// Before any agent has reported the operator's Deployment there is nothing to offer, and it says so.
	r = a.do("GET", path, nil, withCookie(admin))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if got := r.json(t); got["found"] != false || len(got["candidates"].([]any)) != 0 {
		t.Fatalf("before discovery: %v", got)
	}

	v := liveView(*a.now, "athens-edge-1")
	v.State.Nodes["athens-edge-1"].InternalIps = []string{"10.20.0.14"}
	v.State.Nodes["athens-edge-1"].Probe = &continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
		{Interface: "eth0", Address: "10.20.0.14/24"}, {Interface: "tailscale0", Kind: "tun", Address: "100.64.3.7/32"},
	}}
	v.State.Workloads["continuum-system/Deployment/"+operators.OperatorServiceName(id)] = &continuumv1.WorkloadFacts{
		Key: "continuum-system/Deployment/" + operators.OperatorServiceName(id), Namespace: "continuum-system", Kind: "Deployment", Name: operators.OperatorServiceName(id),
		NodeNames: []string{"athens-edge-1"}, Reachable: []*continuumv1.Address{{Port: 31317, Kind: "node-port"}},
	}
	// A workload of the same name in another namespace is somebody else's.
	v.State.Workloads["other/Deployment/"+operators.OperatorServiceName(id)] = &continuumv1.WorkloadFacts{
		Key: "other/Deployment/" + operators.OperatorServiceName(id), Namespace: "other", Kind: "Deployment", Name: operators.OperatorServiceName(id),
		Reachable: []*continuumv1.Address{{Ip: "198.51.100.77", Port: 4317, Kind: "load-balancer"}},
	}
	a.hub().Mu.Lock()
	a.hub().Views[agentID] = v
	a.hub().Mu.Unlock()

	r = a.do("GET", path, nil, withCookie(admin))
	got := r.json(t)
	if got["found"] != true {
		t.Fatalf("the operator's Deployment was reported: %v", got)
	}
	list := got["candidates"].([]any)
	if len(list) != 2 {
		t.Fatalf("candidates: %v", list)
	}
	c := list[0].(map[string]any)
	if c["address"] != "10.20.0.14:31317" || c["kind"] != "node-port" || c["node"] != "athens-edge-1" || c["network"] != "private" {
		t.Fatalf("candidate: %v", c)
	}
	if m := list[1].(map[string]any); m["address"] != "100.64.3.7:31317" || m["network"] != "mesh" || m["interface"] != "tailscale0" {
		t.Fatalf("the node's mesh address, from its probe: %v", m)
	}
	if got["hints"] != nil {
		t.Fatalf("a node that reports its interfaces needs no hint about the probe: %v", got["hints"])
	}

	// A recorded address is part of the list and marked as the one in use.
	if r := a.do("POST", "/api/v1/operators/"+id+"/address", map[string]any{"address": "otlp.example.com:4317"}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("record: %d %s", r.Code, r.Body.String())
	}
	list = a.do("GET", path, nil, withCookie(admin)).json(t)["candidates"].([]any)
	if len(list) != 3 || list[2].(map[string]any)["recorded"] != true || list[2].(map[string]any)["kind"] != "recorded" {
		t.Fatalf("with a recorded address: %v", list)
	}
}
