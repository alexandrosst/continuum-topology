package collect

import (
	"net/netip"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Index turns the addresses seen on the wire into what they mean inside this cluster. It is built from
// the informers' caches and stays in the agent: pod addresses change constantly and mean nothing to
// the server, which only ever receives the conclusions.
type Index struct {
	// Pods maps a pod IP to the key of the workload that owns it ("namespace/Kind/name").
	Pods map[string]string
	// PodNames maps a pod IP to that pod's own name, alongside Pods' workload key for the same IP. Kept
	// separate (rather than folded into Pods) because almost nothing needs it: resolve.go consults it only
	// to fill Flow.src_pod/dst_pod, a field the aggregator deliberately excludes from a flow's identity
	// (flow.key in aggregate.go) so that distinct pods of one workload still collapse into a single,
	// bounded dependency edge. Only ever consulted for the live index, never through the resolver's
	// post-eviction "recent" memory of a pod that has since churned - unlike a workload, which outlives
	// any one of its pods, a stale pod name would be actively misleading once attached to new traffic.
	PodNames map[string]string
	// PodUIDs maps a pod's UID (Pod.ObjectMeta.UID) to the key of the workload that owns it - unlike Pods
	// above, keyed by UID rather than IP, since the one caller that needs this (WorkloadForCgroup)
	// resolves a cgroup id to a pod UID first (cgroupIDToPodUID, via the cgroup filesystem), never to an
	// IP. Populated for every visible pod, including a hostNetwork one - Pods above deliberately excludes
	// those (see its own doc comment just below), but PodUIDs has no such exclusion: a hostNetwork pod's
	// UID is exactly what WorkloadForCgroup needs to turn a resolved cgroup id into a workload key.
	PodUIDs map[string]string
	// Services maps a Service cluster IP to the workloads it selects.
	Services map[string][]string
	// Nodes maps a node address to the node name. Host-network pods share their node's address, so
	// traffic from them is attributed to the node, not to a workload.
	Nodes map[string]string
	// Opaque holds cluster IPs of Services that select no workload (the API server's own Service, for
	// example): traffic to them is real but says nothing about the applications, so it is dropped.
	Opaque map[string]bool
	// NodePorts maps a node port to the workloads behind it, for callers that dial a node address.
	NodePorts map[int32][]string
	// Hidden holds the addresses (pods and Service cluster IPs) of namespaces outside the agent's scope.
	// Traffic to or from them is dropped, so it never appears as a mystery endpoint.
	Hidden map[string]bool
	// PodCIDRs is this cluster's own pod network ranges, published per node (Node.Spec.PodCIDRs). Used only
	// to recognize a peer address as this cluster's own CNI plumbing - a bridge/gateway address (a flannel
	// gateway at the ".1" of a node's pod subnet, for example) or a pod that churned between index rebuilds
	// - instead of mislabeling clearly-internal infrastructure as an external endpoint.
	PodCIDRs []netip.Prefix
	// ServiceCIDR is this cluster's own Service network range, guessed the same way detectServiceCIDR does
	// (smallest bounding prefix over live ClusterIPs) - same purpose as PodCIDRs above, for an unresolvable
	// address that nonetheless falls inside the Service range.
	ServiceCIDR netip.Prefix
}

// Index builds the current address index. It returns an empty index below access tier 2.
func (c *Collector) Index() *Index {
	ix := &Index{Pods: map[string]string{}, PodNames: map[string]string{}, PodUIDs: map[string]string{}, Services: map[string][]string{}, Nodes: map[string]string{}, Opaque: map[string]bool{}, NodePorts: map[int32][]string{}, Hidden: map[string]bool{}}
	if c.nodes != nil {
		each(c.nodes.GetStore().List(), func(n *corev1.Node) {
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeInternalIP || a.Type == corev1.NodeExternalIP {
					ix.Nodes[a.Address] = n.Name
				}
			}
			for _, pc := range n.Spec.PodCIDRs {
				if p, err := netip.ParsePrefix(pc); err == nil {
					ix.PodCIDRs = append(ix.PodCIDRs, p)
				}
			}
		})
	}
	if c.pods == nil || c.rs == nil {
		return ix
	}
	rsOwner := map[string]string{}
	each(c.rs.List(), func(r *appsv1.ReplicaSet) {
		for _, o := range r.OwnerReferences {
			if o.Kind == "Deployment" {
				rsOwner[r.Namespace+"/"+r.Name] = o.Name
			}
		}
	})
	// pod-template labels of every workload, to join Services to workloads
	type owner struct {
		key string
		ns  string
		sel labels.Labels
	}
	var owners []owner
	if c.deploys != nil {
		each(c.deploys.List(), func(d *appsv1.Deployment) {
			owners = append(owners, owner{d.Namespace + "/Deployment/" + d.Name, d.Namespace, labels.Set(d.Spec.Template.Labels)})
		})
		each(c.sts.List(), func(d *appsv1.StatefulSet) {
			owners = append(owners, owner{d.Namespace + "/StatefulSet/" + d.Name, d.Namespace, labels.Set(d.Spec.Template.Labels)})
		})
		each(c.ds.List(), func(d *appsv1.DaemonSet) {
			owners = append(owners, owner{d.Namespace + "/DaemonSet/" + d.Name, d.Namespace, labels.Set(d.Spec.Template.Labels)})
		})
	}
	known := map[string]bool{}
	for _, o := range owners {
		known[o.key] = true
	}
	vis := c.visible()
	each(c.pods.List(), func(p *corev1.Pod) {
		if !vis(p.Namespace) {
			if !p.Spec.HostNetwork {
				for _, ip := range append([]string{p.Status.PodIP}, podIPs(p)...) {
					if ip != "" {
						ix.Hidden[ip] = true
					}
				}
			}
			return
		}
		if p.UID != "" && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			if key := ownerKey(p, rsOwner, known); key != "" {
				ix.PodUIDs[string(p.UID)] = key
			}
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed || p.Spec.HostNetwork {
			return
		}
		key := ownerKey(p, rsOwner, known)
		if key == "" {
			return
		}
		ips := map[string]bool{}
		if p.Status.PodIP != "" {
			ips[p.Status.PodIP] = true
		}
		for _, ip := range p.Status.PodIPs {
			ips[ip.IP] = true
		}
		for ip := range ips {
			ix.Pods[ip] = key
			ix.PodNames[ip] = p.Name
		}
	})
	if c.svcs != nil {
		if sc := detectServiceCIDR(c.svcs.List()); sc != "" {
			if p, err := netip.ParsePrefix(sc); err == nil {
				ix.ServiceCIDR = p
			}
		}
		each(c.svcs.List(), func(s *corev1.Service) {
			ips := append([]string{s.Spec.ClusterIP}, s.Spec.ClusterIPs...)
			if !vis(s.Namespace) {
				for _, ip := range ips {
					if ip != "" && ip != corev1.ClusterIPNone {
						ix.Hidden[ip] = true
					}
				}
				return
			}
			var keys []string
			if len(s.Spec.Selector) > 0 {
				sel := labels.SelectorFromSet(s.Spec.Selector)
				for _, o := range owners {
					if o.ns == s.Namespace && sel.Matches(o.sel) {
						keys = append(keys, o.key)
					}
				}
			}
			sort.Strings(keys)
			for _, ip := range ips {
				if ip == "" || ip == corev1.ClusterIPNone {
					continue
				}
				if len(keys) == 0 {
					ix.Opaque[ip] = true
				} else {
					ix.Services[ip] = keys
				}
			}
			for _, p := range s.Spec.Ports {
				if p.NodePort != 0 && len(keys) > 0 {
					ix.NodePorts[p.NodePort] = keys
				}
			}
		})
	}
	return ix
}

// ownerKey returns the workload key ("namespace/Kind/name") that owns p, by the same
// ReplicaSet->Deployment/StatefulSet/DaemonSet resolution Index() already applied inline before this
// was factored out - "" when p has no recognized owner, or its owner is not one Index() already found
// among this cluster's own Deployments/StatefulSets/DaemonSets (known).
func ownerKey(p *corev1.Pod, rsOwner map[string]string, known map[string]bool) string {
	key := ""
	for _, o := range p.OwnerReferences {
		switch o.Kind {
		case "ReplicaSet":
			if d, ok := rsOwner[p.Namespace+"/"+o.Name]; ok {
				key = p.Namespace + "/Deployment/" + d
			}
		case "StatefulSet", "DaemonSet":
			key = p.Namespace + "/" + o.Kind + "/" + o.Name
		}
	}
	if key == "" || !known[key] {
		return ""
	}
	return key
}

func podIPs(p *corev1.Pod) []string {
	out := make([]string, 0, len(p.Status.PodIPs))
	for _, ip := range p.Status.PodIPs {
		out = append(out, ip.IP)
	}
	return out
}
