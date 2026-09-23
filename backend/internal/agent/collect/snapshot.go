package collect

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/types/known/timestamppb"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

var standardResources = map[corev1.ResourceName]bool{
	corev1.ResourceCPU: true, corev1.ResourceMemory: true, corev1.ResourcePods: true, corev1.ResourceEphemeralStorage: true,
}

func each[T any](items []any, fn func(T)) {
	for _, it := range items {
		if v, ok := it.(T); ok {
			fn(v)
		}
	}
}

// Snapshot returns the complete current state as one full Sync (seq is set by the sender).
func (c *Collector) Snapshot() *continuumv1.Sync {
	s := &continuumv1.Sync{Full: true, Modules: c.Modules()}
	s.Cluster = &continuumv1.ClusterFacts{ApiHost: c.apiHost}

	var pods []*corev1.Pod
	observed := c.pods != nil // pods are only read at tier 2; below that "requested" and "pod count" are unknown, not zero
	if observed {
		each(c.pods.List(), func(p *corev1.Pod) { pods = append(pods, p) })
	}

	if c.tier >= 1 {
		requested, counts := requestsByNode(pods)
		each(c.nodes.GetStore().List(), func(n *corev1.Node) {
			nf := nodeFacts(n, requested[n.Name], observed)
			if observed {
				cnt := int32(counts[n.Name])
				nf.PodCount = &cnt
			}
			s.Nodes = append(s.Nodes, nf)
		})
		each(c.sc.GetStore().List(), func(x *storagev1.StorageClass) { s.Cluster.StorageClasses = append(s.Cluster.StorageClasses, x.Name) })
		each(c.ic.GetStore().List(), func(x *networkingv1.IngressClass) {
			s.Cluster.IngressClasses = append(s.Cluster.IngressClasses, x.Name)
		})
		sort.Strings(s.Cluster.StorageClasses)
		sort.Strings(s.Cluster.IngressClasses)
		sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].Key < s.Nodes[j].Key })
	}
	if c.tier >= 2 {
		vis := c.visible()
		// c.ns is nil in namespaced mode (rbac.mode=namespaced): no Role, in any namespace, can grant reading
		// Namespace objects, which are cluster-scoped. Namespace metadata (labels, creation time) is then simply
		// unavailable, not empty; scope.namespaces is still known (it is what the Role was scoped to), so the
		// server learns which namespaces are in scope from Cluster.Scope below even without a NamespaceFacts entry
		// for each.
		if c.ns != nil {
			each(c.ns.GetStore().List(), func(n *corev1.Namespace) {
				if !vis(n.Name) {
					return // outside the scope: not even its name leaves the cluster
				}
				s.Namespaces = append(s.Namespaces, &continuumv1.NamespaceFacts{Key: n.Name, Name: n.Name, Labels: n.Labels, Annotations: n.Annotations})
			})
		}
		s.Cluster.Scope = c.scopeFacts(vis)
		sort.Slice(s.Namespaces, func(i, j int) bool { return s.Namespaces[i].Key < s.Namespaces[j].Key })
		var mesh *continuumv1.MeshFacts
		s.Workloads, mesh = c.workloads(pods)
		s.Cluster.Mesh = mesh
		s.Modules = c.setMeshModule(mesh)
		if c.svcs != nil {
			s.Cluster.ServiceCidr = detectServiceCIDR(c.svcs.List())
		}
	}
	return s
}

func ts(t metav1.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t.Time)
}

func nodeFacts(n *corev1.Node, req corev1.ResourceList, podsObserved bool) *continuumv1.NodeFacts {
	f := &continuumv1.NodeFacts{
		Key: n.Name, Name: n.Name, Uid: string(n.UID),
		MachineId: n.Status.NodeInfo.MachineID, SystemUuid: n.Status.NodeInfo.SystemUUID, ProviderId: n.Spec.ProviderID,
		OsImage: n.Status.NodeInfo.OSImage, KernelVersion: n.Status.NodeInfo.KernelVersion,
		ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion, KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		Architecture:      n.Status.NodeInfo.Architecture,
		CpuCapacityMillis: n.Status.Capacity.Cpu().MilliValue(), MemoryCapacityBytes: n.Status.Capacity.Memory().Value(),
		CpuAllocatableMillis: n.Status.Allocatable.Cpu().MilliValue(), MemoryAllocatableBytes: n.Status.Allocatable.Memory().Value(),
		Labels: n.Labels, Annotations: n.Annotations, PodCidrs: n.Spec.PodCIDRs,
		CreatedAt: ts(n.CreationTimestamp), PodCapacity: int32(n.Status.Capacity.Pods().Value()),
	}
	if podsObserved {
		if q, ok := req[corev1.ResourceCPU]; ok {
			f.CpuRequestedMillis = q.MilliValue()
		}
		if q, ok := req[corev1.ResourceMemory]; ok {
			f.MemoryRequestedBytes = q.Value()
		}
	}
	for _, a := range n.Status.Addresses {
		switch a.Type {
		case corev1.NodeInternalIP:
			f.InternalIps = append(f.InternalIps, a.Address)
		case corev1.NodeExternalIP:
			f.ExternalIps = append(f.ExternalIps, a.Address)
		}
	}
	for _, t := range n.Spec.Taints {
		f.Taints = append(f.Taints, taintString(t))
	}
	sort.Strings(f.Taints)
	for _, cnd := range n.Status.Conditions {
		switch cnd.Type {
		case corev1.NodeReady:
			f.Ready = cnd.Status == corev1.ConditionTrue
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable:
			if cnd.Status == corev1.ConditionTrue {
				f.ProblemConditions = append(f.ProblemConditions, string(cnd.Type))
			}
		}
	}
	sort.Strings(f.ProblemConditions)
	for name, q := range n.Status.Capacity {
		if standardResources[name] || strings.HasPrefix(string(name), "hugepages-") || strings.HasPrefix(string(name), "attachable-volumes-") {
			continue
		}
		if f.ExtendedResources == nil {
			f.ExtendedResources = map[string]int64{}
		}
		f.ExtendedResources[string(name)] = q.Value()
	}
	return f
}

func taintString(t corev1.Taint) string {
	if t.Value != "" {
		return t.Key + "=" + t.Value + ":" + string(t.Effect)
	}
	return t.Key + ":" + string(t.Effect)
}

// requestsByNode sums container requests of pods that still hold resources on a node, and counts them.
func requestsByNode(pods []*corev1.Pod) (map[string]corev1.ResourceList, map[string]int) {
	out := map[string]corev1.ResourceList{}
	counts := map[string]int{}
	for _, p := range pods {
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		counts[p.Spec.NodeName]++
		rl := out[p.Spec.NodeName]
		if rl == nil {
			rl = corev1.ResourceList{}
			out[p.Spec.NodeName] = rl
		}
		for _, ct := range p.Spec.Containers {
			for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
				if q, ok := ct.Resources.Requests[r]; ok {
					cur := rl[r]
					cur.Add(q)
					rl[r] = cur
				}
			}
		}
	}
	return out, counts
}

type wl struct {
	facts    *continuumv1.WorkloadFacts
	selector labels.Labels // pod-template labels, used only to join Services
	// The pod template's labels and allow-listed annotations, read for service-mesh markers only.
	tmplLabels, tmplAnn map[string]string
	pods                []*corev1.Pod
}

func (c *Collector) workloads(pods []*corev1.Pod) ([]*continuumv1.WorkloadFacts, *continuumv1.MeshFacts) {
	all := map[string]*wl{} // "ns/Kind/name"
	vis := c.visible()
	add := func(kind, ns, name string, created metav1.Time, meta map[string]string, ann map[string]string, tmpl corev1.PodTemplateSpec, replicas, ready int32) {
		if !vis(ns) {
			return
		}
		f := &continuumv1.WorkloadFacts{
			CreatedAt: ts(created),
			Key:       ns + "/" + kind + "/" + name, Namespace: ns, Kind: kind, Name: name, Replicas: replicas, ReadyReplicas: ready,
			Labels: meta, Annotations: ann, NodeSelector: tmpl.Spec.NodeSelector, Exposure: "internal",
		}
		var cpu, mem, lcpu, lmem int64
		for _, ct := range tmpl.Spec.Containers {
			f.Images = append(f.Images, &continuumv1.ContainerImage{Image: sanitizeImage(ct.Image)})
			cpu += ct.Resources.Requests.Cpu().MilliValue()
			mem += ct.Resources.Requests.Memory().Value()
			lcpu += ct.Resources.Limits.Cpu().MilliValue()
			lmem += ct.Resources.Limits.Memory().Value()
			for _, p := range ct.Ports {
				f.Ports = append(f.Ports, p.ContainerPort)
			}
		}
		f.CpuRequestMillis, f.MemoryRequestBytes, f.CpuLimitMillis, f.MemoryLimitBytes = cpu, mem, lcpu, lmem
		for _, t := range tmpl.Spec.Tolerations {
			f.Tolerations = append(f.Tolerations, tolerationString(t))
		}
		all[f.Key] = &wl{facts: f, selector: labels.Set(tmpl.Labels), tmplLabels: tmpl.Labels, tmplAnn: tmpl.Annotations}
	}
	if c.deploys != nil {
		each(c.deploys.List(), func(d *appsv1.Deployment) {
			add("Deployment", d.Namespace, d.Name, d.CreationTimestamp, d.Labels, d.Annotations, d.Spec.Template, ptrOr(d.Spec.Replicas, 1), d.Status.ReadyReplicas)
		})
		each(c.sts.List(), func(d *appsv1.StatefulSet) {
			add("StatefulSet", d.Namespace, d.Name, d.CreationTimestamp, d.Labels, d.Annotations, d.Spec.Template, ptrOr(d.Spec.Replicas, 1), d.Status.ReadyReplicas)
		})
		each(c.ds.List(), func(d *appsv1.DaemonSet) {
			add("DaemonSet", d.Namespace, d.Name, d.CreationTimestamp, d.Labels, d.Annotations, d.Spec.Template, d.Status.DesiredNumberScheduled, d.Status.NumberReady)
		})
	}

	// pod -> workload through owner references (ReplicaSet -> Deployment)
	rsOwner := map[string]string{} // ns/rsname -> deployment name
	each(c.rs.List(), func(r *appsv1.ReplicaSet) {
		for _, o := range r.OwnerReferences {
			if o.Kind == "Deployment" {
				rsOwner[r.Namespace+"/"+r.Name] = o.Name
			}
		}
	})
	for _, p := range pods {
		for _, o := range p.OwnerReferences {
			var key string
			switch o.Kind {
			case "ReplicaSet":
				if d, ok := rsOwner[p.Namespace+"/"+o.Name]; ok {
					key = p.Namespace + "/Deployment/" + d
				}
			case "StatefulSet", "DaemonSet":
				key = p.Namespace + "/" + o.Kind + "/" + o.Name
			}
			if w := all[key]; w != nil {
				w.pods = append(w.pods, p)
			}
		}
	}
	for _, w := range all {
		nodes := map[string]bool{}
		for _, p := range w.pods {
			if p.Spec.NodeName != "" {
				nodes[p.Spec.NodeName] = true
			}
			for i, cs := range p.Status.ContainerStatuses {
				w.facts.Restarts += cs.RestartCount
				if i == 0 && len(w.facts.Images) > 0 && w.facts.Images[0].Digest == "" {
					w.facts.Images[0].Digest = digestOf(cs.ImageID)
				}
			}
		}
		for n := range nodes {
			w.facts.NodeNames = append(w.facts.NodeNames, n)
		}
		sort.Strings(w.facts.NodeNames)
	}
	c.joinServices(all)
	c.joinStorage(all)
	c.joinScaling(all)
	mesh := c.detectMesh(all, vis)

	out := make([]*continuumv1.WorkloadFacts, 0, len(all))
	for _, w := range all {
		sort.Slice(w.facts.Ports, func(i, j int) bool { return w.facts.Ports[i] < w.facts.Ports[j] })
		out = append(out, w.facts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, mesh
}

// setMeshModule records whether the mesh module is worth showing (only when a mesh was found) and returns the full
// module list for this snapshot.
func (c *Collector) setMeshModule(m *continuumv1.MeshFacts) []*continuumv1.ModuleStatus {
	var st *continuumv1.ModuleStatus
	if m != nil {
		st = &continuumv1.ModuleStatus{Name: ModMesh, State: continuumv1.ModuleStatus_OK}
		if !m.PolicyRead && m.PolicyNote != "" {
			st.State, st.Reason = continuumv1.ModuleStatus_SKIPPED, "policy objects not read: "+m.PolicyNote
		}
	}
	c.mu.Lock()
	c.meshMod = st
	c.mu.Unlock()
	return c.Modules()
}

// detectServiceCIDR guesses the cluster's Service CIDR from the smallest IPv4 range covering every
// ClusterIP currently assigned - the same "smallest bounding prefix" trick commonPodCIDR uses server-side
// for node pod CIDRs, but computed here from individual addresses rather than published per-node ranges,
// and sent up as a single already-derived value: like joinServices, the server never sees a raw ClusterIP.
func detectServiceCIDR(svcs []any) string {
	var addrs []netip.Addr
	each(svcs, func(s *corev1.Service) {
		ips := s.Spec.ClusterIPs
		if len(ips) == 0 && s.Spec.ClusterIP != "" {
			ips = []string{s.Spec.ClusterIP}
		}
		for _, raw := range ips {
			if raw == "" || raw == corev1.ClusterIPNone {
				continue
			}
			if a, err := netip.ParseAddr(raw); err == nil && a.Is4() {
				addrs = append(addrs, a)
			}
		}
	})
	if len(addrs) == 0 {
		return ""
	}
	bits := 32
	first := addrs[0]
	for _, a := range addrs[1:] {
		if b := commonAddrBits(first, a); b < bits {
			bits = b
		}
	}
	p, err := first.Prefix(bits)
	if err != nil {
		return ""
	}
	return p.Masked().String()
}

// commonAddrBits is how many leading bits two IPv4 addresses share.
func commonAddrBits(a, b netip.Addr) int {
	x, y := a.As4(), b.As4()
	n := 0
	for i := 0; i < 4; i++ {
		d := x[i] ^ y[i]
		if d == 0 {
			n += 8
			continue
		}
		for m := byte(0x80); m != 0 && d&m == 0; m >>= 1 {
			n++
		}
		break
	}
	return n
}

var exposureRank = map[string]int{"internal": 0, "node-port": 1, "load-balancer": 2, "ingress": 3}

// joinServices attaches Service ports/exposure and Ingress hosts to the workloads they select.
// The join happens here so the server receives conclusions, never raw Service or Ingress objects.
func (c *Collector) joinServices(all map[string]*wl) {
	if c.svcs == nil {
		return
	}
	svcWorkloads := map[string][]*wl{} // ns/svc -> workloads it selects
	each(c.svcs.List(), func(s *corev1.Service) {
		if len(s.Spec.Selector) == 0 {
			return
		}
		sel := labels.SelectorFromSet(s.Spec.Selector)
		for _, w := range all {
			if w.facts.Namespace != s.Namespace || !sel.Matches(w.selector) {
				continue
			}
			svcWorkloads[s.Namespace+"/"+s.Name] = append(svcWorkloads[s.Namespace+"/"+s.Name], w)
			exp := "internal"
			switch s.Spec.Type {
			case corev1.ServiceTypeNodePort:
				exp = "node-port"
			case corev1.ServiceTypeLoadBalancer:
				exp = "load-balancer"
			}
			raise(w.facts, exp)
			for _, p := range s.Spec.Ports {
				w.facts.Ports = appendUnique(w.facts.Ports, p.Port)
			}
			w.facts.Reachable = appendReachable(w.facts.Reachable, s)
		}
	})
	if c.ings == nil {
		return
	}
	each(c.ings.List(), func(ing *networkingv1.Ingress) {
		route := func(host string, b *networkingv1.IngressBackend) {
			if b == nil || b.Service == nil {
				return
			}
			for _, w := range svcWorkloads[ing.Namespace+"/"+b.Service.Name] {
				raise(w.facts, "ingress")
				if host != "" && !contains(w.facts.Hosts, host) {
					w.facts.Hosts = append(w.facts.Hosts, host)
					sort.Strings(w.facts.Hosts)
				}
			}
		}
		route("", ing.Spec.DefaultBackend)
		for _, r := range ing.Spec.Rules {
			if r.HTTP == nil {
				continue
			}
			for _, p := range r.HTTP.Paths {
				b := p.Backend
				route(r.Host, &b)
			}
		}
	})
}

func raise(f *continuumv1.WorkloadFacts, exp string) {
	if exposureRank[exp] > exposureRank[f.Exposure] {
		f.Exposure = exp
	}
}

func appendUnique(s []int32, v int32) []int32 {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func ptrOr(p *int32, d int32) int32 {
	if p == nil {
		return d
	}
	return *p
}

func tolerationString(t corev1.Toleration) string {
	if t.Operator == corev1.TolerationOpExists && t.Key == "" {
		return "*"
	}
	s := t.Key
	if t.Operator == corev1.TolerationOpEqual || t.Value != "" {
		s += "=" + t.Value
	}
	if t.Effect != "" {
		s += ":" + string(t.Effect)
	}
	return s
}

// digestOf extracts "sha256:..." from an image id such as "docker.io/library/nginx@sha256:abc".
func digestOf(imageID string) string {
	if i := strings.Index(imageID, "@sha256:"); i >= 0 {
		return imageID[i+1:]
	}
	return ""
}

// sanitizeImage removes credentials that someone embedded in an image reference
// ("user:token@registry.example.com/app:1") and any URL scheme, then bounds the length.
// A digest reference ("app@sha256:...") is left alone: its "@" comes after the first "/" or
// there is no "/" at all.
func sanitizeImage(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if slash := strings.Index(s, "/"); slash > 0 {
		if at := strings.LastIndex(s[:slash], "@"); at >= 0 {
			s = s[at+1:]
		}
	}
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// joinStorage attaches the persistent volume claims a workload's pods mount, with size, class and, for
// node-local volumes, the nodes the data is stuck on. Only present when the storage module is on.
func (c *Collector) joinStorage(all map[string]*wl) {
	if c.pvcs == nil {
		return
	}
	pinned := map[string][]string{} // pv name -> nodes
	if c.pvs != nil {
		each(c.pvs.GetStore().List(), func(v *corev1.PersistentVolume) { pinned[v.Name] = pvNodes(v) })
	}
	claims := map[string]*corev1.PersistentVolumeClaim{}
	each(c.pvcs.List(), func(p *corev1.PersistentVolumeClaim) { claims[p.Namespace+"/"+p.Name] = p })
	for _, w := range all {
		seen := map[string]bool{}
		for _, p := range w.pods {
			for _, v := range p.Spec.Volumes {
				if v.PersistentVolumeClaim == nil || seen[v.PersistentVolumeClaim.ClaimName] {
					continue
				}
				seen[v.PersistentVolumeClaim.ClaimName] = true
				pvc := claims[p.Namespace+"/"+v.PersistentVolumeClaim.ClaimName]
				if pvc == nil {
					continue
				}
				vc := &continuumv1.VolumeClaim{Name: pvc.Name, Phase: string(pvc.Status.Phase), PinnedNodes: pinned[pvc.Spec.VolumeName]}
				if pvc.Spec.StorageClassName != nil {
					vc.StorageClass = *pvc.Spec.StorageClassName
				}
				if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
					vc.RequestedBytes = q.Value()
				}
				for _, m := range pvc.Spec.AccessModes {
					vc.AccessModes = append(vc.AccessModes, string(m))
				}
				w.facts.VolumeClaims = append(w.facts.VolumeClaims, vc)
			}
		}
		sort.Slice(w.facts.VolumeClaims, func(i, j int) bool { return w.facts.VolumeClaims[i].Name < w.facts.VolumeClaims[j].Name })
	}
}

// pvNodes lists the node names a volume is restricted to by its node affinity ("kubernetes.io/hostname").
func pvNodes(v *corev1.PersistentVolume) []string {
	if v.Spec.NodeAffinity == nil || v.Spec.NodeAffinity.Required == nil {
		return nil
	}
	set := map[string]bool{}
	for _, t := range v.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, e := range t.MatchExpressions {
			if e.Key == corev1.LabelHostname && e.Operator == corev1.NodeSelectorOpIn {
				for _, val := range e.Values {
					set[val] = true
				}
			}
		}
		for _, e := range t.MatchFields {
			if e.Key == "metadata.name" && e.Operator == corev1.NodeSelectorOpIn {
				for _, val := range e.Values {
					set[val] = true
				}
			}
		}
	}
	var out []string
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// joinScaling attaches the autoscaler that targets a workload and the disruption budget that selects its pods.
func (c *Collector) joinScaling(all map[string]*wl) {
	if c.hpas != nil {
		each(c.hpas.List(), func(h *autoscalingv2.HorizontalPodAutoscaler) {
			w := all[h.Namespace+"/"+h.Spec.ScaleTargetRef.Kind+"/"+h.Spec.ScaleTargetRef.Name]
			if w == nil {
				return
			}
			a := &continuumv1.Autoscaler{MaxReplicas: h.Spec.MaxReplicas, MinReplicas: ptrOr(h.Spec.MinReplicas, 1), CurrentReplicas: h.Status.CurrentReplicas}
			for _, m := range h.Spec.Metrics {
				a.Targets = append(a.Targets, metricTarget(m))
			}
			w.facts.Autoscaler = a
		})
	}
	if c.pdbs != nil {
		each(c.pdbs.List(), func(b *policyv1.PodDisruptionBudget) {
			if b.Spec.Selector == nil {
				return
			}
			sel, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
			if err != nil || sel.Empty() {
				return
			}
			d := &continuumv1.Disruption{DisruptionsAllowed: b.Status.DisruptionsAllowed}
			if b.Spec.MinAvailable != nil {
				d.MinAvailable = b.Spec.MinAvailable.String()
			}
			if b.Spec.MaxUnavailable != nil {
				d.MaxUnavailable = b.Spec.MaxUnavailable.String()
			}
			for _, w := range all {
				if w.facts.Namespace == b.Namespace && sel.Matches(w.selector) && w.facts.Disruption == nil {
					w.facts.Disruption = d
				}
			}
		})
	}
}

func metricTarget(m autoscalingv2.MetricSpec) string {
	switch m.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if m.Resource == nil {
			return "resource"
		}
		t := m.Resource.Target
		switch {
		case t.AverageUtilization != nil:
			return fmt.Sprintf("%s %d%%", m.Resource.Name, *t.AverageUtilization)
		case t.AverageValue != nil:
			return fmt.Sprintf("%s %s", m.Resource.Name, t.AverageValue.String())
		}
		return string(m.Resource.Name)
	case autoscalingv2.ContainerResourceMetricSourceType:
		if m.ContainerResource != nil && m.ContainerResource.Target.AverageUtilization != nil {
			return fmt.Sprintf("%s %d%%", m.ContainerResource.Name, *m.ContainerResource.Target.AverageUtilization)
		}
		return "container resource"
	case autoscalingv2.PodsMetricSourceType:
		if m.Pods != nil {
			return "pods: " + m.Pods.Metric.Name
		}
	case autoscalingv2.ObjectMetricSourceType:
		if m.Object != nil {
			return "object: " + m.Object.Metric.Name
		}
	case autoscalingv2.ExternalMetricSourceType:
		if m.External != nil {
			return "external: " + m.External.Metric.Name
		}
	}
	return string(m.Type)
}

// appendReachable lists the addresses a Service can be reached on from outside its cluster: load
// balancer addresses, external IPs and node ports. A load balancer that is only a host name is not
// listed, because traffic is seen by IP address and the name's addresses are not known here.
func appendReachable(out []*continuumv1.Address, s *corev1.Service) []*continuumv1.Address {
	add := func(a *continuumv1.Address) {
		for _, x := range out {
			if x.Ip == a.Ip && x.Port == a.Port && x.Kind == a.Kind {
				return
			}
		}
		out = append(out, a)
	}
	for _, p := range s.Spec.Ports {
		for _, ing := range s.Status.LoadBalancer.Ingress {
			if ing.IP != "" {
				add(&continuumv1.Address{Ip: ing.IP, Port: p.Port, Kind: "load-balancer"})
			}
		}
		for _, ip := range s.Spec.ExternalIPs {
			add(&continuumv1.Address{Ip: ip, Port: p.Port, Kind: "external-ip"})
		}
		if p.NodePort != 0 && (s.Spec.Type == corev1.ServiceTypeNodePort || s.Spec.Type == corev1.ServiceTypeLoadBalancer) {
			add(&continuumv1.Address{Port: p.NodePort, Kind: "node-port"})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Ip != b.Ip {
			return a.Ip < b.Ip
		}
		return a.Port < b.Port
	})
	return out
}
