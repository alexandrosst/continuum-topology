package collect

import "strings"

// The agent sends only labels and annotations it recognises. Everything else stays in the
// cluster. This is the privacy boundary for metadata: nothing the allow-list does not name
// (an annotation holding a URL with a token, for example) can leave the cluster.

var labelExact = map[string]bool{
	"app": true, "k8s-app": true,
	"kubernetes.io/arch": true, "kubernetes.io/os": true,
	"kubernetes.azure.com/cluster": true, "eks.amazonaws.com/nodegroup": true, "cloud.google.com/gke-nodepool": true,
	"argocd.argoproj.io/instance": true, "kustomize.toolkit.fluxcd.io/name": true, "helm.toolkit.fluxcd.io/name": true,
	"helm.sh/chart": true,
	// Service-mesh markers: whether a namespace or workload is meant to be in a mesh. Nothing else about the mesh's
	// configuration is read from labels.
	"istio-injection": true, "istio.io/rev": true, "istio.io/dataplane-mode": true,
	"sidecar.istio.io/inject": true, "kuma.io/sidecar-injection": true,
}

var labelPrefix = []string{
	"app.kubernetes.io/", "continuum.io/", "topology.kubernetes.io/", "failure-domain.beta.kubernetes.io/",
	"node.kubernetes.io/", "node-role.kubernetes.io/", "beta.kubernetes.io/",
	"feature.node.kubernetes.io/cpu-", "nvidia.com/gpu.", "microk8s.io/", "minikube.k8s.io/", "node.openshift.io/",
}

var annotationExact = map[string]bool{
	"meta.helm.sh/release-name": true, "meta.helm.sh/release-namespace": true,
	"argocd.argoproj.io/tracking-id":         true,
	"kubeadm.alpha.kubernetes.io/cri-socket": true,
	"flannel.alpha.coreos.com/backend-type":  true,
	// Service-mesh markers (see labelExact).
	"sidecar.istio.io/inject": true, "sidecar.istio.io/status": true, "ambient.istio.io/redirection": true,
	"linkerd.io/inject": true, "linkerd.io/proxy-version": true,
	"consul.hashicorp.com/connect-inject": true, "kuma.io/sidecar-injection": true,
}

// The prefixes hold the port lists a workload excludes from its proxy.
var annotationPrefix = []string{"continuum.io/", "traffic.sidecar.istio.io/", "config.linkerd.io/skip-"}

const (
	maxEntries  = 60
	maxValueLen = 128
)

func filterLabels(in map[string]string) map[string]string {
	return filter(in, labelExact, labelPrefix)
}

func filterAnnotations(in map[string]string) map[string]string {
	return filter(in, annotationExact, annotationPrefix)
}

func filter(in map[string]string, exact map[string]bool, prefixes []string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if len(out) >= maxEntries {
			break
		}
		if !allowed(k, exact, prefixes) {
			continue
		}
		if len(v) > maxValueLen {
			v = v[:maxValueLen]
		}
		out[k] = v
	}
	return out
}

func allowed(k string, exact map[string]bool, prefixes []string) bool {
	if exact[k] {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}
