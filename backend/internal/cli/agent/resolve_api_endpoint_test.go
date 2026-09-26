package agent

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A real cluster's "kubernetes" Endpoints in "default" (whatever backs the control plane) is what
// resolveApiEndpoint reads instead of the virtual ClusterIP every in-cluster client is handed by default.
func TestResolveApiEndpointPrefersTheRealBackendAddress(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "10.0.5.12"}},
			Ports:     []corev1.EndpointPort{{Name: "https", Port: 6443}},
		}},
	})
	if got, want := resolveApiEndpoint(client), "10.0.5.12:6443"; got != want {
		t.Errorf("resolveApiEndpoint() = %q, want %q", got, want)
	}
}

// A port with no name at all is still usable - the first (and here, only) one listed.
func TestResolveApiEndpointFallsBackToTheFirstPortWhenNoneIsNamedHttpsOr443(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "10.0.5.12"}},
			Ports:     []corev1.EndpointPort{{Name: "", Port: 8443}},
		}},
	})
	if got, want := resolveApiEndpoint(client), "10.0.5.12:8443"; got != want {
		t.Errorf("resolveApiEndpoint() = %q, want %q", got, want)
	}
}

// No permission, no such object, or no subset ready yet: the caller must be able to tell "nothing better
// found" apart from a real address, so it can keep whatever it already had.
func TestResolveApiEndpointIsEmptyWhenThereIsNothingUsable(t *testing.T) {
	cases := []struct {
		name   string
		client func() *fake.Clientset
	}{
		{"no such object (RBAC not granted, or a non-standard cluster)", func() *fake.Clientset { return fake.NewSimpleClientset() }},
		{"object exists but no subset has come up yet", func() *fake.Clientset {
			return fake.NewSimpleClientset(&corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}})
		}},
		{"a subset with no addresses", func() *fake.Clientset {
			return fake.NewSimpleClientset(&corev1.Endpoints{
				ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
				Subsets:    []corev1.EndpointSubset{{Ports: []corev1.EndpointPort{{Port: 443}}}},
			})
		}},
	}
	for _, c := range cases {
		if got := resolveApiEndpoint(c.client()); got != "" {
			t.Errorf("%s: resolveApiEndpoint() = %q, want \"\"", c.name, got)
		}
	}
}
