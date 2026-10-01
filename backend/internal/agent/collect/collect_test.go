package collect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func i32(v int32) *int32 { return &v }

func fixture() *fake.Clientset {
	q := func(s string) resource.Quantity { return resource.MustParse(s) }
	labels := map[string]string{"app": "cart"}
	secretEnv := []corev1.EnvVar{{Name: "DB_PASSWORD", Value: "hunter2-super-secret"}}
	return fake.NewSimpleClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "edge-1", UID: "u1", CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
				Labels:      map[string]string{"node-role.kubernetes.io/control-plane": "", "node.kubernetes.io/instance-type": "k3s", "internal/secretish": "x", "kubernetes.io/arch": "arm64"},
				Annotations: map[string]string{"flannel.alpha.coreos.com/backend-type": "vxlan", "k3s.io/node-args": "[\"--token\",\"abc123\"]"}},
			Spec: corev1.NodeSpec{ProviderID: "k3s://edge-1", PodCIDRs: []string{"10.42.0.0/24"}, Taints: []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}},
			Status: corev1.NodeStatus{
				Capacity:    corev1.ResourceList{"cpu": q("4"), "memory": q("8Gi"), "pods": q("110"), "nvidia.com/gpu": q("1"), "hugepages-2Mi": q("0"), "ephemeral-storage": q("32Gi")},
				Allocatable: corev1.ResourceList{"cpu": q("3900m"), "memory": q("7Gi"), "ephemeral-storage": q("28Gi")},
				Addresses:   []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.5"}, {Type: corev1.NodeHostName, Address: "edge-1"}},
				NodeInfo:    corev1.NodeSystemInfo{Architecture: "arm64", OSImage: "Raspbian 12", KernelVersion: "6.6.31+rpt-rpi-2712", KubeletVersion: "v1.30.5+k3s1", MachineID: "m1"},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}, {Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue}, {Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse}},
				Images:      []corev1.ContainerImage{{Names: []string{"registry.example.com/private/app:1"}}},
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "x"}}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop", Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm", "internal/x": "y"},
				Annotations: map[string]string{"meta.helm.sh/release-name": "shop", "kubectl.kubernetes.io/last-applied-configuration": "{\"env\":\"PASSWORD=hunter2\"}", "note": "https://user:tok@x"}},
			Spec: appsv1.DeploymentSpec{Replicas: i32(2), Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "cart", Image: "ghcr.io/acme/cart:1.4", Env: secretEnv, Command: []string{"run", "--api-key=abc"}, Args: []string{"--password=p"},
					Ports:     []corev1.ContainerPort{{ContainerPort: 8080}},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"cpu": q("100m"), "memory": q("256Mi")}, Limits: corev1.ResourceList{"memory": q("512Mi")}},
				}}, NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"}, Tolerations: []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}}}},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "cart-abc", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "cart"}}}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "cart-abc-1", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "cart-abc"}}},
			Spec: corev1.PodSpec{NodeName: "edge-1", Containers: []corev1.Container{{Name: "cart", Env: secretEnv, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"cpu": q("100m"), "memory": q("256Mi")}}}},
				Volumes: []corev1.Volume{
					{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "cart-data"}}},
					{Name: "creds", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "hunter2-vol-secret"}}},
				}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "cart", RestartCount: 3, ImageID: "ghcr.io/acme/cart@sha256:deadbeef",
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}}}},
		},
		&corev1.Pod{ // finished pods hold no resources
			ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: "shop"},
			Spec:       corev1.PodSpec{NodeName: "edge-1", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"cpu": q("2")}}}}},
			Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
		},
		&corev1.Pod{ // waiting to be scheduled: no node assigned yet
			ObjectMeta: metav1.ObjectMeta{Name: "cart-abc-2", Namespace: "shop"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "cart"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Selector: labels, Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "headless-no-selector", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "shop"}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "shop.example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "cart", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}}}}}}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "cart-data", Namespace: "shop"},
			Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: func() *string { s := "local-path"; return &s }(), VolumeName: "pv-1", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: q("10Gi")}}},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "nfs.secret-host", Path: "/exports/x"}},
				NodeAffinity:           &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"edge-1"}}}}}}}}},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "cart"}, MinReplicas: i32(2), MaxReplicas: 6,
				Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: "cpu", Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: i32(80)}}}}},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 2}},
		&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"},
			Spec:   policyv1.PodDisruptionBudgetSpec{MinAvailable: func() *intstr.IntOrString { v := intstr.FromString("50%"); return &v }(), Selector: &metav1.LabelSelector{MatchLabels: labels}},
			Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Data: map[string][]byte{"password": []byte("hunter2")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "shop"}, Data: map[string]string{"k": "v"}},
	)
}

// secondNamespaceFixture is fixture() plus an independent "payments" namespace with its own workload, service and
// PVC, entirely separate from "shop" - used to check that rbac.mode=namespaced (scope.namespaces) really does keep
// a whole namespace out, not merely filter it after reading it.
func secondNamespaceFixture() *fake.Clientset {
	cs := fixture()
	q := resource.MustParse
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "ledger", Namespace: "payments"},
			Spec: appsv1.DeploymentSpec{Replicas: i32(1), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "ledger", Image: "ghcr.io/acme/ledger:1"}},
			}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "ledger", Namespace: "payments"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "ledger"}, Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "ledger-data", Namespace: "payments"},
			Spec:   corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: q("1Gi")}}},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
	}
	for _, o := range objs {
		if err := cs.Tracker().Add(o); err != nil {
			panic(err)
		}
	}
	return cs
}

// startNamespaced is start, but for rbac.mode=namespaced: tier 2's RBAC is a Role per namespace in nss instead of
// one cluster-wide ClusterRole.
func startNamespaced(t *testing.T, cs *fake.Clientset, nss ...string) *Collector {
	t.Helper()
	c := New(cs, 2, "10.0.0.5:6443")
	c.SetScope(&Scope{Include: nss})
	c.SetNamespacedRBAC(true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return c
}

// forbidInNamespace refuses the verb on resource, but only for objects in ns, so a Role missing or misapplied in
// just one namespace can be simulated without affecting any other.
func forbidInNamespace(cs *fake.Clientset, verb, resource, ns string) {
	cs.PrependReactor(verb, resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != ns {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("no"))
	})
}

func start(t *testing.T, tier int) (*Collector, *fake.Clientset) {
	t.Helper()
	cs := fixture()
	c := New(cs, tier, "10.0.0.5:6443")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return c, cs
}

func TestSnapshotTier2(t *testing.T) {
	c, _ := start(t, 2)
	s := c.Snapshot()
	if !s.Full || len(s.Nodes) != 1 || len(s.Namespaces) != 1 || len(s.Workloads) != 1 {
		t.Fatalf("snapshot = %d nodes %d ns %d workloads", len(s.Nodes), len(s.Namespaces), len(s.Workloads))
	}
	n := s.Nodes[0]
	if n.Name != "edge-1" || n.Architecture != "arm64" || n.CpuCapacityMillis != 4000 || n.MemoryCapacityBytes != 8<<30 || !n.Ready ||
		n.ProviderId != "k3s://edge-1" || n.InternalIps[0] != "10.0.0.5" || n.PodCidrs[0] != "10.42.0.0/24" {
		t.Errorf("node = %v", n)
	}
	if n.CpuRequestedMillis != 100 || n.MemoryRequestedBytes != 256<<20 {
		t.Errorf("requested = %d m / %d B (the finished job must not count)", n.CpuRequestedMillis, n.MemoryRequestedBytes)
	}
	if len(n.ProblemConditions) != 1 || n.ProblemConditions[0] != "DiskPressure" || n.Taints[0] != "dedicated=gpu:NoSchedule" {
		t.Errorf("conditions/taints = %v %v", n.ProblemConditions, n.Taints)
	}
	if n.EphemeralStorageCapacityBytes != 32<<30 || n.EphemeralStorageAllocatableBytes != 28<<30 {
		t.Errorf("ephemeral storage = %d / %d B, want 32Gi / 28Gi", n.EphemeralStorageCapacityBytes, n.EphemeralStorageAllocatableBytes)
	}
	if n.ExtendedResources["nvidia.com/gpu"] != 1 || len(n.ExtendedResources) != 1 {
		t.Errorf("extended = %v", n.ExtendedResources)
	}
	if _, ok := n.Annotations["flannel.alpha.coreos.com/backend-type"]; !ok {
		t.Error("allow-listed annotation dropped")
	}

	w := s.Workloads[0]
	if w.Key != "shop/Deployment/cart" || w.Replicas != 2 || w.ReadyReplicas != 1 || w.Restarts != 3 || w.OomKills != 1 {
		t.Errorf("workload = %v", w)
	}
	// Exactly the one real, owned pod (cart-abc-1) - job-1 belongs to no workload at all and cart-abc-2 has
	// no owner reference despite its name, so neither leaks in here even though both exist in the fixture.
	if len(w.Pods) != 1 {
		t.Fatalf("pods = %d, want 1", len(w.Pods))
	}
	if p := w.Pods[0]; p.Name != "cart-abc-1" || p.NodeName != "edge-1" || p.Phase != "Running" || p.Ready || p.Restarts != 3 {
		t.Errorf("pod = %v (no PodReady condition is set in the fixture, so Ready must be false, not guessed true)", p)
	}
	if w.Images[0].Image != "ghcr.io/acme/cart:1.4" || w.Images[0].Digest != "sha256:deadbeef" {
		t.Errorf("images = %v", w.Images)
	}
	if w.CpuRequestMillis != 100 || w.MemoryRequestBytes != 256<<20 || w.MemoryLimitBytes != 512<<20 {
		t.Errorf("resources = %v", w)
	}
	if len(w.NodeNames) != 1 || w.NodeNames[0] != "edge-1" {
		t.Errorf("placement = %v", w.NodeNames)
	}
	if w.Exposure != "ingress" || len(w.Hosts) != 1 || w.Hosts[0] != "shop.example.com" {
		t.Errorf("exposure = %s hosts = %v", w.Exposure, w.Hosts)
	}
	if len(w.Ports) != 2 || w.Ports[0] != 80 || w.Ports[1] != 8080 {
		t.Errorf("ports = %v", w.Ports)
	}
	if w.Tolerations[0] != "dedicated=gpu:NoSchedule" || w.NodeSelector["kubernetes.io/arch"] != "arm64" {
		t.Errorf("constraints = %v %v", w.Tolerations, w.NodeSelector)
	}
	if s.Cluster.ApiHost != "10.0.0.5:6443" {
		t.Errorf("cluster = %v", s.Cluster)
	}
	if s.Cluster.PendingPodCount == nil || *s.Cluster.PendingPodCount != 1 {
		t.Errorf("pending pod count = %v, want 1 (the two placed pods and the finished job must not count)", s.Cluster.PendingPodCount)
	}

	// pod count vs the kubelet's maximum, and age
	if n.PodCapacity != 110 || n.PodCount == nil || *n.PodCount != 1 || n.CreatedAt.AsTime().Year() != 2026 {
		t.Errorf("pods/age = cap %d count %v created %v (the finished job must not be counted)", n.PodCapacity, n.PodCount, n.CreatedAt)
	}
	// storage: only the PVC is reported (not the Secret volume), with the node the local volume is stuck on
	if len(w.VolumeClaims) != 1 {
		t.Fatalf("volume claims = %v", w.VolumeClaims)
	}
	if v := w.VolumeClaims[0]; v.Name != "cart-data" || v.StorageClass != "local-path" || v.RequestedBytes != 10<<30 || v.Phase != "Bound" || len(v.PinnedNodes) != 1 || v.PinnedNodes[0] != "edge-1" {
		t.Errorf("claim = %v", v)
	}
	if a := w.Autoscaler; a == nil || a.MinReplicas != 2 || a.MaxReplicas != 6 || a.CurrentReplicas != 2 || len(a.Targets) != 1 || a.Targets[0] != "cpu 80%" {
		t.Errorf("autoscaler = %v", a)
	}
	if d := w.Disruption; d == nil || d.MinAvailable != "50%" || d.DisruptionsAllowed != 1 {
		t.Errorf("disruption = %v", d)
	}
	for _, m := range s.Modules {
		if (m.Name == ModStorage || m.Name == ModScaling) && m.State.String() != "OK" {
			t.Errorf("module %s = %v", m.Name, m)
		}
	}
}

// Several pods on one workload, mixed ready/not-ready and with a readable PodReady condition this time
// (TestSnapshotTier2's single pod has none set at all, so it only ever pins the Ready=false path) - also
// pins the pod list's sort order (by name, independent of scan order) and that each pod's own restart
// count is its own containers' sum, not the workload-wide total.
func TestSnapshotPodsCoverMultipleReplicasSortedByName(t *testing.T) {
	q := func(s string) resource.Quantity { return resource.MustParse(s) }
	cs := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "edge-1"}, Status: corev1.NodeStatus{Capacity: corev1.ResourceList{"cpu": q("4"), "memory": q("8Gi")}}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "collector", Namespace: "obs"},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "collector"}}}}}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "collector-z", Namespace: "obs", OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "collector"}}},
			Spec:       corev1.PodSpec{NodeName: "edge-1", Containers: []corev1.Container{{Name: "collector"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "collector", RestartCount: 1}}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "collector-a", Namespace: "obs", OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "collector"}}},
			Spec:       corev1.PodSpec{NodeName: "edge-1", Containers: []corev1.Container{{Name: "collector"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "collector", RestartCount: 4}}},
		},
	)
	c := New(cs, 2, "10.0.0.5:6443")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if len(s.Workloads) != 1 {
		t.Fatalf("workloads = %d, want 1", len(s.Workloads))
	}
	w := s.Workloads[0]
	if len(w.Pods) != 2 {
		t.Fatalf("pods = %d, want 2", len(w.Pods))
	}
	// Sorted by name, not by whatever order the fake clientset happened to list them in (collector-z was
	// created before collector-a above).
	if w.Pods[0].Name != "collector-a" || w.Pods[1].Name != "collector-z" {
		t.Errorf("pod order = %s, %s - want collector-a before collector-z", w.Pods[0].Name, w.Pods[1].Name)
	}
	if w.Pods[0].Ready || w.Pods[0].Restarts != 4 {
		t.Errorf("collector-a = ready %v restarts %d, want not-ready, 4 restarts", w.Pods[0].Ready, w.Pods[0].Restarts)
	}
	if !w.Pods[1].Ready || w.Pods[1].Restarts != 1 {
		t.Errorf("collector-z = ready %v restarts %d, want ready, 1 restart", w.Pods[1].Ready, w.Pods[1].Restarts)
	}
	// The workload-wide total is still the sum across both pods, same as before this field existed.
	if w.Restarts != 5 {
		t.Errorf("workload restarts = %d, want 5 (1 + 4)", w.Restarts)
	}
}

// A workload with an unrealistic number of replicas (a runaway ReplicaSet, or a DaemonSet on a very large
// cluster) must not make WorkloadFacts.Pods grow without bound - see maxPodsPerWorkload in snapshot.go.
func TestSnapshotPodsAreCappedPerWorkload(t *testing.T) {
	const total = maxPodsPerWorkload + 10
	objs := []runtime.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}, Status: corev1.NodeStatus{Capacity: corev1.ResourceList{"cpu": resource.MustParse("4"), "memory": resource.MustParse("8Gi")}}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "big", Namespace: "obs"},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}}}},
	}
	for i := 0; i < total; i++ {
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("big-%04d", i), Namespace: "obs", OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "big"}}},
			Spec:       corev1.PodSpec{NodeName: "n", Containers: []corev1.Container{{Name: "c"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		})
	}
	cs := fake.NewSimpleClientset(objs...)
	c := New(cs, 2, "x")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if len(s.Workloads) != 1 {
		t.Fatalf("workloads = %d, want 1", len(s.Workloads))
	}
	w := s.Workloads[0]
	if len(w.Pods) != maxPodsPerWorkload {
		t.Fatalf("pods = %d, want capped at %d (of %d actually created)", len(w.Pods), maxPodsPerWorkload, total)
	}
	// The cap keeps the lexicographically-first maxPodsPerWorkload names (post-sort truncation), not an
	// arbitrary or lister-order-dependent subset - deterministic across polls.
	if w.Pods[0].Name != "big-0000" || w.Pods[len(w.Pods)-1].Name != fmt.Sprintf("big-%04d", maxPodsPerWorkload-1) {
		t.Errorf("pod window = %s..%s, want big-0000..big-%04d", w.Pods[0].Name, w.Pods[len(w.Pods)-1].Name, maxPodsPerWorkload-1)
	}
}

// A cluster that refuses the optional reads (older RBAC, missing API) must still work: the extras
// switch off with a stated reason and everything else is reported as before.
func TestOptionalModulesDegrade(t *testing.T) {
	cs := fixture()
	forbid := func(resource string) {
		cs.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("no"))
		})
	}
	forbid("persistentvolumeclaims")
	forbid("horizontalpodautoscalers")
	c := New(cs, 2, "x")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	w := s.Workloads[0]
	if len(w.VolumeClaims) != 0 || w.Autoscaler != nil || w.Disruption != nil {
		t.Errorf("extras should be off: %v", w)
	}
	if w.Exposure != "ingress" || len(s.Nodes) != 1 {
		t.Errorf("the rest must be unaffected: %v", w)
	}
	got := map[string]string{}
	for _, m := range c.Modules() {
		got[m.Name] = m.State.String() + ":" + m.Reason
	}
	if got[ModStorage] != "SKIPPED:not permitted by installed RBAC" || got[ModScaling] != "SKIPPED:not permitted by installed RBAC" || got[ModServices] != "OK:" {
		t.Errorf("modules = %v", got)
	}
}

// Nothing the allow-list does not name may reach the wire; this is checked on the marshalled
// bytes so it holds no matter which field a value would have travelled in.
func TestNoSecretMaterialInTheSnapshot(t *testing.T) {
	c, _ := start(t, 2)
	s := c.Snapshot()
	wire := s.String()
	for _, leak := range []string{"hunter2", "DB_PASSWORD", "api-key", "--password", "abc123", "--token", "user:tok", "last-applied", "private/app", "internal/secretish", "internal/x", "kubectl.kubernetes.io", "k3s.io/node-args", "team", "hunter2-vol-secret", "nfs.secret-host", "/exports/x"} {
		if strings.Contains(wire, leak) {
			t.Errorf("%q leaked into the snapshot", leak)
		}
	}
}

func TestNeverTouchesSecretsOrConfigMaps(t *testing.T) {
	_, cs := start(t, 2)
	for _, a := range cs.Actions() {
		switch a.GetResource().Resource {
		case "secrets", "configmaps", "serviceaccounts", "roles", "clusterroles":
			t.Errorf("agent called %s on %s", a.GetVerb(), a.GetResource().Resource)
		}
		if a.GetVerb() != "list" && a.GetVerb() != "watch" && a.GetVerb() != "get" {
			t.Errorf("agent used the non-read verb %q on %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}

func TestTier1ReadsOnlyInfrastructure(t *testing.T) {
	c, cs := start(t, 1)
	s := c.Snapshot()
	if len(s.Nodes) != 1 || len(s.Workloads) != 0 || len(s.Namespaces) != 0 {
		t.Fatalf("tier 1 snapshot = %d nodes %d ns %d workloads", len(s.Nodes), len(s.Namespaces), len(s.Workloads))
	}
	if s.Nodes[0].CpuRequestedMillis != 0 || s.Nodes[0].PodCount != nil {
		t.Error("requested resources and pod count need pods, which tier 1 does not read: they must be unknown, not zero")
	}
	for _, a := range cs.Actions() {
		switch a.GetResource().Resource {
		case "pods", "deployments", "services", "namespaces", "replicasets", "statefulsets", "daemonsets", "ingresses", "persistentvolumeclaims", "persistentvolumes", "horizontalpodautoscalers", "poddisruptionbudgets":
			t.Errorf("tier 1 touched %s", a.GetResource().Resource)
		}
	}
	mods := c.Modules()
	if mods[0].Name != ModInfrastructure || mods[0].State.String() != "OK" || mods[1].State.String() != "SKIPPED" || !strings.Contains(mods[1].Reason, "tier 2") {
		t.Errorf("modules = %v", mods)
	}
}

func TestChangesAreSignalledAndSnapshotFollows(t *testing.T) {
	c, cs := start(t, 2)
	select {
	case <-c.Changes(): // initial sync events
	default:
	}
	for len(c.Changes()) > 0 {
		<-c.Changes()
	}
	_, err := cs.AppsV1().Deployments("shop").Create(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Replicas: i32(1)}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Changes():
	case <-time.After(5 * time.Second):
		t.Fatal("no change signal")
	}
	if len(c.Snapshot().Workloads) != 2 {
		t.Error("snapshot did not pick up the new workload")
	}
	_ = k8stesting.ObjectReaction
	_ = intstr.FromInt
}

func TestStripDropsCredentialCarryingFields(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Env: []corev1.EnvVar{{Name: "A", Value: "b"}}, EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{}}},
			Command: []string{"x"}, Args: []string{"y"}, VolumeMounts: []corev1.VolumeMount{{Name: "v"}}}},
		Volumes: []corev1.Volume{{Name: "v"}}, ServiceAccountName: "sa"}}}}
	out, _ := stripDeployment(d)
	ct := out.(*appsv1.Deployment).Spec.Template.Spec
	if len(ct.Containers[0].Env) != 0 || len(ct.Containers[0].EnvFrom) != 0 || len(ct.Containers[0].Command) != 0 || len(ct.Containers[0].Args) != 0 ||
		len(ct.Containers[0].VolumeMounts) != 0 || len(ct.Volumes) != 0 || ct.ServiceAccountName != "" {
		t.Errorf("stripped deployment still carries %+v", ct)
	}
}

// stripPod keeps only the one PodReady condition's Status, same discipline as the OOMKilled-only
// LastTerminationState copy right next to it - never LastTransitionTime, Reason, Message, or any other
// condition type (PodScheduled, ContainersReady, ...), which say far more about a pod's own history than
// a single ready/not-ready bit needs.
func TestStripPodKeepsOnlyPodReadyCondition(t *testing.T) {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			{Type: corev1.PodReady, Status: corev1.ConditionTrue, Reason: "secret-reason", Message: "secret message with detail", LastTransitionTime: metav1.Now()},
		}},
	}
	out, _ := stripPod(p)
	cs := out.(*corev1.Pod).Status.Conditions
	if len(cs) != 1 || cs[0].Type != corev1.PodReady || cs[0].Status != corev1.ConditionTrue || cs[0].Reason != "" || cs[0].Message != "" || !cs[0].LastTransitionTime.IsZero() {
		t.Errorf("stripped conditions = %+v, want only a bare PodReady/True, nothing else carried", cs)
	}
}

func TestSanitizeImageRemovesEmbeddedCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"nginx:1.27":                            "nginx:1.27",
		"registry.io/team/app:2":                "registry.io/team/app:2",
		"nginx@sha256:abcdef":                   "nginx@sha256:abcdef",
		"registry.io/app@sha256:abcdef":         "registry.io/app@sha256:abcdef",
		"deploy:hunter2@registry.io/team/app:1": "registry.io/team/app:1",
		"https://bot:tok@registry.io/app:1":     "registry.io/app:1",
	} {
		if got := sanitizeImage(in); got != want {
			t.Errorf("sanitizeImage(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeImage(strings.Repeat("a", 2000)); len(got) != 512 {
		t.Errorf("length = %d", len(got))
	}
}

// ---- rbac.mode=namespaced: tier 2 granted with a Role per namespace instead of one cluster-wide ClusterRole ----

// A namespace left out of scope.namespaces must never be read at all, not merely filtered out after the fact: the
// whole point of this mode is that the ServiceAccount is never given permission to list it in the first place. The
// fake clientset does not enforce RBAC, so this checks what was actually asked for (cs.Actions()), not just what
// came back.
func TestNamespacedRBACOnlyWatchesNamedNamespaces(t *testing.T) {
	cs := secondNamespaceFixture()
	c := startNamespaced(t, cs, "shop")
	s := c.Snapshot()
	if len(s.Workloads) != 1 || s.Workloads[0].Namespace != "shop" {
		t.Fatalf("workloads = %v (payments must not appear at all)", s.Workloads)
	}
	for _, a := range cs.Actions() {
		if ns := a.GetNamespace(); ns == "payments" {
			t.Errorf("agent asked the cluster about the out-of-scope namespace payments: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
		if a.GetResource().Resource == "namespaces" {
			t.Errorf("agent listed/watched Namespaces directly: no Role, in any namespace, can grant that")
		}
	}
	got := map[string]string{}
	for _, m := range c.Modules() {
		got[m.Name] = m.State.String()
	}
	if got[ModServices] != "OK" || got[ModStorage] != "OK" {
		t.Errorf("modules = %v", got)
	}
}

// Namespace metadata and persistent volumes are cluster-scoped: no Role, in any namespace, can grant either, so
// this mode never reads them (not "reads and hides", genuinely never asks). Namespace facts are absent rather than
// empty, and a claim's PinnedNodes (which come from the PersistentVolume, not the claim) stay unset even though the
// claim itself is read fine.
func TestNamespacedRBACNeverReadsNamespacesOrVolumes(t *testing.T) {
	c, _ := start(t, 2) // cluster mode, for a baseline: the fixture's PV does pin cart-data to edge-1
	base := c.Snapshot()
	if len(base.Namespaces) != 1 || len(base.Workloads[0].VolumeClaims[0].PinnedNodes) != 1 {
		t.Fatalf("cluster-mode baseline should see the namespace and the PV pin: %+v", base.Workloads[0].VolumeClaims)
	}

	nsCS := fixture()
	nc := startNamespaced(t, nsCS, "shop")
	s := nc.Snapshot()
	if len(s.Namespaces) != 0 {
		t.Errorf("namespaces = %v, want none: Namespace objects are cluster-scoped and no Role can grant them", s.Namespaces)
	}
	if s.Cluster.Scope == nil || s.Cluster.Scope.Description == "" {
		t.Errorf("the scope rule itself should still be reported: %v", s.Cluster.Scope)
	}
	if len(s.Workloads) != 1 || len(s.Workloads[0].VolumeClaims) != 1 {
		t.Fatalf("the claim itself is namespaced and should still be read: %v", s.Workloads)
	}
	if len(s.Workloads[0].VolumeClaims[0].PinnedNodes) != 0 {
		t.Errorf("PinnedNodes = %v, want none: that comes from the PersistentVolume, which is never read in this mode", s.Workloads[0].VolumeClaims[0].PinnedNodes)
	}
	for _, a := range nsCS.Actions() {
		if a.GetResource().Resource == "persistentvolumes" {
			t.Errorf("agent listed/watched PersistentVolumes: no Role, in any namespace, can grant that")
		}
	}
}

// A Role missing or refused in just one namespace must be reported (and later recovered from) on its own, not
// mistaken for every namespace: the collector runs one real informer per namespace, not one informer scoped
// however this mode happens to be implemented.
func TestNamespacedRBACIsolatesAPerNamespaceFailure(t *testing.T) {
	cs := secondNamespaceFixture()
	forbidInNamespace(cs, "list", "pods", "payments")
	c := startNamespaced(t, cs, "shop", "payments")
	s := c.Snapshot()
	// The Deployment informer for payments is unaffected (only its pods list was forbidden), so its workload still
	// appears - built, like any workload, from the Deployment object - but without any of the pod-derived facts
	// (ready replicas, restarts, node placement) that only a working pods watch can supply.
	var shop, payments *continuumv1.WorkloadFacts
	for _, w := range s.Workloads {
		switch w.Namespace {
		case "shop":
			shop = w
		case "payments":
			payments = w
		}
	}
	if shop == nil || shop.ReadyReplicas != 1 {
		t.Errorf("shop should be entirely unaffected by payments' forbidden pods: %v", shop)
	}
	if payments == nil || payments.ReadyReplicas != 0 || len(payments.NodeNames) != 0 {
		t.Errorf("payments should show no pod-derived facts, its pods watch being forbidden: %v", payments)
	}
	got := map[string]string{}
	for _, m := range c.Modules() {
		got[m.Name] = m.State.String()
	}
	if got[ModServices] != "ERROR" {
		t.Errorf("modules = %v, want services in error (its pods watch is forbidden in payments)", got)
	}
	var shopOK, paymentsForbidden bool
	for _, i := range c.Informers() {
		if i.Name == "pods (namespace shop)" && i.Synced && i.Err == "" {
			shopOK = true
		}
		if i.Name == "pods (namespace payments)" && i.Forbidden {
			paymentsForbidden = true
		}
	}
	if !shopOK || !paymentsForbidden {
		t.Errorf("informers = %+v", c.Informers())
	}
}

// SetNamespacedRBAC(true) with nothing in scope.Include cannot mean anything (it cannot discover namespaces on its
// own - that needs exactly the cluster-wide list permission this mode exists to avoid), so Start refuses outright
// rather than silently watching nothing.
func TestNamespacedRBACRequiresNamespaces(t *testing.T) {
	c := New(fixture(), 2, "x")
	c.SetNamespacedRBAC(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(ctx); err == nil || !strings.Contains(err.Error(), "scope.Include") && !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("Start() error = %v, want a clear refusal", err)
	}
}
