package interpret

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
)

// Grouping origins, strongest first.
const (
	OriginExplicit  = "explicit"  // continuum.io/application
	OriginArgo      = "argo"      // Argo CD tracking id / instance
	OriginHelm      = "helm"      // Helm release
	OriginPartOf    = "part-of"   // app.kubernetes.io/part-of
	OriginNamespace = "namespace" // fallback
)

type appRef struct {
	name, origin, confidence, signal string
}

// applicationFor decides which application a workload belongs to. The first rule that
// matches wins. Everything here works from labels and annotations alone; nothing needs a
// service mesh or any other tool to be installed.
func applicationFor(w *continuumv1.WorkloadFacts) appRef {
	l, a := w.Labels, w.Annotations
	if v := l["continuum.io/application"]; v != "" {
		return appRef{v, OriginExplicit, "high", "label continuum.io/application=" + v}
	}
	// Argo CD's tracking id looks like "<app>:<group>/<kind>:<namespace>/<name>".
	if v := a["argocd.argoproj.io/tracking-id"]; v != "" {
		if i := strings.Index(v, ":"); i > 0 {
			return appRef{v[:i], OriginArgo, "high", "annotation argocd.argoproj.io/tracking-id"}
		}
	}
	if v := l["argocd.argoproj.io/instance"]; v != "" {
		return appRef{v, OriginArgo, "high", "label argocd.argoproj.io/instance=" + v}
	}
	if v := a["meta.helm.sh/release-name"]; v != "" {
		return appRef{v, OriginHelm, "high", "annotation meta.helm.sh/release-name=" + v}
	}
	if strings.EqualFold(l["app.kubernetes.io/managed-by"], "Helm") && l["app.kubernetes.io/instance"] != "" {
		return appRef{l["app.kubernetes.io/instance"], OriginHelm, "medium", "labels managed-by=Helm, instance=" + l["app.kubernetes.io/instance"]}
	}
	if v := l["app.kubernetes.io/part-of"]; v != "" {
		return appRef{v, OriginPartOf, "medium", "label app.kubernetes.io/part-of=" + v}
	}
	return appRef{w.Namespace, OriginNamespace, "low", "namespace " + w.Namespace}
}

func managedBy(w *continuumv1.WorkloadFacts) string {
	l, a := w.Labels, w.Annotations
	switch {
	case a["argocd.argoproj.io/tracking-id"] != "" || l["argocd.argoproj.io/instance"] != "":
		return "argo"
	case l["helm.toolkit.fluxcd.io/name"] != "" || l["kustomize.toolkit.fluxcd.io/name"] != "":
		return "flux"
	case a["meta.helm.sh/release-name"] != "" || strings.EqualFold(l["app.kubernetes.io/managed-by"], "Helm"):
		return "helm"
	case strings.EqualFold(l["app.kubernetes.io/managed-by"], "kustomize"):
		return "kustomize"
	case strings.Contains(strings.ToLower(l["app.kubernetes.io/managed-by"]), "operator"):
		return "operator"
	}
	return "unknown"
}

func hash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:6])
}

// applicationID is stable and, for named groupings (explicit, Argo, Helm, part-of), shared across
// clusters so one application can span cloud and edge. Namespace groupings stay per cluster because
// names like "default" or "apps" mean nothing across clusters.
func applicationID(org string, r appRef, clusterID string) string {
	if r.origin == OriginNamespace {
		return "app-" + hash(org, "ns", clusterID, r.name)
	}
	return "app-" + hash(org, "name", strings.ToLower(r.name))
}
