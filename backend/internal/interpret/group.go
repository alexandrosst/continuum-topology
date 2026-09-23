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

// applicationFor decides which application a workload belongs to: the strongest match from
// applicationCandidates. Everything here works from labels and annotations alone; nothing needs a
// service mesh or any other tool to be installed.
func applicationFor(w *continuumv1.WorkloadFacts) appRef {
	return applicationCandidates(w)[0]
}

// applicationCandidates tries every grouping rule against the workload, strongest first, instead of
// stopping at the first match: applicationFor uses only the first result, and the rest are offered as
// alternatives a person can regroup onto instead of the winning one, without typing a name by hand.
// Always returns at least one candidate (the namespace fallback never fails to match).
func applicationCandidates(w *continuumv1.WorkloadFacts) []appRef {
	l, a := w.Labels, w.Annotations
	var out []appRef
	if v := l["continuum.io/application"]; v != "" {
		out = append(out, appRef{v, OriginExplicit, "high", "label continuum.io/application=" + v})
	}
	// Argo CD's tracking id looks like "<app>:<group>/<kind>:<namespace>/<name>". A malformed one (no
	// colon) is not usable, so the instance label is tried next, same as applicationFor used to fall through.
	switch v := a["argocd.argoproj.io/tracking-id"]; {
	case v != "" && strings.Index(v, ":") > 0:
		out = append(out, appRef{v[:strings.Index(v, ":")], OriginArgo, "high", "annotation argocd.argoproj.io/tracking-id"})
	case l["argocd.argoproj.io/instance"] != "":
		out = append(out, appRef{l["argocd.argoproj.io/instance"], OriginArgo, "high", "label argocd.argoproj.io/instance=" + l["argocd.argoproj.io/instance"]})
	}
	switch {
	case a["meta.helm.sh/release-name"] != "":
		out = append(out, appRef{a["meta.helm.sh/release-name"], OriginHelm, "high", "annotation meta.helm.sh/release-name=" + a["meta.helm.sh/release-name"]})
	case strings.EqualFold(l["app.kubernetes.io/managed-by"], "Helm") && l["app.kubernetes.io/instance"] != "":
		out = append(out, appRef{l["app.kubernetes.io/instance"], OriginHelm, "medium", "labels managed-by=Helm, instance=" + l["app.kubernetes.io/instance"]})
	}
	if v := l["app.kubernetes.io/part-of"]; v != "" {
		out = append(out, appRef{v, OriginPartOf, "medium", "label app.kubernetes.io/part-of=" + v})
	}
	out = append(out, appRef{w.Namespace, OriginNamespace, "low", "namespace " + w.Namespace})
	return out
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
