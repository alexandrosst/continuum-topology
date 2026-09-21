package facts

import "strings"

// Mesh kinds and modes as they appear in the wire format and the model.
const (
	MeshIstio   = "istio"
	MeshLinkerd = "linkerd"
	MeshConsul  = "consul"
	MeshKuma    = "kuma"

	ProxySidecar = "sidecar"
	ProxyAmbient = "ambient"
)

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
