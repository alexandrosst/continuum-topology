package interpret

import "testing"

func wl(namespace string, labels, annotations map[string]string) *W {
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	return &W{Namespace: namespace, Labels: labels, Annotations: annotations}
}

func TestApplicationForIsTheStrongestCandidate(t *testing.T) {
	w := wl("shop", map[string]string{"continuum.io/application": "checkout", "app.kubernetes.io/part-of": "shop-suite"}, nil)
	got := applicationFor(w)
	cands := applicationCandidates(w)
	if got != cands[0] {
		t.Errorf("applicationFor should return applicationCandidates()[0], got %+v vs %+v", got, cands[0])
	}
	if got.name != "checkout" || got.origin != OriginExplicit {
		t.Errorf("expected the explicit label to win, got %+v", got)
	}
}

func TestApplicationCandidatesListsEveryTierThatMatched(t *testing.T) {
	w := wl("shop", map[string]string{
		"continuum.io/application":     "checkout",
		"app.kubernetes.io/part-of":    "shop-suite",
		"app.kubernetes.io/managed-by": "Helm",
		"app.kubernetes.io/instance":   "checkout-release",
	}, nil)
	cands := applicationCandidates(w)
	if len(cands) != 4 { // explicit, helm (instance), part-of, namespace
		t.Fatalf("expected 4 candidates, got %d: %+v", len(cands), cands)
	}
	origins := []string{cands[0].origin, cands[1].origin, cands[2].origin, cands[3].origin}
	want := []string{OriginExplicit, OriginHelm, OriginPartOf, OriginNamespace}
	for i := range want {
		if origins[i] != want[i] {
			t.Errorf("candidate %d origin = %s, want %s (order: %v)", i, origins[i], want[i], origins)
		}
	}
}

func TestApplicationCandidatesArgoTrackingIdFallsThroughWhenMalformed(t *testing.T) {
	// No colon in the tracking id: applicationFor used to fall through to the instance label, and the
	// candidate list must keep doing the same thing, not silently drop the Argo tier.
	w := wl("shop", map[string]string{"argocd.argoproj.io/instance": "checkout-argo"}, map[string]string{"argocd.argoproj.io/tracking-id": "malformed-no-colon"})
	cands := applicationCandidates(w)
	if cands[0].origin != OriginArgo || cands[0].name != "checkout-argo" {
		t.Errorf("expected the instance label to win when the tracking id is malformed, got %+v", cands[0])
	}
}

func TestApplicationCandidatesArgoTrackingIdWins(t *testing.T) {
	w := wl("shop", map[string]string{"argocd.argoproj.io/instance": "checkout-argo"}, map[string]string{"argocd.argoproj.io/tracking-id": "checkout:apps/Deployment:shop/checkout"})
	cands := applicationCandidates(w)
	if cands[0].origin != OriginArgo || cands[0].name != "checkout" {
		t.Errorf("expected the tracking id to win over the instance label, got %+v", cands[0])
	}
}

func TestApplicationCandidatesJustNamespaceWhenNothingElseMatches(t *testing.T) {
	w := wl("shop", nil, nil)
	cands := applicationCandidates(w)
	if len(cands) != 1 || cands[0].origin != OriginNamespace || cands[0].name != "shop" {
		t.Errorf("expected only the namespace fallback, got %+v", cands)
	}
}
