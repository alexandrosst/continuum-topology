// Package interpret turns raw agent facts into schema-v3 records. Rules live here,
// on the server, so they can improve without redeploying agents. Every detected value
// carries the signal that produced it and how sure we are.
package interpret

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/model"
)

func ev(signal, conf, detail string) model.Evidence {
	return model.Evidence{Signal: signal, Confidence: conf, Detail: detail}
}

// Distribution names, as shown in the UI.
const (
	DistK3s       = "k3s"
	DistRKE2      = "RKE2"
	DistK0s       = "k0s"
	DistEKS       = "EKS"
	DistGKE       = "GKE"
	DistAKS       = "AKS"
	DistOpenShift = "OpenShift"
	DistMicroK8s  = "MicroK8s"
	DistKind      = "kind"
	DistMinikube  = "minikube"
	DistTalos     = "Talos"
	DistDocker    = "Docker Desktop"
	DistKubeadm   = "kubeadm"
	DistUnknown   = "Kubernetes"
)

// detectDistribution combines the API server's version string, node labels and OS image.
// Order matters: the most specific, least spoofable signal wins.
func detectDistribution(c *continuumv1.ClusterFacts, nodes []*continuumv1.NodeFacts) (string, model.Evidence) {
	ver := ""
	if c != nil {
		ver = c.Version
	}
	switch {
	case strings.Contains(ver, "+k3s"):
		return DistK3s, ev("API version "+ver, "high", "version suffix +k3s")
	case strings.Contains(ver, "+rke2"):
		return DistRKE2, ev("API version "+ver, "high", "version suffix +rke2")
	case strings.Contains(ver, "+k0s"):
		return DistK0s, ev("API version "+ver, "high", "version suffix +k0s")
	case strings.Contains(ver, "-eks-"):
		return DistEKS, ev("API version "+ver, "high", "version contains -eks-")
	case strings.Contains(ver, "-gke."):
		return DistGKE, ev("API version "+ver, "high", "version contains -gke.")
	}
	has := func(match func(n *continuumv1.NodeFacts) (string, bool)) (string, bool) {
		for _, n := range nodes {
			if s, ok := match(n); ok {
				return s, true
			}
		}
		return "", false
	}
	label := func(key string) func(n *continuumv1.NodeFacts) (string, bool) {
		return func(n *continuumv1.NodeFacts) (string, bool) {
			_, ok := n.Labels[key]
			return "node label " + key, ok
		}
	}
	if s, ok := has(func(n *continuumv1.NodeFacts) (string, bool) {
		v := n.Labels["node.kubernetes.io/instance-type"]
		return "node label node.kubernetes.io/instance-type=" + v, v == "k3s"
	}); ok {
		return DistK3s, ev(s, "high", "")
	}
	if s, ok := has(func(n *continuumv1.NodeFacts) (string, bool) {
		return "node providerID " + n.ProviderId, strings.HasPrefix(n.ProviderId, "k3s://")
	}); ok {
		return DistK3s, ev(s, "medium", "")
	}
	for _, r := range []struct {
		dist, key string
	}{
		{DistOpenShift, "node.openshift.io/os_id"},
		{DistMicroK8s, "microk8s.io/cluster"},
		{DistMinikube, "minikube.k8s.io/name"},
		{DistAKS, "kubernetes.azure.com/cluster"},
		{DistEKS, "eks.amazonaws.com/nodegroup"},
		{DistGKE, "cloud.google.com/gke-nodepool"},
	} {
		if s, ok := has(label(r.key)); ok {
			return r.dist, ev(s, "high", "")
		}
	}
	if s, ok := has(func(n *continuumv1.NodeFacts) (string, bool) {
		return "node providerID " + n.ProviderId, strings.HasPrefix(n.ProviderId, "kind://")
	}); ok {
		return DistKind, ev(s, "high", "")
	}
	if s, ok := has(func(n *continuumv1.NodeFacts) (string, bool) {
		return "node OS image " + n.OsImage, strings.HasPrefix(n.OsImage, "Talos")
	}); ok {
		return DistTalos, ev(s, "high", "")
	}
	if len(nodes) == 1 && nodes[0].Name == "docker-desktop" {
		return DistDocker, ev("single node named docker-desktop", "medium", "")
	}
	if s, ok := has(func(n *continuumv1.NodeFacts) (string, bool) {
		_, ok := n.Annotations["kubeadm.alpha.kubernetes.io/cri-socket"]
		return "node annotation kubeadm.alpha.kubernetes.io/cri-socket", ok
	}); ok {
		return DistKubeadm, ev(s, "medium", "kubeadm-managed node; could also be a kubeadm-based product")
	}
	return DistUnknown, ev("no distribution-specific signal", "low", "")
}

var metalInstance = regexp.MustCompile(`\.metal(-[a-z0-9]+)?$`)

// detectProvider reads the cloud from providerID / labels.
func detectProvider(nodes []*continuumv1.NodeFacts) (string, model.Evidence) {
	for _, n := range nodes {
		p := n.ProviderId
		for prefix, name := range map[string]string{
			"aws://": "AWS", "gce://": "Google Cloud", "azure://": "Azure", "hcloud://": "Hetzner Cloud",
			// hrobot:// is Hetzner's dedicated-server product (Robot), not the Cloud API: a rented physical
			// box, not a hyperscaler VM, so it is named and tiered differently from hcloud://.
			"hrobot://": "Hetzner Robot", "digitalocean://": "DigitalOcean", "openstack://": "OpenStack",
			"vsphere://": "VMware vSphere", "linode://": "Linode", "scaleway://": "Scaleway", "ovhcloud://": "OVHcloud",
			"kind://": "Local (kind)",
		} {
			if strings.HasPrefix(p, prefix) {
				return name, ev("node providerID "+prefix+"…", "high", "")
			}
		}
	}
	return "On-prem", ev("no cloud providerID on any node", "low", "assumed on-prem or edge")
}

type nodeKindResult struct {
	kind, hardware string
	kindEv         model.Evidence
	hwEv           *model.Evidence
	// Set only when a node probe reported on the machine.
	probed       bool
	virt         string
	virtEv       *model.Evidence
	connectivity string
	connEv       *model.Evidence
	battery      bool
}

func memGB(b int64) float64 { return float64(b) / (1 << 30) }

// detectNodeKind decides vm / bare-metal / edge-device from what the Kubernetes API exposes.
// Without a node probe (DMI, hypervisor flag) a plain x86 host on-prem is genuinely ambiguous,
// and the result says so with low confidence rather than guessing quietly.
func detectNodeKind(n *continuumv1.NodeFacts) nodeKindResult {
	inst := firstNonEmpty(n.Labels["node.kubernetes.io/instance-type"], n.Labels["beta.kubernetes.io/instance-type"])
	if inst == "k3s" {
		inst = ""
	}
	arm := strings.HasPrefix(n.Architecture, "arm")
	hw, hwEv := detectHardware(n)

	// The node probe reads the machine itself, so when it has reported it decides.
	if n.Probe != nil {
		if r, ok := kindFromProbe(n, hw, hwEv); ok {
			return r
		}
	}

	// Node Feature Discovery, when the cluster runs it, reports the hypervisor flag directly.
	if v, ok := n.Labels["feature.node.kubernetes.io/cpu-cpuid.HYPERVISOR"]; ok {
		if v == "true" {
			return nodeKindResult{kind: "vm", hardware: hw, kindEv: ev("NFD label cpu-cpuid.HYPERVISOR=true", "high", "CPU reports a hypervisor"), hwEv: hwEv}
		}
		return nodeKindResult{kind: "bare-metal", hardware: hw, kindEv: ev("NFD label cpu-cpuid.HYPERVISOR absent/false", "high", "no hypervisor flag"), hwEv: hwEv}
	}
	switch {
	case strings.HasPrefix(n.ProviderId, "aws://") && metalInstance.MatchString(inst):
		return nodeKindResult{kind: "bare-metal", hardware: hw, kindEv: ev("AWS instance type "+inst, "high", ".metal instance"), hwEv: hwEv}
	case strings.HasPrefix(n.ProviderId, "hrobot://"):
		return nodeKindResult{kind: "bare-metal", hardware: hw, kindEv: ev("providerID hrobot://", "high", "Hetzner dedicated server"), hwEv: hwEv}
	case strings.HasPrefix(n.ProviderId, "aws://"), strings.HasPrefix(n.ProviderId, "gce://"), strings.HasPrefix(n.ProviderId, "azure://"),
		strings.HasPrefix(n.ProviderId, "hcloud://"), strings.HasPrefix(n.ProviderId, "digitalocean://"), strings.HasPrefix(n.ProviderId, "vsphere://"),
		strings.HasPrefix(n.ProviderId, "openstack://"), strings.HasPrefix(n.ProviderId, "linode://"), strings.HasPrefix(n.ProviderId, "scaleway://"):
		return nodeKindResult{kind: "vm", hardware: hw, kindEv: ev("cloud providerID "+scheme(n.ProviderId), "high", "cloud instances are virtual machines"), hwEv: hwEv}
	case strings.HasPrefix(n.ProviderId, "kind://"):
		return nodeKindResult{kind: "vm", hardware: hw, kindEv: ev("providerID kind://", "medium", "a container standing in for a machine"), hwEv: hwEv}
	}
	if hw != "" && arm { // Pi / Jetson class boards
		return nodeKindResult{kind: "edge-device", hardware: hw, kindEv: ev("hardware "+hw, "medium", "single-board computer"), hwEv: hwEv}
	}
	if arm && memGB(n.MemoryCapacityBytes) <= 16 && n.ProviderId == "" || (arm && strings.HasPrefix(n.ProviderId, "k3s://") && memGB(n.MemoryCapacityBytes) <= 8) {
		return nodeKindResult{kind: "edge-device", hardware: hw, kindEv: ev("ARM CPU, ≤16 GB memory, no cloud provider", "low", "typical of an edge board; confirm"), hwEv: hwEv}
	}
	return nodeKindResult{kind: "vm", hardware: hw, kindEv: ev("no hypervisor or hardware signal in the Kubernetes API", "low", "could be bare metal; the node probe can decide"), hwEv: hwEv}
}

func detectHardware(n *continuumv1.NodeFacts) (string, *model.Evidence) {
	k := strings.ToLower(n.KernelVersion)
	switch {
	case strings.Contains(k, "tegra"):
		e := ev("kernel "+n.KernelVersion, "medium", "Tegra kernel")
		return "NVIDIA Jetson", &e
	case strings.Contains(k, "raspi"), strings.Contains(k, "rpt-rpi"), strings.HasSuffix(k, "-v8+"), strings.HasSuffix(k, "-v7l+"), strings.HasSuffix(k, "-v7+"):
		e := ev("kernel "+n.KernelVersion, "medium", "Raspberry Pi kernel")
		return "Raspberry Pi", &e
	}
	if p := n.Labels["nvidia.com/gpu.machine"]; p != "" {
		e := ev("GPU feature discovery machine="+p, "high", "")
		return p, &e
	}
	return "", nil
}

func scheme(p string) string {
	if i := strings.Index(p, "://"); i > 0 {
		return p[:i+3]
	}
	return p
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func nodeRole(n *continuumv1.NodeFacts) string {
	for k := range n.Labels {
		if k == "node-role.kubernetes.io/control-plane" || k == "node-role.kubernetes.io/master" {
			return "control-plane"
		}
	}
	return "worker"
}

func nodeStatus(n *continuumv1.NodeFacts) string {
	switch {
	case !n.Ready:
		return "offline"
	case len(n.ProblemConditions) > 0:
		return "degraded"
	}
	return "healthy"
}

// tierFor places a cluster on the continuum. It is a suggestion the UI lets a person override -
// see Cluster.overrides in the frontend model, which keeps a corrected tier even after rediscovery.
func tierFor(provider string, nodes []*continuumv1.NodeFacts, kinds map[string]string) (string, model.Evidence) {
	switch provider {
	// A hyperscaler or another public cloud API: real VMs, not something anyone racked themselves.
	// Hetzner's dedicated-server product (Robot, hrobot://) is deliberately left out here - it is rented
	// hardware, not a cloud API, and detectNodeKind already calls its nodes bare-metal.
	case "AWS", "Google Cloud", "Azure", "DigitalOcean", "Linode", "Scaleway", "OVHcloud", "Hetzner Cloud":
		return "cloud", ev("provider "+provider, "medium", "hyperscaler or public cloud")
	}
	// far-edge: most or all nodes are single-board computers (Pi/Jetson class - see detectNodeKind).
	// A cluster is rarely purely uniform: one node without a probe, or a slightly bigger box acting as
	// control plane at the same site, is common and shouldn't by itself demote the whole cluster back to
	// the generic "edge" bucket. Nodes whose kind could not be determined at all are left out of the
	// count rather than treated as evidence either way.
	edge, known := 0, 0
	for _, n := range nodes {
		if k := kinds[n.Key]; k != "" {
			known++
			if k == "edge-device" {
				edge++
			}
		}
	}
	switch {
	case known > 0 && edge == known:
		return "far-edge", ev("every node is a single-board edge device", "medium", "no bare-metal server or VM was seen among them")
	case known > 0 && float64(edge)/float64(known) > 0.5:
		return "far-edge", ev(fmt.Sprintf("%d of %d nodes are single-board edge devices", edge, known), "low", "the rest may be a control-plane box or gateway at the same site")
	}
	return "edge", ev("not a public cloud", "low", "on-prem and edge share this default; open the cluster and set the tier if this is a data center")
}

// commonPodCIDR returns the smallest prefix covering all nodes' pod CIDRs.
func commonPodCIDR(nodes []*continuumv1.NodeFacts) string {
	var first netip.Prefix
	bits := 128
	n := 0
	var prefixes []netip.Prefix
	for _, nd := range nodes {
		for _, c := range nd.PodCidrs {
			p, err := netip.ParsePrefix(c)
			if err != nil || !p.Addr().Is4() {
				continue
			}
			prefixes = append(prefixes, p.Masked())
		}
	}
	if len(prefixes) == 0 {
		return ""
	}
	first = prefixes[0]
	bits = first.Bits()
	for _, p := range prefixes[1:] {
		b := commonBits(first.Addr(), p.Addr())
		if b < bits {
			bits = b
		}
		if p.Bits() < bits {
			bits = p.Bits()
		}
		n++
	}
	out, _ := first.Addr().Prefix(bits)
	return out.String()
}

func commonBits(a, b netip.Addr) int {
	x, y := a.As4(), b.As4()
	c := 0
	for i := 0; i < 4; i++ {
		d := x[i] ^ y[i]
		if d == 0 {
			c += 8
			continue
		}
		for m := byte(0x80); m != 0 && d&m == 0; m >>= 1 {
			c++
		}
		break
	}
	return c
}
