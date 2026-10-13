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
	host := operatorHost{Reachable: []reachableAddr{{Port: 31317, Kind: "node-port"}}, PodNodes: []string{"a"}}
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
	hints := bare.hints([]AddressCandidate{{Kind: candidateNodePort}})
	if len(hints) != 2 || !strings.Contains(hints[0], "MetalLB") || !strings.Contains(hints[1], "node probe") {
		t.Fatalf("hints: %v", hints)
	}
	// A load balancer address already there needs no suggestion about one, and a node that reports its interfaces none about the probe.
	done := operatorHost{Nodes: []hostNode{{Name: "a", Ready: true, Interfaces: []hostAddr{{Interface: "tailscale0", Kind: "tun", IP: "100.64.3.7"}}}}, Providers: []string{"metallb"}}
	if got := done.hints([]AddressCandidate{{Kind: candidateLoadBalancer}, {Kind: candidateNodePort}}); len(got) != 0 {
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

func TestAnUnexposedServiceIsSaidToBeSoAndWhatToChange(t *testing.T) {
	node := []AddressCandidate{{Kind: candidateNodePort}}
	if h := exposureHint(node, true, true); h != "" {
		t.Fatalf("a node port is on offer: %q", h)
	}
	if h := exposureHint(nil, true, true); !strings.Contains(h, "fusion.central.service.type") || !strings.Contains(h, "only reachable inside") {
		t.Fatalf("central, seen: %q", h)
	}
	if h := exposureHint(nil, false, true); !strings.Contains(h, "fusion.central.service.type") {
		t.Fatalf("central, not seen: %q", h)
	}
	if h := exposureHint(nil, true, false); !strings.Contains(h, "node port or a load balancer") {
		t.Fatalf("regional, seen: %q", h)
	}
	if h := exposureHint([]AddressCandidate{{Kind: candidateRecorded}}, false, false); !strings.Contains(h, "No agent has reported") {
		t.Fatalf("regional, not seen: %q", h)
	}
	// The probe is only worth asking for when there is a node port to dial on the node's addresses.
	if got := (operatorHost{Nodes: []hostNode{{Name: "a", Ready: true}}}).hints(nil); len(got) != 0 {
		t.Fatalf("no node port, so no talk of the probe: %v", got)
	}
}

func TestHintsSayWhyNoMeshAddressIsOffered(t *testing.T) {
	np := []AddressCandidate{{Kind: candidateNodePort}}
	// The probe is on and sees a LAN only: what it saw is listed, so a missing mesh can be told from a missing probe.
	lan := operatorHost{Nodes: []hostNode{{Name: "a", Ready: true, Interfaces: []hostAddr{{Interface: "eth0", IP: "10.0.0.5"}}}}}
	if got := lan.hints(np); len(got) != 1 || !strings.Contains(got[0], "no mesh address") || !strings.Contains(got[0], "a: eth0 10.0.0.5") {
		t.Fatalf("lan only: %v", got)
	}
	mesh := operatorHost{Nodes: []hostNode{{Name: "a", Ready: true, Interfaces: []hostAddr{{Interface: "tailscale0", Kind: "tun", IP: "100.64.3.7"}}}}}
	if got := mesh.hints(np); len(got) != 0 {
		t.Fatalf("a mesh interface is offered, nothing to explain: %v", got)
	}
	// A load balancer address without a node port: the nodes' addresses have nothing to be dialled on.
	if got := lan.hints([]AddressCandidate{{Kind: candidateLoadBalancer}}); len(got) != 1 || !strings.Contains(got[0], "no node port") {
		t.Fatalf("load balancer only: %v", got)
	}
	if !strings.Contains(noHostHint(true), "FUSION's gateway") || !strings.Contains(noHostHint(false), "this operator") {
		t.Fatal("the unseen-host hint names what is missing")
	}
}

func TestWhenThePodsNodeIsUnknownMoreNodesAreListed(t *testing.T) {
	host := operatorHost{Reachable: []reachableAddr{{Port: 31317, Kind: "node-port"}}}
	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"} {
		host.Nodes = append(host.Nodes, hostNode{Name: "n" + ip, Ready: true, Internal: []string{ip}})
	}
	if got := addressCandidates(host, ""); len(got) != 5 {
		t.Fatalf("pod node unknown: %d listed", len(got))
	}
	host.PodNodes = []string{"n10.0.0.5"}
	got := addressCandidates(host, "")
	if len(got) != maxNodePortNodes || got[0].Node != "n10.0.0.5" {
		t.Fatalf("pod node known: the pod's node first, and no more than %d: %v", maxNodePortNodes, got)
	}
}

func TestAnInterfacesIPv4AddressComesBeforeItsIPv6Ones(t *testing.T) {
	host := operatorHost{
		Reachable: []reachableAddr{{Port: 30338, Kind: "node-port"}},
		Nodes: []hostNode{{Name: "a", Ready: true, Interfaces: []hostAddr{
			{Interface: "eno1", IP: "2001:db8:0:d::1"}, {Interface: "eno1", IP: "2001:db8:0:d::2"}, {Interface: "eno1", IP: "198.51.100.9"},
		}}},
	}
	var got []string
	for _, c := range addressCandidates(host, "") {
		got = append(got, c.Address)
	}
	if want := []string{"198.51.100.9:30338", "[2001:db8:0:d::1]:30338", "[2001:db8:0:d::2]:30338"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTheServicePortIsOfferedOnANodeWhenTheLoadBalancerIsTheNodeItself(t *testing.T) {
	node := hostNode{Name: "hexa", Ready: true, Internal: []string{"147.102.13.108"}, Interfaces: []hostAddr{
		{Interface: "eno1", IP: "147.102.13.108"}, {Interface: "wt0", Kind: "wireguard", IP: "100.64.0.139"},
	}}
	lb := func(ip string) operatorHost {
		return operatorHost{Nodes: []hostNode{node}, Reachable: []reachableAddr{{IP: ip, Port: 4317, Kind: "load-balancer"}, {Port: 30338, Kind: "node-port"}}}
	}
	kinds := func(h operatorHost) map[string]string {
		out := map[string]string{}
		for _, c := range addressCandidates(h, "") {
			out[c.Address] = c.Kind
		}
		return out
	}

	// k3s's built-in load balancer gives the Service the node's own address: the node answers on the Service port too, on
	// every address it has, the mesh one included.
	got := kinds(lb("147.102.13.108"))
	want := map[string]string{
		"147.102.13.108:4317":  "load-balancer",
		"147.102.13.108:30338": "node-port",
		"100.64.0.139:30338":   "node-port",
		"100.64.0.139:4317":    "node-service-port",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}

	// A load balancer of its own (MetalLB, a cloud one) is not a node: its port is not open on the nodes, so it is not offered there.
	got = kinds(lb("203.0.113.50"))
	if got["100.64.0.139:4317"] != "" || got["147.102.13.108:4317"] != "" || got["100.64.0.139:30338"] != "node-port" {
		t.Fatalf("got %v", got)
	}

	// A load balancer Service without node ports still has the node's own address on the Service port.
	h := lb("147.102.13.108")
	h.Reachable = h.Reachable[:1]
	got = kinds(h)
	if got["100.64.0.139:4317"] != "node-service-port" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
