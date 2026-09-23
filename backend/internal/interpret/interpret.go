package interpret

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/model"

	"google.golang.org/protobuf/types/known/timestamppb"
)

type Input struct {
	OrgID, AgentID, ClusterID, Name string
	State                           *facts.State
	Now                             time.Time
	AccessTier                      int
	// NodeIDs, when set, gives the record id of each node by its key, as decided by the identity registry
	// (a renamed machine keeps its id; a different machine that reuses a name gets a new one). A node not in the
	// map gets the id it has always had: a hash of the cluster and its key.
	NodeIDs map[string]string
}

// systemNamespace is excluded from the topology: it is machinery, not the user's applications.
// Its workloads are still read for detection.
func systemNamespace(ns string) bool {
	return ns == "kube-system" || ns == "kube-public" || ns == "kube-node-lease"
}

// continuumOwnedNamespaces finds namespaces whose only workloads are continuum's own agent/server -
// installed into a cluster it also happens to be monitoring. Those workloads are already left out of the
// topology (below, by the app.kubernetes.io/part-of=continuum label), but without this the namespace that
// held them would still show up as if it were a namespace of the user's own, just an empty-looking one. This
// works regardless of what the namespace happens to be named (continuum, continuum-system, or anything an
// operator chose at install time), by asking what is actually in it rather than matching a fixed name.
// A namespace the agent never saw any workload in at all is left alone: it may simply be a real, empty
// namespace of the user's, and there is nothing here to tell the two cases apart.
func continuumOwnedNamespaces(st *facts.State) map[string]bool {
	hasOther := map[string]bool{}
	hasAny := map[string]bool{}
	for _, w := range st.Workloads {
		hasAny[w.Namespace] = true
		if w.Labels["app.kubernetes.io/part-of"] != "continuum" {
			hasOther[w.Namespace] = true
		}
	}
	owned := map[string]bool{}
	for ns := range hasAny {
		if !hasOther[ns] {
			owned[ns] = true
		}
	}
	return owned
}

func nodeID(cluster, key string) string { return "nd-" + hash(cluster, key) }

// NodeID is the id a node has when nothing better is known: a hash of the cluster and the node's key (its name).
func NodeID(cluster, key string) string { return nodeID(cluster, key) }
func nsID(cluster, name string) string  { return "ns-" + hash(cluster, name) }
func svcID(cluster, key string) string  { return "sv-" + hash(cluster, key) }

// ServiceID is the id the interpreter gives the workload with this key ("namespace/Kind/name") in a cluster.
func ServiceID(cluster, key string) string { return svcID(cluster, key) }

// Hash is the short stable hash the interpreter uses for ids.
func Hash(parts ...string) string { return hash(parts...) }

// Interpret builds the schema-v3 view of one cluster from its facts. It is a pure function of its
// input, so the same facts always give the same ids.
func Interpret(in Input) model.Topology {
	st := in.State
	stamp := in.Now.UTC().Format(time.RFC3339)
	prov := func(key string, rev uint64) model.Provenance {
		return model.Provenance{OrgID: in.OrgID, Source: "discovered", Key: key, LastSeen: stamp, DetectedAt: stamp, AgentID: in.AgentID, Revision: rev}
	}
	out := model.Topology{}

	nodes := sortedNodes(st)
	dist, distEv := detectDistribution(st.Cluster, nodes)
	provider, provEv := detectProvider(nodes)

	// ---- nodes ----
	nodeIDs := map[string]string{} // node name -> id
	kinds := map[string]string{}
	healthy := 0
	for _, n := range nodes {
		k := detectNodeKind(n)
		kinds[n.Key] = k.kind
		id := nodeID(in.ClusterID, n.Key)
		if given, ok := in.NodeIDs[n.Key]; ok {
			id = given
		}
		nodeIDs[n.Name] = id
		mn := model.Node{
			Provenance: prov(in.ClusterID+"/node/"+n.Key, st.Seq),
			ID:         id, Name: n.Name, ClusterID: in.ClusterID, Role: nodeRole(n), Kind: k.kind,
			OS: n.OsImage, CPU: float64(n.CpuCapacityMillis) / 1000, MemoryGb: round1(memGB(n.MemoryCapacityBytes)),
			Status: nodeStatus(n), Labels: n.Labels, Arch: n.Architecture, Kernel: n.KernelVersion,
			Runtime: n.ContainerRuntime, KubeletVersion: n.KubeletVersion, ProviderID: n.ProviderId,
			InstanceType:  firstNonEmpty(n.Labels["node.kubernetes.io/instance-type"], n.Labels["beta.kubernetes.io/instance-type"]),
			Zone:          firstNonEmpty(n.Labels["topology.kubernetes.io/zone"], n.Labels["failure-domain.beta.kubernetes.io/zone"]),
			HardwareModel: k.hardware, Taints: n.Taints, Conditions: n.ProblemConditions,
			Allocatable: &model.Resources{CPU: float64(n.CpuAllocatableMillis) / 1000, MemoryGb: round1(memGB(n.MemoryAllocatableBytes))},
			CreatedAt:   rfc3339(n.CreatedAt), PodCapacity: n.PodCapacity, PodCount: n.PodCount,
		}
		if n.PodCount != nil { // pods were read, so the requested figures are real (possibly zero)
			mn.Requested = &model.Resources{CPU: float64(n.CpuRequestedMillis) / 1000, MemoryGb: round1(memGB(n.MemoryRequestedBytes))}
		}
		if mn.InstanceType == "k3s" {
			mn.InstanceType = ""
		}
		if len(n.InternalIps) > 0 {
			mn.IP = n.InternalIps[0]
		}
		mn.Evidence = map[string]model.Evidence{"kind": k.kindEv}
		if k.hwEv != nil {
			mn.Evidence["hardwareModel"] = *k.hwEv
		}
		if k.probed {
			mn.Probed, mn.Virtualization, mn.Connectivity, mn.HasBattery = true, k.virt, k.connectivity, k.battery
			if k.virtEv != nil {
				mn.Evidence["virtualization"] = *k.virtEv
			}
			if k.connEv != nil {
				mn.Evidence["connectivity"] = *k.connEv
			}
		}
		mn.Accelerators = accelerators(n)
		if nodeStatus(n) == "healthy" {
			healthy++
		}
		out.Nodes = append(out.Nodes, mn)
	}

	// ---- cluster ----
	tier, tierEv := tierFor(provider, nodes, kinds)
	cl := model.Cluster{
		Provenance: prov(st.ClusterUID(), st.Seq), ID: in.ClusterID, Name: in.Name, Tier: tier,
		Distribution: dist, Provider: provider, Labels: map[string]string{},
		Status: clusterStatus(len(nodes), healthy),
	}
	if st.Cluster != nil {
		cl.Version = st.Cluster.Version
		cl.APIEndpoint = st.Cluster.ApiHost
		cl.StorageClasses = st.Cluster.StorageClasses
		cl.CreatedAt = rfc3339(st.Cluster.CreatedAt)
	}
	cl.Evidence = map[string]model.Evidence{"distribution": distEv, "provider": provEv, "tier": tierEv}
	for _, n := range nodes {
		if r := firstNonEmpty(n.Labels["topology.kubernetes.io/region"], n.Labels["failure-domain.beta.kubernetes.io/region"]); r != "" {
			cl.Region = r
			cl.Evidence["region"] = ev("node label topology.kubernetes.io/region", "high", "")
			break
		}
	}
	if pc := commonPodCIDR(nodes); pc != "" {
		cl.PodCIDR = pc
		if (dist == DistK3s || dist == DistRKE2) && strings.HasPrefix(pc, "10.42.") {
			cl.PodCIDR = "10.42.0.0/16"
			cl.Evidence["podCidr"] = ev("default cluster CIDR of "+dist, "medium", "node pod CIDRs fall inside 10.42.0.0/16")
		} else {
			cl.Evidence["podCidr"] = ev("node podCIDR values", "medium", "smallest range covering the nodes' CIDRs")
		}
	}
	if st.Cluster != nil && st.Cluster.ServiceCidr != "" {
		sc := st.Cluster.ServiceCidr
		cl.ServiceCIDR = sc
		if (dist == DistK3s || dist == DistRKE2) && strings.HasPrefix(sc, "10.43.") {
			cl.ServiceCIDR = "10.43.0.0/16"
			cl.Evidence["serviceCidr"] = ev("default cluster CIDR of "+dist, "medium", "ClusterIPs seen fall inside 10.43.0.0/16")
		} else {
			cl.Evidence["serviceCidr"] = ev("Service ClusterIPs", "low", "smallest range covering the ClusterIPs currently assigned - the cluster's configured range may be wider")
		}
	}
	cl.CNI, cl.Ingress = detectAddons(st, nodes)
	out.Clusters = append(out.Clusters, cl)

	// ---- namespaces ----
	ownNS := continuumOwnedNamespaces(st)
	var nsNames []string
	for _, n := range st.Namespaces {
		if !systemNamespace(n.Name) && !ownNS[n.Name] {
			nsNames = append(nsNames, n.Name)
		}
	}
	sort.Strings(nsNames)
	for _, name := range nsNames {
		n := st.Namespaces[name]
		mn := model.Namespace{
			Provenance: prov(in.ClusterID+"/ns/"+name, st.Seq), ID: nsID(in.ClusterID, name), ClusterID: in.ClusterID, Name: name, Labels: orEmpty(n.Labels),
		}
		mn.Mesh, mn.MeshProxy, mn.MeshOff = facts.NamespaceMesh(n.Labels, n.Annotations)
		if st.Cluster != nil && st.Cluster.Mesh != nil {
			mn.Mtls = st.Cluster.Mesh.NamespaceMtls[name]
		}
		out.Namespaces = append(out.Namespaces, mn)
	}

	// ---- services and application suggestions ----
	type group struct {
		ref appRef
		ids []string
		ns  map[string]bool
	}
	groups := map[string]*group{}
	var wl []*continuumv1.WorkloadFacts
	for _, w := range st.Workloads {
		if systemNamespace(w.Namespace) || w.Labels["app.kubernetes.io/part-of"] == "continuum" {
			continue
		}
		wl = append(wl, w)
	}
	sort.Slice(wl, func(i, j int) bool { return wl[i].Key < wl[j].Key })
	for _, w := range wl {
		id := svcID(in.ClusterID, w.Key)
		ref := applicationFor(w)
		appID := applicationID(in.OrgID, ref, in.ClusterID)
		controlPlane := w.Mesh != nil && w.Mesh.ControlPlane
		s := model.Service{
			Provenance: prov(in.ClusterID+"/"+w.Key, st.Seq), ID: id, Name: w.Name, Namespace: w.Namespace, ClusterID: in.ClusterID,
			Kind: w.Kind, Replicas: w.Replicas, ReadyReplicas: w.ReadyReplicas, Status: serviceStatus(w),
			Labels: orEmpty(w.Labels), Exposure: w.Exposure, Hosts: w.Hosts, Ports: w.Ports, ManagedBy: managedBy(w),
			CPURequestM: w.CpuRequestMillis, MemRequestMi: w.MemoryRequestBytes >> 20, CPULimitM: w.CpuLimitMillis, MemLimitMi: w.MemoryLimitBytes >> 20,
			NodeSelector: w.NodeSelector, Tolerations: w.Tolerations, Restarts: w.Restarts, ApplicationHint: appID,
			NodeIDs: []string{}, CreatedAt: rfc3339(w.CreatedAt),
		}
		for _, vc := range w.VolumeClaims {
			v := model.Volume{Name: vc.Name, StorageClass: vc.StorageClass, SizeGb: math.Round(memGB(vc.RequestedBytes)*100) / 100, AccessModes: vc.AccessModes, Phase: vc.Phase}
			for _, nn := range vc.PinnedNodes {
				if nid, ok := nodeIDs[nn]; ok {
					v.PinnedNodeIDs = append(v.PinnedNodeIDs, nid)
				}
			}
			s.Volumes = append(s.Volumes, v)
		}
		if a := w.Autoscaler; a != nil {
			s.Autoscaler = &model.Autoscaler{Min: a.MinReplicas, Max: a.MaxReplicas, Current: a.CurrentReplicas, Targets: a.Targets}
		}
		if d := w.Disruption; d != nil {
			s.Disruption = &model.Disruption{MinAvailable: d.MinAvailable, MaxUnavailable: d.MaxUnavailable, Allowed: d.DisruptionsAllowed}
		}
		if m := w.Mesh; m != nil {
			s.Mesh = &model.ServiceMesh{Mesh: m.Mesh, Proxy: m.Proxy, Bypass: m.Bypass, ExcludedPorts: m.ExcludedPorts, ControlPlane: m.ControlPlane, Source: m.Source}
		}
		if len(w.Images) > 0 {
			s.Image, s.ImageDigest = w.Images[0].Image, w.Images[0].Digest
		}
		for _, nn := range w.NodeNames {
			if nid, ok := nodeIDs[nn]; ok {
				s.NodeIDs = append(s.NodeIDs, nid)
			}
		}
		s.Evidence = map[string]model.Evidence{"application": ev(ref.signal, ref.confidence, "grouped as "+ref.origin)}
		if controlPlane {
			// The mesh's own control plane is infrastructure, not one of the person's applications: it is not offered for
			// grouping, and the UI draws it as part of its cluster.
			s.ApplicationHint = ""
			out.Services = append(out.Services, s)
			continue
		}
		out.Services = append(out.Services, s)

		g := groups[appID]
		if g == nil {
			g = &group{ref: ref, ns: map[string]bool{}}
			groups[appID] = g
		}
		g.ids = append(g.ids, id)
		g.ns[w.Namespace] = true
	}

	if st.Cluster != nil && st.Cluster.Mesh != nil {
		m := st.Cluster.Mesh
		cm := &model.ClusterMesh{Kind: m.Kind, Mode: m.Mode, Version: m.Version, Mtls: m.Mtls, NamespaceMtls: m.NamespaceMtls, ControlPlane: []string{}, PolicyRead: m.PolicyRead, PolicyNote: m.PolicyNote}
		for _, key := range m.ControlPlane {
			if systemNamespace(namespaceOfKey(key)) {
				continue // system namespaces are not in the topology
			}
			cm.ControlPlane = append(cm.ControlPlane, svcID(in.ClusterID, key))
		}
		out.Clusters[0].Mesh = cm
	}

	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, appID := range ids {
		g := groups[appID]
		name := g.ref.name
		desc := fmt.Sprintf("Grouped from %s in %s", g.ref.signal, in.Name)
		app := model.Application{
			Provenance: prov("app/"+strings.ToLower(name), st.Seq), ID: appID, Name: name, Description: desc, Origin: g.ref.origin, Confidence: g.ref.confidence,
		}
		app.Evidence = map[string]model.Evidence{"grouping": ev(g.ref.signal, g.ref.confidence, "")}
		out.Suggestions = append(out.Suggestions, model.Suggestion{
			ID: "sg-app-" + appID + "-" + in.ClusterID, OrgID: in.OrgID, Kind: "application", AgentID: in.AgentID, CreatedAt: stamp, Status: "open",
			Title:  fmt.Sprintf("Group %d %s in %s as “%s”", len(g.ids), plural(len(g.ids), "service", "services"), in.Name, name),
			Detail: fmt.Sprintf("Detected from %s (%s confidence). Accepting creates the application and puts these services in it.", g.ref.signal, g.ref.confidence),
			Apply:  model.CreateApplication{Type: "create-application", Application: app, ServiceIDs: g.ids},
		})
	}
	return out
}

func namespaceOfKey(key string) string {
	if i := strings.Index(key, "/"); i >= 0 {
		return key[:i]
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func clusterStatus(total, healthy int) string {
	switch {
	case total == 0:
		return "unknown"
	case healthy == total:
		return "healthy"
	case healthy == 0:
		return "offline"
	}
	return "degraded"
}

func serviceStatus(w *continuumv1.WorkloadFacts) string {
	switch {
	case w.Replicas == 0:
		return "unknown"
	case w.ReadyReplicas >= w.Replicas:
		return "healthy"
	case w.ReadyReplicas == 0:
		return "offline"
	}
	return "degraded"
}

func accelerators(n *continuumv1.NodeFacts) []model.Accelerator {
	var out []model.Accelerator
	for res, c := range n.ExtendedResources {
		if c <= 0 {
			continue
		}
		switch res {
		case "nvidia.com/gpu":
			out = append(out, model.Accelerator{Vendor: "NVIDIA", Model: firstNonEmpty(n.Labels["nvidia.com/gpu.product"], "GPU"), Count: c})
		case "amd.com/gpu":
			out = append(out, model.Accelerator{Vendor: "AMD", Model: "GPU", Count: c})
		case "google.com/tpu":
			out = append(out, model.Accelerator{Vendor: "Google", Model: "TPU", Count: c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Vendor < out[j].Vendor })
	return out
}

func sortedNodes(st *facts.State) []*continuumv1.NodeFacts {
	out := make([]*continuumv1.NodeFacts, 0, len(st.Nodes))
	for _, n := range st.Nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// detectAddons names the CNI and ingress controller from what runs in kube-system and friends.
func detectAddons(st *facts.State, nodes []*continuumv1.NodeFacts) (cni, ingress string) {
	names := map[string]bool{}
	for _, w := range st.Workloads {
		names[w.Name] = true
	}
	has := func(n ...string) bool {
		for _, x := range n {
			for name := range names {
				if strings.HasPrefix(name, x) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("cilium"):
		cni = "Cilium"
	case has("calico-node", "calico-typha"):
		cni = "Calico"
	case has("antrea-agent"):
		cni = "Antrea"
	case has("weave-net"):
		cni = "Weave Net"
	case has("kube-flannel"):
		cni = "Flannel"
	default:
		for _, n := range nodes {
			if _, ok := n.Annotations["flannel.alpha.coreos.com/backend-type"]; ok {
				cni = "Flannel"
			}
		}
	}
	switch {
	case has("traefik"):
		ingress = "Traefik"
	case has("ingress-nginx-controller", "nginx-ingress"):
		ingress = "NGINX"
	case has("haproxy-ingress", "kubernetes-ingress"):
		ingress = "HAProxy"
	case has("contour"):
		ingress = "Contour"
	case has("istio-ingressgateway"):
		ingress = "Istio gateway"
	}
	if ingress == "" && st.Cluster != nil && len(st.Cluster.IngressClasses) > 0 {
		ingress = st.Cluster.IngressClasses[0]
	}
	return
}

func rfc3339(t *timestamppb.Timestamp) string {
	if t == nil || (t.Seconds == 0 && t.Nanos == 0) {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}
