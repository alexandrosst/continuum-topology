package collect

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func attributionCluster() *fake.Clientset {
	sel := map[string]string{"app": "cart"}
	dep := func(name string, l map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: l}}}}
	}
	rs := func(name, dep string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: dep}}}}
	}
	pod := func(name, owner, ip string, hostNet bool, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: owner}}},
			Spec: corev1.PodSpec{NodeName: "n1", HostNetwork: hostNet}, Status: corev1.PodStatus{Phase: phase, PodIP: ip, PodIPs: []corev1.PodIP{{IP: ip}}}}
	}
	return fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "uid-1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "192.168.1.10"}, {Type: corev1.NodeExternalIP, Address: "203.0.113.10"}}}},
		dep("cart", sel), dep("db", map[string]string{"app": "db"}),
		rs("cart-1", "cart"), rs("db-1", "db"),
		pod("cart-1-a", "cart-1", "10.42.0.5", false, corev1.PodRunning),
		pod("cart-1-b", "cart-1", "10.42.0.6", false, corev1.PodRunning),
		pod("db-1-a", "db-1", "10.42.0.7", false, corev1.PodRunning),
		pod("cart-1-old", "cart-1", "10.42.0.99", false, corev1.PodFailed), // finished pods keep no address
		pod("cart-1-host", "cart-1", "192.168.1.10", true, corev1.PodRunning),
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"}, Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer, Selector: sel, ClusterIP: "10.43.0.20", ClusterIPs: []string{"10.43.0.20"},
			Ports: []corev1.ServicePort{{Port: 80, NodePort: 30080}}},
			Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "198.51.100.7"}, {Hostname: "elb.example.com"}}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "db"}, ClusterIP: "10.43.0.21", Ports: []corev1.ServicePort{{Port: 5432}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: "shop"}, Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "db"}, ClusterIP: corev1.ClusterIPNone}},
	)
}

func TestIndexAttributesAddressesToWorkloads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := New(attributionCluster(), 2, "h")
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ix := c.Index()
	for ip, want := range map[string]string{"10.42.0.5": "shop/Deployment/cart", "10.42.0.6": "shop/Deployment/cart", "10.42.0.7": "shop/Deployment/db"} {
		if ix.Pods[ip] != want {
			t.Errorf("pod %s -> %q, want %q", ip, ix.Pods[ip], want)
		}
	}
	if _, ok := ix.Pods["10.42.0.99"]; ok {
		t.Error("a finished pod must not own an address")
	}
	if _, ok := ix.Pods["192.168.1.10"]; ok {
		t.Error("a host-network pod shares its node's address and must not be attributed to a workload")
	}
	if ix.Nodes["192.168.1.10"] != "n1" || ix.Nodes["203.0.113.10"] != "n1" {
		t.Errorf("node addresses = %v", ix.Nodes)
	}
	if got := ix.Services["10.43.0.20"]; len(got) != 1 || got[0] != "shop/Deployment/cart" {
		t.Errorf("cluster IP of cart = %v", got)
	}
	if got := ix.Services["10.43.0.21"]; len(got) != 1 || got[0] != "shop/Deployment/db" {
		t.Errorf("cluster IP of db = %v", got)
	}
	if _, ok := ix.Services["None"]; ok {
		t.Error("a headless Service has no cluster IP to attribute")
	}
}

func TestReachableAddressesAndNodeExternalIPs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := New(attributionCluster(), 2, "h")
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	var cart, db = snap.Workloads[0], snap.Workloads[1]
	if cart.Name != "cart" {
		cart, db = db, cart
	}
	got := map[string]bool{}
	for _, a := range cart.Reachable {
		got[a.Kind+"|"+a.Ip] = true
		if a.Kind == "load-balancer" && (a.Ip != "198.51.100.7" || a.Port != 80) {
			t.Errorf("load balancer address = %v", a)
		}
	}
	if !got["load-balancer|198.51.100.7"] || !got["node-port|"] || len(cart.Reachable) != 2 {
		t.Errorf("cart reachable = %v (a host-name-only load balancer is not listed)", cart.Reachable)
	}
	if len(db.Reachable) != 0 {
		t.Errorf("a ClusterIP Service is not reachable from outside: %v", db.Reachable)
	}
	if len(snap.Nodes[0].ExternalIps) != 1 || snap.Nodes[0].ExternalIps[0] != "203.0.113.10" {
		t.Errorf("node external IPs = %v", snap.Nodes[0].ExternalIps)
	}
}
