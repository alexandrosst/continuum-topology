package interpret

import (
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/model"
)

type N = continuumv1.NodeFacts
type W = continuumv1.WorkloadFacts

func node(name string, mod func(n *N)) *N {
	n := &N{Key: name, Name: name, Architecture: "amd64", Ready: true, CpuCapacityMillis: 4000, MemoryCapacityBytes: 16 << 30,
		Labels: map[string]string{}, Annotations: map[string]string{}, InternalIps: []string{"10.0.0.1"}}
	if mod != nil {
		mod(n)
	}
	return n
}

func TestDistributionDetection(t *testing.T) {
	cases := []struct {
		name    string
		version string
		node    *N
		want    string
		conf    string
	}{
		{"k3s by version", "v1.30.5+k3s1", node("a", nil), DistK3s, "high"},
		{"rke2 by version", "v1.29.4+rke2r1", node("a", nil), DistRKE2, "high"},
		{"k0s by version", "v1.29.4+k0s.0", node("a", nil), DistK0s, "high"},
		{"eks by version", "v1.30.2-eks-db838b0", node("a", nil), DistEKS, "high"},
		{"gke by version", "v1.29.6-gke.1038001", node("a", nil), DistGKE, "high"},
		{"k3s by instance-type label", "v1.30.5", node("a", func(n *N) { n.Labels["node.kubernetes.io/instance-type"] = "k3s" }), DistK3s, "high"},
		{"k3s by providerID", "v1.30.5", node("a", func(n *N) { n.ProviderId = "k3s://a" }), DistK3s, "medium"},
		{"microk8s by label", "v1.30.1", node("a", func(n *N) { n.Labels["microk8s.io/cluster"] = "true" }), DistMicroK8s, "high"},
		{"aks by label", "v1.29.5", node("a", func(n *N) { n.Labels["kubernetes.azure.com/cluster"] = "MC_rg" }), DistAKS, "high"},
		{"openshift by label", "v1.28.9+416ecaf", node("a", func(n *N) { n.Labels["node.openshift.io/os_id"] = "rhcos" }), DistOpenShift, "high"},
		{"kind by providerID", "v1.30.0", node("a", func(n *N) { n.ProviderId = "kind://docker/kind/kind-control-plane" }), DistKind, "high"},
		{"talos by os image", "v1.30.0", node("a", func(n *N) { n.OsImage = "Talos (v1.7.4)" }), DistTalos, "high"},
		{"minikube by label", "v1.30.0", node("minikube", func(n *N) { n.Labels["minikube.k8s.io/name"] = "minikube" }), DistMinikube, "high"},
		{"docker desktop", "v1.30.0", node("docker-desktop", nil), DistDocker, "medium"},
		{"kubeadm by annotation", "v1.30.0", node("a", func(n *N) {
			n.Annotations["kubeadm.alpha.kubernetes.io/cri-socket"] = "unix:///run/containerd/containerd.sock"
		}), DistKubeadm, "medium"},
		{"nothing to go on", "v1.30.0", node("a", nil), DistUnknown, "low"},
	}
	for _, c := range cases {
		got, e := detectDistribution(&continuumv1.ClusterFacts{Version: c.version}, []*N{c.node})
		if got != c.want || e.Confidence != c.conf || e.Signal == "" {
			t.Errorf("%s: got %q (%s, %q), want %q (%s)", c.name, got, e.Confidence, e.Signal, c.want, c.conf)
		}
	}
}

func TestNodeKindDetection(t *testing.T) {
	cases := []struct {
		name     string
		n        *N
		kind, hw string
		conf     string
	}{
		{"aws vm", node("a", func(n *N) {
			n.ProviderId = "aws:///eu-west-1a/i-0abc"
			n.Labels["node.kubernetes.io/instance-type"] = "m5.large"
		}), "vm", "", "high"},
		{"aws metal", node("a", func(n *N) {
			n.ProviderId = "aws:///eu-west-1a/i-0abc"
			n.Labels["node.kubernetes.io/instance-type"] = "m5.metal"
		}), "bare-metal", "", "high"},
		{"hetzner dedicated", node("a", func(n *N) { n.ProviderId = "hrobot://12345" }), "bare-metal", "", "high"},
		{"hetzner cloud", node("a", func(n *N) { n.ProviderId = "hcloud://98765" }), "vm", "", "high"},
		{"gce", node("a", func(n *N) { n.ProviderId = "gce://proj/zone/vm" }), "vm", "", "high"},
		{"nfd hypervisor", node("a", func(n *N) { n.Labels["feature.node.kubernetes.io/cpu-cpuid.HYPERVISOR"] = "true" }), "vm", "", "high"},
		{"nfd no hypervisor", node("a", func(n *N) { n.Labels["feature.node.kubernetes.io/cpu-cpuid.HYPERVISOR"] = "false" }), "bare-metal", "", "high"},
		{"raspberry pi", node("a", func(n *N) {
			n.Architecture = "arm64"
			n.KernelVersion = "6.6.31+rpt-rpi-2712"
			n.MemoryCapacityBytes = 8 << 30
			n.ProviderId = "k3s://a"
		}), "edge-device", "Raspberry Pi", "medium"},
		{"jetson", node("a", func(n *N) { n.Architecture = "arm64"; n.KernelVersion = "5.15.136-tegra" }), "edge-device", "NVIDIA Jetson", "medium"},
		{"small arm board, no clue", node("a", func(n *N) { n.Architecture = "arm64"; n.MemoryCapacityBytes = 4 << 30 }), "edge-device", "", "low"},
		{"big arm server on prem", node("a", func(n *N) { n.Architecture = "arm64"; n.MemoryCapacityBytes = 128 << 30 }), "vm", "", "low"},
		{"plain x86 on prem is honestly unsure", node("a", nil), "vm", "", "low"},
	}
	for _, c := range cases {
		r := detectNodeKind(c.n)
		if r.kind != c.kind || r.hardware != c.hw || r.kindEv.Confidence != c.conf {
			t.Errorf("%s: got kind=%s hw=%q conf=%s, want kind=%s hw=%q conf=%s", c.name, r.kind, r.hardware, r.kindEv.Confidence, c.kind, c.hw, c.conf)
		}
		if r.kindEv.Signal == "" {
			t.Errorf("%s: no evidence signal", c.name)
		}
	}
}

func TestApplicationGroupingPrecedence(t *testing.T) {
	cases := []struct {
		name          string
		w             *W
		app, origin   string
		conf, managed string
	}{
		{"explicit wins over everything", &W{Namespace: "n", Labels: map[string]string{"continuum.io/application": "Shop", "app.kubernetes.io/part-of": "other"},
			Annotations: map[string]string{"meta.helm.sh/release-name": "rel"}}, "Shop", OriginExplicit, "high", "helm"},
		{"argo tracking id", &W{Namespace: "n", Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "shop-prod:apps/Deployment:n/cart"}}, "shop-prod", OriginArgo, "high", "argo"},
		{"argo instance label", &W{Namespace: "n", Labels: map[string]string{"argocd.argoproj.io/instance": "shop"}}, "shop", OriginArgo, "high", "argo"},
		{"helm annotation", &W{Namespace: "n", Annotations: map[string]string{"meta.helm.sh/release-name": "shop"}}, "shop", OriginHelm, "high", "helm"},
		{"helm labels only", &W{Namespace: "n", Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "shop"}}, "shop", OriginHelm, "medium", "helm"},
		{"part-of", &W{Namespace: "n", Labels: map[string]string{"app.kubernetes.io/part-of": "shop"}}, "shop", OriginPartOf, "medium", "unknown"},
		{"namespace fallback", &W{Namespace: "payments"}, "payments", OriginNamespace, "low", "unknown"},
		{"flux", &W{Namespace: "n", Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}}, "n", OriginNamespace, "low", "flux"},
		{"argo tracking id without app part falls through", &W{Namespace: "n", Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "garbage"}}, "n", OriginNamespace, "low", "argo"},
	}
	for _, c := range cases {
		r := applicationFor(c.w)
		if r.name != c.app || r.origin != c.origin || r.confidence != c.conf || managedBy(c.w) != c.managed {
			t.Errorf("%s: got %+v managed=%s", c.name, r, managedBy(c.w))
		}
	}
	// Named groupings are shared across clusters, namespace ones are not.
	h := appRef{"Shop", OriginHelm, "high", ""}
	if applicationID("o", h, "cl-a") != applicationID("o", appRef{"shop", OriginArgo, "high", ""}, "cl-b") {
		t.Error("the same application name should be one application across clusters")
	}
	ns := appRef{"default", OriginNamespace, "low", ""}
	if applicationID("o", ns, "cl-a") == applicationID("o", ns, "cl-b") {
		t.Error("namespace groupings must stay per cluster")
	}
}

func k3sFixture() *facts.State {
	s := facts.New()
	s.Cluster = &continuumv1.ClusterFacts{
		Uid: "8f3c2a9e-1111-4222-8333-944455556666", Version: "v1.30.5+k3s1", ApiHost: "10.0.0.5:6443",
		StorageClasses: []string{"local-path"}, ServiceCidr: "10.43.5.0/24",
	}
	s.Nodes["edge-1"] = node("edge-1", func(n *N) {
		n.Labels["node-role.kubernetes.io/control-plane"] = "true"
		n.Labels["node.kubernetes.io/instance-type"] = "k3s"
		n.ProviderId = "k3s://edge-1"
		n.PodCidrs = []string{"10.42.0.0/24"}
		n.Architecture = "arm64"
		n.KernelVersion = "6.6.31+rpt-rpi-2712"
		n.MemoryCapacityBytes = 8 << 30
	})
	s.Nodes["edge-2"] = node("edge-2", func(n *N) {
		n.ProviderId = "k3s://edge-2"
		n.PodCidrs = []string{"10.42.1.0/24"}
		n.Ready = false
	})
	for _, ns := range []string{"kube-system", "default", "shop", "continuum-system"} {
		s.Namespaces[ns] = &continuumv1.NamespaceFacts{Key: ns, Name: ns}
	}
	s.Workloads["kube-system/Deployment/traefik"] = &W{Key: "kube-system/Deployment/traefik", Namespace: "kube-system", Kind: "Deployment", Name: "traefik", Replicas: 1, ReadyReplicas: 1}
	s.Workloads["kube-system/DaemonSet/svclb-traefik"] = &W{Key: "kube-system/DaemonSet/svclb-traefik", Namespace: "kube-system", Kind: "DaemonSet", Name: "svclb-traefik"}
	s.Workloads["shop/Deployment/cart"] = &W{Key: "shop/Deployment/cart", Namespace: "shop", Kind: "Deployment", Name: "cart", Replicas: 2, ReadyReplicas: 1,
		Images: []*continuumv1.ContainerImage{{Image: "ghcr.io/acme/cart:1.4", Digest: "sha256:abc"}}, NodeNames: []string{"edge-1", "ghost-node"},
		Annotations: map[string]string{"meta.helm.sh/release-name": "shop"}, Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
		MemoryRequestBytes: 256 << 20, CpuRequestMillis: 100, Ports: []int32{8080}, Exposure: "ingress", Hosts: []string{"shop.example.com"}}
	s.Workloads["shop/StatefulSet/db"] = &W{Key: "shop/StatefulSet/db", Namespace: "shop", Kind: "StatefulSet", Name: "db", Replicas: 1, ReadyReplicas: 1,
		Annotations: map[string]string{"meta.helm.sh/release-name": "shop"}}
	s.Workloads["default/Deployment/hello"] = &W{Key: "default/Deployment/hello", Namespace: "default", Kind: "Deployment", Name: "hello", Replicas: 0}
	s.Workloads["continuum-system/Deployment/agent"] = &W{Key: "continuum-system/Deployment/agent", Namespace: "continuum-system", Kind: "Deployment", Name: "agent",
		Labels: map[string]string{"app.kubernetes.io/part-of": "continuum"}, Replicas: 1, ReadyReplicas: 1}
	return s
}

func TestInterpretK3sCluster(t *testing.T) {
	in := Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "edge-patras", State: k3sFixture(), Now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	out := Interpret(in)

	cl := out.Clusters[0]
	if cl.Distribution != DistK3s || cl.Provider != "On-prem" || cl.Tier != "edge" || cl.Version != "v1.30.5+k3s1" || cl.Ingress != "Traefik" {
		t.Fatalf("cluster = %+v", cl)
	}
	if cl.PodCIDR != "10.42.0.0/16" {
		t.Errorf("podCidr = %s", cl.PodCIDR)
	}
	if cl.ServiceCIDR != "10.43.0.0/16" {
		t.Errorf("serviceCidr = %s, want the agent's estimate widened to k3s's well-known default", cl.ServiceCIDR)
	}
	if cl.Evidence["serviceCidr"].Signal == "" {
		t.Error("serviceCidr should carry evidence explaining where it came from")
	}
	if cl.Status != "degraded" {
		t.Errorf("one of two nodes down should be degraded, got %s", cl.Status)
	}
	if cl.Evidence["distribution"].Signal == "" || cl.Source != "discovered" || cl.Key != "8f3c2a9e-1111-4222-8333-944455556666" {
		t.Errorf("provenance = %+v", cl.Provenance)
	}
	if len(out.Nodes) != 2 || out.Nodes[0].Role != "control-plane" || out.Nodes[0].HardwareModel != "Raspberry Pi" || out.Nodes[0].Kind != "edge-device" || out.Nodes[1].Status != "offline" {
		t.Errorf("nodes = %+v", out.Nodes)
	}
	if out.Nodes[0].InstanceType != "" {
		t.Error("the k3s pseudo instance type should not be shown as an instance type")
	}
	// kube-system is a system namespace, and continuum-system holds nothing but continuum's own agent here -
	// neither belongs in the topology as if it were one of the user's own namespaces.
	for _, ns := range out.Namespaces {
		if ns.Name == "kube-system" {
			t.Error("kube-system leaked into namespaces")
		}
		if ns.Name == "continuum-system" {
			t.Error("a namespace holding only continuum's own workloads leaked into namespaces")
		}
	}
	if len(out.Namespaces) != 2 {
		t.Errorf("expected only default and shop, got %+v", out.Namespaces)
	}
	var names []string
	for _, s := range out.Services {
		names = append(names, s.Namespace+"/"+s.Name)
	}
	if strings.Join(names, ",") != "default/hello,shop/cart,shop/db" {
		t.Fatalf("services = %v", names)
	}
	var cart model.Service
	for _, s := range out.Services {
		if s.Name == "cart" {
			cart = s
		}
	}
	if cart.Status != "degraded" || cart.Image != "ghcr.io/acme/cart:1.4" || cart.ImageDigest != "sha256:abc" || cart.MemRequestMi != 256 || cart.ManagedBy != "helm" || cart.Exposure != "ingress" {
		t.Errorf("cart = %+v", cart)
	}
	if len(cart.NodeIDs) != 1 || cart.NodeIDs[0] != out.Nodes[0].ID {
		t.Errorf("placement should keep known nodes only: %v", cart.NodeIDs)
	}
	if out.Services[0].Status != "unknown" { // hello, scaled to zero
		t.Errorf("scaled to zero = %s", out.Services[0].Status)
	}

	// Two application suggestions: helm release "shop" (2 services) and namespace "default" (1).
	if len(out.Suggestions) != 2 {
		t.Fatalf("suggestions = %+v", out.Suggestions)
	}
	var shop *model.CreateApplication
	for _, s := range out.Suggestions {
		if a := s.Apply.(model.CreateApplication); a.Application.Name == "shop" {
			shop = &a
		}
	}
	if shop == nil || len(shop.ServiceIDs) != 2 || shop.Application.Origin != OriginHelm || shop.Application.Confidence != "high" {
		t.Fatalf("shop suggestion = %+v", shop)
	}
	if cart.ApplicationHint != shop.Application.ID {
		t.Error("services carry the id of the application they were grouped into")
	}
	for _, s := range out.Suggestions {
		if s.Status != "open" || s.Kind != "application" || s.OrgID != "org" || s.AgentID != "ag-1" {
			t.Errorf("suggestion = %+v", s)
		}
	}
}

func TestInterpretIsDeterministicAndIDsSurviveChanges(t *testing.T) {
	in := Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "e", State: k3sFixture(), Now: time.Now()}
	a, b := Interpret(in), Interpret(in)
	for i := range a.Services {
		if a.Services[i].ID != b.Services[i].ID {
			t.Fatal("ids must be deterministic")
		}
	}
	// A different agent replacing the first one, or a renamed cluster, keeps every id.
	in2 := in
	in2.AgentID, in2.Name = "ag-2", "renamed"
	c := Interpret(in2)
	if a.Nodes[0].ID != c.Nodes[0].ID || a.Services[0].ID != c.Services[0].ID || a.Namespaces[0].ID != c.Namespaces[0].ID {
		t.Fatal("ids must not depend on the agent or the display name")
	}
	// Adding a workload leaves the other ids alone.
	in.State.Workloads["shop/Deployment/new"] = &W{Key: "shop/Deployment/new", Namespace: "shop", Kind: "Deployment", Name: "new", Replicas: 1, ReadyReplicas: 1}
	d := Interpret(in)
	ids := map[string]bool{}
	for _, s := range d.Services {
		ids[s.ID] = true
	}
	for _, s := range a.Services {
		if !ids[s.ID] {
			t.Fatalf("id of %s changed after adding another workload", s.Name)
		}
	}
}

// Off the k3s/RKE2 fast path, a reported Service CIDR passes through as the agent's own best-effort estimate
// (never widened to a guessed default), and no Service CIDR at all leaves the field simply unset.
func TestServiceCIDRPassesThroughOnNonK3sDistributions(t *testing.T) {
	s := facts.New()
	s.Cluster = &continuumv1.ClusterFacts{Uid: "u", Version: "v1.29.3-eks-abc123", ServiceCidr: "172.20.0.0/16"}
	out := Interpret(Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "eks", State: s, Now: time.Now()})
	cl := out.Clusters[0]
	if cl.Distribution != DistEKS {
		t.Fatalf("distribution = %s, want EKS for this test to mean anything", cl.Distribution)
	}
	if cl.ServiceCIDR != "172.20.0.0/16" {
		t.Errorf("serviceCidr = %s, want the agent's own value passed through unchanged", cl.ServiceCIDR)
	}
	if cl.Evidence["serviceCidr"].Confidence != "low" {
		t.Errorf("an unwidened bounding estimate should say so plainly, got confidence %q", cl.Evidence["serviceCidr"].Confidence)
	}

	s2 := facts.New()
	s2.Cluster = &continuumv1.ClusterFacts{Uid: "u"}
	out2 := Interpret(Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-y", Name: "bare", State: s2, Now: time.Now()})
	if got := out2.Clusters[0].ServiceCIDR; got != "" {
		t.Errorf("serviceCidr = %q, want unset when the agent never reported one", got)
	}
}

func TestCommonPodCIDR(t *testing.T) {
	mk := func(c ...string) []*N {
		var out []*N
		for _, x := range c {
			out = append(out, node(x, func(n *N) { n.PodCidrs = []string{x} }))
		}
		return out
	}
	for in, want := range map[string]string{
		"10.42.0.0/24":                "10.42.0.0/24",
		"10.42.0.0/24 10.42.1.0/24":   "10.42.0.0/23",
		"10.42.0.0/24 10.42.200.0/24": "10.42.0.0/16",
		"192.168.0.0/24 10.42.0.0/24": "0.0.0.0/0",
	} {
		if got := commonPodCIDR(mk(strings.Fields(in)...)); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	if commonPodCIDR(nil) != "" {
		t.Error("no nodes, no cidr")
	}
}

func TestAddonsAndAccelerators(t *testing.T) {
	s := facts.New()
	s.Workloads["kube-system/DaemonSet/cilium"] = &W{Name: "cilium"}
	s.Workloads["ingress-nginx/Deployment/ingress-nginx-controller"] = &W{Name: "ingress-nginx-controller"}
	cni, ing := detectAddons(s, nil)
	if cni != "Cilium" || ing != "NGINX" {
		t.Errorf("cni=%s ingress=%s", cni, ing)
	}
	s2 := facts.New()
	cni, _ = detectAddons(s2, []*N{node("a", func(n *N) { n.Annotations["flannel.alpha.coreos.com/backend-type"] = "vxlan" })})
	if cni != "Flannel" {
		t.Errorf("flannel annotation: %s", cni)
	}
	acc := accelerators(node("g", func(n *N) {
		n.ExtendedResources = map[string]int64{"nvidia.com/gpu": 2, "hugepages-2Mi": 4}
		n.Labels["nvidia.com/gpu.product"] = "NVIDIA-A100"
	}))
	if len(acc) != 1 || acc[0].Model != "NVIDIA-A100" || acc[0].Count != 2 {
		t.Errorf("accelerators = %+v", acc)
	}
}

func TestInterpretExtendedFacts(t *testing.T) {
	st := k3sFixture()
	cnt := int32(7)
	first := sortedNodes(st)[0]
	first.PodCount, first.PodCapacity, first.CpuRequestedMillis = &cnt, 110, 500
	out := Interpret(Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "edge", State: st, Now: time.Now()})
	known, unknown := 0, 0
	for _, n := range out.Nodes {
		if n.PodCount != nil {
			known++
			if n.Requested == nil || *n.PodCount != 7 || n.PodCapacity != 110 {
				t.Errorf("node with pods observed = %+v", n)
			}
		} else {
			unknown++
			if n.Requested != nil {
				t.Error("requested must be unknown (nil), not zero, when pods were not read")
			}
		}
	}
	if known != 1 || unknown < 1 {
		t.Errorf("known %d unknown %d", known, unknown)
	}
}
