package collect

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func svc(ips ...string) *corev1.Service {
	spec := corev1.ServiceSpec{}
	if len(ips) == 1 {
		spec.ClusterIP = ips[0]
	} else if len(ips) > 0 {
		spec.ClusterIPs = ips
	}
	return &corev1.Service{Spec: spec}
}

func TestDetectServiceCIDRCoversEveryClusterIP(t *testing.T) {
	svcs := []any{
		svc("10.96.0.1"),   // kubernetes.default
		svc("10.96.0.10"),  // kube-dns
		svc("10.96.12.34"), // some app
	}
	got := detectServiceCIDR(svcs)
	if got != "10.96.0.0/20" {
		t.Fatalf("got %q, want the smallest range covering all three addresses", got)
	}
}

func TestDetectServiceCIDRIgnoresHeadlessAndEmpty(t *testing.T) {
	svcs := []any{
		svc(corev1.ClusterIPNone), // headless: "None"
		svc(""),                   // not yet assigned
		svc("10.96.0.1"),
	}
	if got := detectServiceCIDR(svcs); got != "10.96.0.1/32" {
		t.Fatalf("got %q, want only the one real ClusterIP counted", got)
	}
}

func TestDetectServiceCIDRPrefersClusterIPsOverSingularWhenBothSet(t *testing.T) {
	s := svc("10.96.0.1")
	s.Spec.ClusterIPs = []string{"10.96.0.1", "10.96.0.2"}
	got := detectServiceCIDR([]any{s})
	if got != "10.96.0.0/30" {
		t.Fatalf("got %q, want the dual-stack ClusterIPs list to be used", got)
	}
}

func TestDetectServiceCIDRNoneObserved(t *testing.T) {
	if got := detectServiceCIDR(nil); got != "" {
		t.Fatalf("got %q, want empty with nothing to go on", got)
	}
	if got := detectServiceCIDR([]any{svc(corev1.ClusterIPNone)}); got != "" {
		t.Fatalf("got %q, want empty when every service is headless", got)
	}
}

func TestDetectServiceCIDRIgnoresIPv6(t *testing.T) {
	svcs := []any{svc("10.96.0.1"), svc("fd00::1")}
	if got := detectServiceCIDR(svcs); got != "10.96.0.1/32" {
		t.Fatalf("got %q, want the IPv6 address skipped", got)
	}
}
