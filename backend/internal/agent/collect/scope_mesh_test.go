package collect

import (
	"context"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/encoding/protojson"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

func ns(name string, labels, ann map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: ann}}
}

// app is a Deployment with one pod. containers are the pod's container names; the first is the app itself.
func app(namespace, name, image, podIP string, tmplLabels, tmplAnn map[string]string, containers ...string) []runtime.Object {
	if tmplLabels == nil {
		tmplLabels = map[string]string{}
	}
	tmplLabels["app"] = name
	if len(containers) == 0 {
		containers = []string{name}
	}
	var cs []corev1.Container
	for i, c := range containers {
		img := image
		if i > 0 {
			img = "proxy:1"
		}
		cs = append(cs, corev1.Container{Name: c, Image: img})
	}
	rs := name + "-rs"
	return []runtime.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: appsv1.DeploymentSpec{Replicas: i32(1), Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: tmplLabels, Annotations: tmplAnn},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: image}}}}},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: rs, Namespace: namespace, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: name}}}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-1", Namespace: namespace, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs}}},
			Spec:       corev1.PodSpec{NodeName: "n1", Containers: cs},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: podIP, PodIPs: []corev1.PodIP{{IP: podIP}}},
		},
	}
}

func cluster(objs ...[]runtime.Object) *fake.Clientset {
	all := []runtime.Object{
		&corev1.Node{ObjectMeta: metavNode("n1"), Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.5"}}}},
		ns("kube-system", nil, nil),
	}
	for _, o := range objs {
		all = append(all, o...)
	}
	return fake.NewSimpleClientset(all...)
}

func metavNode(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name} }

func begin(t *testing.T, cs *fake.Clientset, scope *Scope, fetch policyFetcher) *Collector {
	t.Helper()
	c := New(cs, 2, "10.0.0.5:6443")
	c.SetScope(scope)
	c.fetchPolicy = fetch
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return c
}

func wlByKey(s *continuumv1.Sync) map[string]*continuumv1.WorkloadFacts {
	m := map[string]*continuumv1.WorkloadFacts{}
	for _, w := range s.Workloads {
		m[w.Key] = w
	}
	return m
}

func nsNames(s *continuumv1.Sync) string {
	var n []string
	for _, x := range s.Namespaces {
		n = append(n, x.Name)
	}
	return strings.Join(n, ",")
}

func TestScopeMatchRules(t *testing.T) {
	mk := func(inc, exc, sel string) *Scope {
		s, err := ParseScope(inc, exc, sel)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct {
		name  string
		s     *Scope
		ns    string
		lbl   map[string]string
		match bool
	}{
		{"no scope sees everything", nil, "shop", nil, true},
		{"empty scope sees everything", mk("", "", ""), "shop", nil, true},
		{"include list", mk("shop,pay", "", ""), "shop", nil, true},
		{"not included", mk("shop,pay", "", ""), "dev", nil, false},
		{"exclude", mk("", "dev", ""), "dev", nil, false},
		{"exclude leaves the rest", mk("", "dev", ""), "shop", nil, true},
		{"exclude wins over include", mk("shop", "shop", ""), "shop", nil, false},
		{"selector matches", mk("", "", "continuum.io/observe=true"), "a", map[string]string{"continuum.io/observe": "true"}, true},
		{"selector misses", mk("", "", "continuum.io/observe=true"), "a", nil, false},
		{"include and selector are a union", mk("shop", "", "continuum.io/team=x"), "b", map[string]string{"continuum.io/team": "x"}, true},
		{"system namespaces are always read", mk("shop", "", ""), "kube-system", nil, true},
		{"a namespace can opt itself out", nil, "shop", map[string]string{OptOutLabel: "false"}, false},
		{"opt-out beats include", mk("shop", "", ""), "shop", map[string]string{OptOutLabel: "false"}, false},
	} {
		if got := tc.s.Match(tc.ns, tc.lbl); got != tc.match {
			t.Errorf("%s: Match = %v, want %v", tc.name, got, tc.match)
		}
	}
	if _, err := ParseScope("", "", "team=payments"); err == nil || !strings.Contains(err.Error(), "does not read") {
		t.Errorf("a selector on a label the agent never keeps must be refused, got %v", err)
	}
	if _, err := ParseScope("", "", "((("); err == nil {
		t.Error("a malformed selector must be refused")
	}
	if d := mk("shop,pay", "dev", "").Describe(); d != "only namespaces pay, shop, except dev" {
		t.Errorf("description: %q", d)
	}
	// What the server is told never names a namespace that is left out.
	if d := mk("shop", "dev,hr", "").Public(); d != "only namespaces shop, 2 namespaces left out by name" {
		t.Errorf("public description: %q", d)
	}
	if d := mk("", "hr-data", "").Public(); strings.Contains(d, "hr-data") || d != "1 namespace left out by name" {
		t.Errorf("public description of an exclusion leaks or is wrong: %q", d)
	}
}

func TestScopeKeepsOutOfScopeNamespacesInTheCluster(t *testing.T) {
	cs := cluster(
		[]runtime.Object{ns("shop", nil, nil), ns("payments", nil, nil), ns("dev", map[string]string{OptOutLabel: "false"}, nil)},
		app("shop", "cart", "cart:1", "10.42.0.10", nil, nil),
		app("payments", "ledger", "ledger:1", "10.42.0.20", nil, nil),
		app("dev", "scratch", "x:1", "10.42.0.30", nil, nil),
	)
	// Without a scope everything is visible, except a namespace that opted out.
	c := begin(t, cs, nil, nil)
	s := c.Snapshot()
	if got := nsNames(s); got != "dev,kube-system,payments,shop" && got != "kube-system,payments,shop" {
		t.Fatalf("namespaces without scope: %s", got)
	}
	if _, ok := wlByKey(s)["dev/Deployment/scratch"]; ok {
		t.Fatal("a namespace labelled continuum.io/observe=false must never be reported")
	}
	if s.Cluster.Scope == nil || s.Cluster.Scope.NamespacesTotal != 3 || s.Cluster.Scope.NamespacesInScope != 2 {
		t.Fatalf("scope facts with an opted-out namespace: %+v", s.Cluster.Scope)
	}

	// With a scope, the rest goes too, and nothing about it leaves.
	sc, _ := ParseScope("shop", "", "")
	c = begin(t, cs, sc, nil)
	s = c.Snapshot()
	if got := nsNames(s); got != "kube-system,shop" {
		t.Fatalf("namespaces in scope: %s", got)
	}
	w := wlByKey(s)
	if len(w) != 1 || w["shop/Deployment/cart"] == nil {
		t.Fatalf("workloads in scope: %v", w)
	}
	if f := s.Cluster.Scope; f == nil || f.NamespacesTotal != 3 || f.NamespacesInScope != 1 || f.Description != "only namespaces shop" {
		t.Fatalf("scope facts: %+v", f)
	}
	b, _ := protojson.Marshal(&continuumv1.Sync{Cluster: s.Cluster, Workloads: s.Workloads, Namespaces: s.Namespaces})
	for _, leak := range []string{"payments", "ledger", "scratch", "10.42.0.20"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("%q leaked into what is sent to the server", leak)
		}
	}
	// Traffic to and from an excluded namespace is dropped, not shown as an unknown address.
	ix := c.Index()
	if !ix.Hidden["10.42.0.20"] || !ix.Hidden["10.42.0.30"] || ix.Pods["10.42.0.10"] != "shop/Deployment/cart" || ix.Hidden["10.42.0.10"] {
		t.Fatalf("index: pods %v hidden %v", ix.Pods, ix.Hidden)
	}
}

func istiod() []runtime.Object {
	return app("istio-system", "istiod", "docker.io/istio/pilot:1.22.3", "10.42.0.2", map[string]string{"istio": "pilot"}, nil)
}

func TestMeshIstioSidecars(t *testing.T) {
	cs := cluster(
		[]runtime.Object{ns("istio-system", nil, nil), ns("meshed", map[string]string{"istio-injection": "enabled"}, nil), ns("plain", nil, nil), ns("strict-ns", map[string]string{"istio-injection": "enabled"}, nil)},
		istiod(),
		app("meshed", "web", "web:1", "10.42.0.11", nil, map[string]string{"traffic.sidecar.istio.io/excludeOutboundPorts": "5432, 6379"}, "web", "istio-proxy"),
		app("meshed", "legacy", "legacy:1", "10.42.0.12", nil, nil), // namespace is meshed, pod has no proxy
		app("meshed", "optout", "optout:1", "10.42.0.13", map[string]string{"sidecar.istio.io/inject": "false"}, nil),
		app("plain", "solo", "solo:1", "10.42.0.14", nil, nil),
	)
	pol := func(context.Context) ([]peerAuth, error) {
		return []peerAuth{{ns: "istio-system", mode: "strict"}, {ns: "meshed", mode: "permissive"}, {ns: "meshed", mode: "disabled", selector: true}, {ns: "strict-ns", mode: "strict"}}, nil
	}
	s := begin(t, cs, nil, pol).Snapshot()
	w := wlByKey(s)

	if m := w["meshed/Deployment/web"].Mesh; m == nil || m.Mesh != "istio" || m.Proxy != "sidecar" || m.Source != "pods" || strings.Join(m.ExcludedPorts, ",") != "out:5432,out:6379" {
		t.Errorf("web: %+v", m)
	}
	if m := w["meshed/Deployment/legacy"].Mesh; m == nil || m.Mesh != "istio" || m.Proxy != "" || m.Bypass || m.Source != "namespace" {
		t.Errorf("a pod without a proxy in a meshed namespace must say so: %+v", m)
	}
	if m := w["meshed/Deployment/optout"].Mesh; m == nil || !m.Bypass || m.Proxy != "" {
		t.Errorf("a workload that switched injection off: %+v", m)
	}
	if m := w["plain/Deployment/solo"].Mesh; m != nil {
		t.Errorf("a workload outside every mesh has no mesh facts: %+v", m)
	}
	if m := w["istio-system/Deployment/istiod"].Mesh; m == nil || !m.ControlPlane || m.Mesh != "istio" {
		t.Errorf("istiod: %+v", m)
	}
	mf := s.Cluster.Mesh
	if mf == nil || mf.Kind != "istio" || mf.Mode != "sidecar" || mf.Version != "1.22.3" || mf.Mtls != "strict" || !mf.PolicyRead {
		t.Fatalf("mesh facts: %+v", mf)
	}
	if len(mf.ControlPlane) != 1 || mf.ControlPlane[0] != "istio-system/Deployment/istiod" {
		t.Errorf("control plane: %v", mf.ControlPlane)
	}
	if mf.NamespaceMtls["meshed"] != "permissive" || len(mf.NamespaceMtls) != 1 {
		t.Errorf("a namespace policy that differs is reported, a selector-bound one and one equal to the mesh-wide mode are not: %v", mf.NamespaceMtls)
	}
	var mod *continuumv1.ModuleStatus
	for _, m := range s.Modules {
		if m.Name == ModMesh {
			mod = m
		}
	}
	if mod == nil || mod.State != continuumv1.ModuleStatus_OK {
		t.Errorf("mesh module: %+v", mod)
	}
}

func TestMeshIstioWithoutPolicyPermission(t *testing.T) {
	cs := cluster([]runtime.Object{ns("istio-system", nil, nil)}, istiod())
	forbidden := func(context.Context) ([]peerAuth, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Group: "security.istio.io", Resource: "peerauthentications"}, "", nil)
	}
	s := begin(t, cs, nil, forbidden).Snapshot()
	mf := s.Cluster.Mesh
	if mf == nil || mf.PolicyRead || mf.Mtls != "unknown" || !strings.Contains(mf.PolicyNote, "RBAC") {
		t.Fatalf("mesh facts: %+v", mf)
	}
	for _, m := range s.Modules {
		if m.Name == ModMesh && (m.State != continuumv1.ModuleStatus_SKIPPED || !strings.Contains(m.Reason, "RBAC")) {
			t.Errorf("mesh module should say why the policy is unread: %+v", m)
		}
	}
}

func TestMeshIstioAmbient(t *testing.T) {
	zt := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ztunnel", Namespace: "istio-system"}}
	cs := cluster(
		[]runtime.Object{ns("istio-system", nil, nil), ns("amb", map[string]string{"istio.io/dataplane-mode": "ambient"}, nil), zt},
		istiod(),
		app("amb", "api", "api:1", "10.42.0.15", nil, nil),
	)
	// the pod is captured by ztunnel: it says so in an annotation
	pod, _ := cs.CoreV1().Pods("amb").Get(context.Background(), "api-1", metav1.GetOptions{})
	pod.Annotations = map[string]string{"ambient.istio.io/redirection": "enabled"}
	cs.CoreV1().Pods("amb").Update(context.Background(), pod, metav1.UpdateOptions{})
	s := begin(t, cs, nil, func(context.Context) ([]peerAuth, error) { return nil, nil }).Snapshot()
	if m := wlByKey(s)["amb/Deployment/api"].Mesh; m == nil || m.Proxy != "ambient" || m.Source != "pods" {
		t.Errorf("ambient workload: %+v", m)
	}
	if mf := s.Cluster.Mesh; mf == nil || mf.Mode != "ambient" || mf.Mtls != "permissive" {
		t.Errorf("mesh facts: %+v", mf)
	}
	if m := wlByKey(s)["istio-system/DaemonSet/ztunnel"].Mesh; m == nil || !m.ControlPlane {
		t.Errorf("ztunnel is part of the mesh itself: %+v", m)
	}
}

func TestMeshLinkerd(t *testing.T) {
	cs := cluster(
		[]runtime.Object{ns("linkerd", nil, nil), ns("emojivoto", nil, map[string]string{"linkerd.io/inject": "enabled"})},
		app("linkerd", "linkerd-destination", "cr.l5d.io/linkerd/controller:stable-2.14.10", "10.42.0.3", nil, nil),
		app("emojivoto", "web", "web:1", "10.42.0.16", nil, map[string]string{"config.linkerd.io/skip-outbound-ports": "3306"}, "web", "linkerd-proxy"),
	)
	s := begin(t, cs, nil, nil).Snapshot()
	mf := s.Cluster.Mesh
	if mf == nil || mf.Kind != "linkerd" || mf.Version != "2.14.10" || mf.Mtls != "automatic" {
		t.Fatalf("mesh facts: %+v", mf)
	}
	if m := wlByKey(s)["emojivoto/Deployment/web"].Mesh; m == nil || m.Mesh != "linkerd" || m.Proxy != "sidecar" || strings.Join(m.ExcludedPorts, ",") != "out:3306" {
		t.Errorf("linkerd workload: %+v", m)
	}
}

func TestNoMeshMeansNoMeshFacts(t *testing.T) {
	c, _ := start(t, 2)
	s := c.Snapshot()
	if s.Cluster.Mesh != nil {
		t.Fatalf("no mesh in the cluster, got %+v", s.Cluster.Mesh)
	}
	for _, w := range s.Workloads {
		if w.Mesh != nil {
			t.Errorf("%s has mesh facts", w.Key)
		}
	}
	for _, m := range s.Modules {
		if m.Name == ModMesh {
			t.Error("a mesh module row appears only when a mesh was found")
		}
	}
}

// refreshPolicySafely exists because an earlier version recovered a panic once around watchPolicy's whole ticker
// goroutine: a single bad refresh then silently ended service-mesh policy collection for the rest of the
// process, with only a one-hour diagnostic to show for it. This is the regression guard: a panicking fetch must
// not stop a later, working one from updating the policy view.
func TestMeshPolicyRefreshSurvivesAPanicAndKeepsWorkingAfterwards(t *testing.T) {
	c := New(cluster(), 2, "10.0.0.5:6443")
	var panics []string
	c.OnPanic = func(task string, p any) { panics = append(panics, task) }
	ctx := context.Background()

	c.fetchPolicy = func(context.Context) ([]peerAuth, error) { panic("kaboom") }
	c.refreshPolicySafely(ctx) // must not panic the test
	if len(panics) != 1 || panics[0] != "service-mesh policy" {
		t.Fatalf("panics = %v, want one report naming the task", panics)
	}

	c.fetchPolicy = func(context.Context) ([]peerAuth, error) {
		return []peerAuth{{ns: "shop", mode: "strict"}}, nil
	}
	c.refreshPolicySafely(ctx)
	c.pol.mu.Lock()
	items, read := append([]peerAuth(nil), c.pol.items...), c.pol.read
	c.pol.mu.Unlock()
	if !read || len(items) != 1 || items[0].ns != "shop" || items[0].mode != "strict" {
		t.Fatalf("a working refresh right after a panicking one must still update the policy view, got read=%v items=%v", read, items)
	}
}

func TestMeshMarkersAreOnTheAllowList(t *testing.T) {
	in := map[string]string{"istio-injection": "enabled", "istio.io/rev": "1-22", "team": "secret-team", "istio.io/dataplane-mode": "ambient"}
	got := filterLabels(in)
	if got["istio-injection"] == "" || got["istio.io/rev"] == "" || got["istio.io/dataplane-mode"] == "" || got["team"] != "" {
		t.Errorf("labels: %v", got)
	}
	ann := filterAnnotations(map[string]string{"traffic.sidecar.istio.io/excludeOutboundPorts": "1", "linkerd.io/inject": "enabled", "config.linkerd.io/skip-inbound-ports": "9", "config.linkerd.io/proxy-cpu-limit": "1", "kubectl.kubernetes.io/last-applied-configuration": "{}"})
	if ann["traffic.sidecar.istio.io/excludeOutboundPorts"] == "" || ann["linkerd.io/inject"] == "" || ann["config.linkerd.io/skip-inbound-ports"] == "" || len(ann) != 3 {
		t.Errorf("annotations: %v", ann)
	}
}
