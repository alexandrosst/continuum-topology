package server

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

func addrs(list []AddressCandidate) []string {
	out := []string{}
	for _, c := range list {
		out = append(out, c.Kind+" "+c.Address)
	}
	return out
}

func TestAddressCandidatesAreRankedAndDeduplicated(t *testing.T) {
	host := operatorHost{
		Reachable: []reachableAddr{
			{IP: "203.0.113.9", Port: 4317, Kind: "load-balancer"},
			{Port: 31317, Kind: "node-port"},
			{IP: "198.51.100.4", Port: 4317, Kind: "external-ip"},
		},
		PodNodes: []string{"b"},
		Nodes: []hostNode{
			{Name: "a", Ready: true, Internal: []string{"10.20.0.11"}},
			{Name: "b", Ready: true, Internal: []string{"10.20.0.12"}, External: []string{"192.0.2.12"}},
			{Name: "c", Ready: false, Internal: []string{"10.20.0.13"}}, // not ready: never offered
		},
	}
	got := addrs(addressCandidates(host, ""))
	want := []string{
		"load-balancer 203.0.113.9:4317",
		"node-port 10.20.0.12:31317", // the node the pod runs on comes first
		"node-port 10.20.0.11:31317",
		"external-ip 198.51.100.4:4317",
		"node-port 192.0.2.12:31317", // external node addresses after the internal ones
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestAddressCandidatesBracketIPv6AndLimitNodes(t *testing.T) {
	host := operatorHost{Reachable: []reachableAddr{{Port: 31317, Kind: "node-port"}}}
	for i, ip := range []string{"fd00::1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"} {
		host.Nodes = append(host.Nodes, hostNode{Name: string(rune('a' + i)), Ready: true, Internal: []string{ip}})
	}
	got := addrs(addressCandidates(host, ""))
	if len(got) != maxNodePortNodes {
		t.Fatalf("%d node addresses listed, want %d: %v", len(got), maxNodePortNodes, got)
	}
	if got[0] != "node-port [fd00::1]:31317" {
		t.Fatalf("an IPv6 address needs its brackets: %v", got)
	}
}

func TestTheRecordedAddressIsMarkedWhereFoundAndListedLastWhenNot(t *testing.T) {
	host := operatorHost{Reachable: []reachableAddr{{IP: "203.0.113.9", Port: 4317, Kind: "load-balancer"}}}
	list := addressCandidates(host, "203.0.113.9:4317")
	if len(list) != 1 || !list[0].Recorded || list[0].Kind != candidateLoadBalancer {
		t.Fatalf("an address found and recorded is listed once, marked: %+v", list)
	}
	list = addressCandidates(host, "otlp.example.com:4317")
	if len(list) != 2 || list[1].Kind != candidateRecorded || !list[1].Recorded || list[0].Recorded {
		t.Fatalf("a recorded address discovery does not know comes last: %+v", list)
	}
	if list := addressCandidates(operatorHost{}, ""); len(list) != 0 {
		t.Fatalf("nothing is known and nothing recorded: %+v", list)
	}
}

func TestOperatorAddressCandidatesComeFromWhatDiscoveryReportsForItsCluster(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", RoleAdmin)
	_, viewer := a.user(t, "vera", RoleViewer)
	agentID := a.env.approvedAgentID(t, fp)
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
	v.state.Nodes["athens-edge-1"].InternalIps = []string{"10.20.0.14"}
	v.state.Nodes["athens-edge-1"].Probe = &continuumv1.HostProbe{HostAddresses: []*continuumv1.HostAddress{
		{Interface: "eth0", Address: "10.20.0.14/24"}, {Interface: "tailscale0", Kind: "tun", Address: "100.64.3.7/32"},
	}}
	v.state.Workloads["continuum-system/Deployment/"+operatorServiceName(id)] = &continuumv1.WorkloadFacts{
		Key: "continuum-system/Deployment/" + operatorServiceName(id), Namespace: operatorNamespace, Kind: "Deployment", Name: operatorServiceName(id),
		NodeNames: []string{"athens-edge-1"}, Reachable: []*continuumv1.Address{{Port: 31317, Kind: "node-port"}},
	}
	// A workload of the same name in another namespace is somebody else's.
	v.state.Workloads["other/Deployment/"+operatorServiceName(id)] = &continuumv1.WorkloadFacts{
		Key: "other/Deployment/" + operatorServiceName(id), Namespace: "other", Kind: "Deployment", Name: operatorServiceName(id),
		Reachable: []*continuumv1.Address{{Ip: "198.51.100.77", Port: 4317, Kind: "load-balancer"}},
	}
	a.hub().mu.Lock()
	a.hub().views[agentID] = v
	a.hub().mu.Unlock()

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

func TestClassifyNetworkTellsAMeshFromALanFromTheInternet(t *testing.T) {
	for _, c := range []struct{ iface, kind, ip, want string }{
		{"wg0", "wireguard", "10.8.0.3", networkMesh},
		{"tailscale0", "tun", "100.64.3.7", networkMesh},
		{"eth9", "", "100.101.102.103", networkMesh}, // the carrier-grade range meshes hand out, whatever the link is called
		{"nebula1", "", "192.168.100.5", networkMesh},
		{"eth0", "", "10.0.5.12", networkPrivate},
		{"eth0", "", "fd00::5", networkPrivate},
		{"eth0", "", "203.0.113.20", networkPublic},
		{"", "", "not-an-ip", ""},
	} {
		if got := classifyNetwork(c.iface, c.kind, c.ip); got != c.want {
			t.Errorf("%s %s %s: %q, want %q", c.iface, c.kind, c.ip, got, c.want)
		}
	}
}

func TestNodeInterfaceAddressesAreOfferedLanThenMeshThenPublicAndNeverTheClusters(t *testing.T) {
	host := operatorHost{
		Reachable: []reachableAddr{{Port: 31317, Kind: "node-port"}},
		Nodes: []hostNode{{
			Name: "a", Ready: true, Internal: []string{"10.20.0.11"}, External: []string{"203.0.113.20"}, PodCIDRs: []string{"10.244.1.0/24"},
			Interfaces: []hostAddr{
				{Interface: "eth0", IP: "10.20.0.11"}, // the internal address again: once
				{Interface: "ens6", IP: "192.168.7.4"},
				{Interface: "tailscale0", Kind: "tun", IP: "100.64.3.7"},
				{Interface: "eth1", IP: "203.0.113.20"}, // the external address again: once
				{Interface: "eth2", IP: "198.51.100.9"},
				{Interface: "br9", IP: "10.244.1.1"}, // inside the node's pod range: never offered
			},
		}},
	}
	list := addressCandidates(host, "")
	var got []string
	for _, c := range list {
		got = append(got, c.Address+" "+c.Network)
	}
	want := []string{
		"10.20.0.11:31317 private", "192.168.7.4:31317 private", "100.64.3.7:31317 mesh",
		"203.0.113.20:31317 public", "198.51.100.9:31317 public",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if list[2].Interface != "tailscale0" || list[2].Node != "a" {
		t.Fatalf("a mesh address says which interface it is on: %+v", list[2])
	}
}

func TestAddressCandidatesAreBoundedWhateverTheNodesReport(t *testing.T) {
	n := hostNode{Name: "a", Ready: true}
	for i := 1; i <= 40; i++ {
		n.Interfaces = append(n.Interfaces, hostAddr{Interface: "eth0", IP: "10.0.0." + strconv.Itoa(i)})
	}
	if got := addressCandidates(operatorHost{Reachable: []reachableAddr{{Port: 31317, Kind: "node-port"}}, Nodes: []hostNode{n}}, ""); len(got) != maxCandidates {
		t.Fatalf("%d candidates", len(got))
	}
}

func TestHintsSuggestTheNodeProbeAndTheProviderTheClusterAlreadyHas(t *testing.T) {
	bare := operatorHost{Nodes: []hostNode{{Name: "a", Ready: true}}, Providers: []string{"metallb"}}
	hints := bare.hints(nil)
	if len(hints) != 2 || !strings.Contains(hints[0], "MetalLB") || !strings.Contains(hints[1], "node probe") {
		t.Fatalf("hints: %v", hints)
	}
	// A load balancer address already there needs no suggestion about one, and a node that reports its interfaces none about the probe.
	done := operatorHost{Nodes: []hostNode{{Name: "a", Ready: true, Interfaces: []hostAddr{{Interface: "eth0", IP: "10.0.0.5"}}}}, Providers: []string{"metallb"}}
	if got := done.hints([]AddressCandidate{{Kind: candidateLoadBalancer}}); len(got) != 0 {
		t.Fatalf("nothing to suggest: %v", got)
	}
	if got := (operatorHost{}).hints(nil); len(got) != 0 {
		t.Fatalf("no nodes are known, so nothing is claimed about the probe: %v", got)
	}
}

func TestProvidersAreFoundAmongTheClustersWorkloads(t *testing.T) {
	got := providersIn(map[string]*continuumv1.WorkloadFacts{
		"a": {Namespace: "metallb-system", Name: "controller"}, "b": {Namespace: "metallb-system", Name: "speaker"},
		"c": {Namespace: "kube-system", Name: "kube-vip-ds"}, "d": {Namespace: "tailscale", Name: "operator"}, "e": {Namespace: "default", Name: "web"},
	})
	if !reflect.DeepEqual(got, []string{"kube-vip", "metallb", "tailscale"}) {
		t.Fatalf("providers: %v", got)
	}
}
