package facts

import (
	"strconv"
	"strings"
)

// Mesh kinds and modes as they appear in the wire format and the model.
const (
	MeshIstio   = "istio"
	MeshLinkerd = "linkerd"
	MeshConsul  = "consul"
	MeshKuma    = "kuma"

	ProxySidecar = "sidecar"
	ProxyAmbient = "ambient"

	// IstioOutboundPort is the local port istio-iptables redirects a sidecar-injected pod's outbound TCP
	// traffic to (envoy's own outbound listener) - istio-iptables' own default, stable across versions.
	// Kept here, documented, for anything on the Go side that needs to say this number out loud (a test,
	// a UI string); flow.c's own observe_egress/note_mesh_bypass deliberately does not compare against
	// this specific value at all (see that function's doc comment for why "is this loopback" is already
	// enough), so this constant exists purely as the canonical, citable source for the number itself.
	IstioOutboundPort = 15001
	// LinkerdOutboundPort is the same thing for Linkerd: the local port linkerd2-proxy-init redirects a
	// meshed pod's outbound traffic to (linkerd-proxy's own outbound listener) - linkerd2-proxy-init's
	// own default, also stable across versions. Same caveat as IstioOutboundPort above.
	LinkerdOutboundPort = 4140
)

// PortExcluded reports whether entries (a WorkloadMesh.ExcludedPorts-shaped list, e.g. "out:5432",
// "in:8000-8100" - see mesh.go's own excludedPorts) lists port for the given direction ("in" or "out").
// Mirrors the frontend's own src/lib/mesh.ts "listed" helper exactly; kept as the one place either side
// of a connection's mesh-bypass evidence gets checked against what a workload actually declared it keeps
// out of its proxy, so the two implementations cannot quietly drift apart.
func PortExcluded(entries []string, dir string, port int) bool {
	for _, e := range entries {
		d, spec, ok := strings.Cut(e, ":")
		if !ok || d != dir || spec == "" {
			continue
		}
		lo, hi, ok := strings.Cut(spec, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		if !ok {
			if port == a {
				return true
			}
			continue
		}
		b, err := strconv.Atoi(hi)
		if err != nil {
			continue
		}
		if port >= a && port <= b {
			return true
		}
	}
	return false
}

// NamespaceMesh says whether a namespace's own labels and annotations ask for its workloads to join a mesh, and
// how. It reads only the markers the agent keeps (see the allow-list in the collector). mesh is empty when the
// namespace asks for nothing; explicitOff is true when it asks not to be meshed.
func NamespaceMesh(labels, annotations map[string]string) (mesh, proxy string, explicitOff bool) {
	switch {
	case strings.EqualFold(labels["istio.io/dataplane-mode"], "ambient"):
		return MeshIstio, ProxyAmbient, false
	case strings.EqualFold(labels["istio-injection"], "enabled"), labels["istio.io/rev"] != "":
		return MeshIstio, ProxySidecar, false
	case strings.EqualFold(labels["istio-injection"], "disabled"):
		return MeshIstio, "", true
	case strings.EqualFold(annotations["linkerd.io/inject"], "enabled"), strings.EqualFold(annotations["linkerd.io/inject"], "ingress"):
		return MeshLinkerd, ProxySidecar, false
	case strings.EqualFold(annotations["linkerd.io/inject"], "disabled"):
		return MeshLinkerd, "", true
	case strings.EqualFold(annotations["consul.hashicorp.com/connect-inject"], "true"):
		return MeshConsul, ProxySidecar, false
	case strings.EqualFold(labels["kuma.io/sidecar-injection"], "enabled"), strings.EqualFold(annotations["kuma.io/sidecar-injection"], "enabled"):
		return MeshKuma, ProxySidecar, false
	}
	return "", "", false
}
