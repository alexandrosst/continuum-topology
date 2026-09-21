package collect

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// slim keeps identity plus the allow-listed labels and annotations. Managed fields,
// last-applied-configuration and every other annotation are dropped here, in memory,
// as soon as the object arrives.
func slim(m metav1.ObjectMeta) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: m.Name, Namespace: m.Namespace, UID: m.UID,
		Labels: filterLabels(m.Labels), Annotations: filterAnnotations(m.Annotations),
		OwnerReferences: m.OwnerReferences, DeletionTimestamp: m.DeletionTimestamp, CreationTimestamp: m.CreationTimestamp,
	}
}

// slimContainers keeps image, ports and resources. Env, EnvFrom, Command, Args, volume
// mounts and probes are deliberately not copied: those are where credentials end up.
func slimContainers(in []corev1.Container) []corev1.Container {
	out := make([]corev1.Container, len(in))
	for i, c := range in {
		out[i] = corev1.Container{Name: c.Name, Image: c.Image, Ports: c.Ports, Resources: c.Resources}
	}
	return out
}

// slimTemplate keeps the raw pod-template labels (needed to join Services to workloads,
// never sent onwards) and the scheduling constraints.
func slimTemplate(t corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: t.Labels, Annotations: filterAnnotations(t.Annotations)},
		Spec: corev1.PodSpec{
			Containers: slimContainers(t.Spec.Containers), NodeSelector: t.Spec.NodeSelector, Tolerations: t.Spec.Tolerations,
		},
	}
}

func stripNode(o any) (any, error) {
	n, ok := o.(*corev1.Node)
	if !ok {
		return o, nil
	}
	return &corev1.Node{
		ObjectMeta: slim(n.ObjectMeta),
		Spec:       corev1.NodeSpec{ProviderID: n.Spec.ProviderID, PodCIDRs: n.Spec.PodCIDRs, Taints: n.Spec.Taints},
		Status: corev1.NodeStatus{
			Capacity: n.Status.Capacity, Allocatable: n.Status.Allocatable, Conditions: n.Status.Conditions,
			Addresses: n.Status.Addresses, NodeInfo: n.Status.NodeInfo, // Images, VolumesInUse etc. dropped
		},
	}, nil
}

func stripNamespace(o any) (any, error) {
	n, ok := o.(*corev1.Namespace)
	if !ok {
		return o, nil
	}
	return &corev1.Namespace{ObjectMeta: slim(n.ObjectMeta)}, nil
}

func stripStorageClass(o any) (any, error) {
	s, ok := o.(*storagev1.StorageClass)
	if !ok {
		return o, nil
	}
	return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: s.Name}}, nil
}

func stripIngressClass(o any) (any, error) {
	s, ok := o.(*networkingv1.IngressClass)
	if !ok {
		return o, nil
	}
	return &networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: s.Name}}, nil
}

func stripPod(o any) (any, error) {
	p, ok := o.(*corev1.Pod)
	if !ok {
		return o, nil
	}
	cs := make([]corev1.ContainerStatus, len(p.Status.ContainerStatuses))
	for i, s := range p.Status.ContainerStatuses {
		cs[i] = corev1.ContainerStatus{Name: s.Name, RestartCount: s.RestartCount, ImageID: s.ImageID, Ready: s.Ready}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace, UID: p.UID, OwnerReferences: p.OwnerReferences, Annotations: filterAnnotations(p.Annotations)},
		Spec:       corev1.PodSpec{NodeName: p.Spec.NodeName, HostNetwork: p.Spec.HostNetwork, Containers: slimContainers(p.Spec.Containers), Volumes: claimVolumes(p.Spec.Volumes)},
		// The pod's addresses are kept so observed traffic can be attributed to workloads; they never leave the agent.
		Status: corev1.PodStatus{Phase: p.Status.Phase, ContainerStatuses: cs, PodIP: p.Status.PodIP, PodIPs: p.Status.PodIPs},
	}, nil
}

func stripReplicaSet(o any) (any, error) {
	r, ok := o.(*appsv1.ReplicaSet)
	if !ok {
		return o, nil
	}
	return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: r.Name, Namespace: r.Namespace, UID: r.UID, OwnerReferences: r.OwnerReferences}}, nil
}

func stripDeployment(o any) (any, error) {
	d, ok := o.(*appsv1.Deployment)
	if !ok {
		return o, nil
	}
	return &appsv1.Deployment{
		ObjectMeta: slim(d.ObjectMeta),
		Spec:       appsv1.DeploymentSpec{Replicas: d.Spec.Replicas, Template: slimTemplate(d.Spec.Template)},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: d.Status.ReadyReplicas},
	}, nil
}

func stripStatefulSet(o any) (any, error) {
	d, ok := o.(*appsv1.StatefulSet)
	if !ok {
		return o, nil
	}
	return &appsv1.StatefulSet{
		ObjectMeta: slim(d.ObjectMeta),
		Spec:       appsv1.StatefulSetSpec{Replicas: d.Spec.Replicas, Template: slimTemplate(d.Spec.Template)},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: d.Status.ReadyReplicas},
	}, nil
}

func stripDaemonSet(o any) (any, error) {
	d, ok := o.(*appsv1.DaemonSet)
	if !ok {
		return o, nil
	}
	return &appsv1.DaemonSet{
		ObjectMeta: slim(d.ObjectMeta),
		Spec:       appsv1.DaemonSetSpec{Template: slimTemplate(d.Spec.Template)},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: d.Status.DesiredNumberScheduled, NumberReady: d.Status.NumberReady},
	}, nil
}

func stripService(o any) (any, error) {
	s, ok := o.(*corev1.Service)
	if !ok {
		return o, nil
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: s.UID},
		Spec: corev1.ServiceSpec{
			Type: s.Spec.Type, Selector: s.Spec.Selector, Ports: s.Spec.Ports,
			ClusterIP: s.Spec.ClusterIP, ClusterIPs: s.Spec.ClusterIPs, ExternalIPs: s.Spec.ExternalIPs,
		},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: s.Status.LoadBalancer.Ingress}},
	}, nil
}

func stripIngress(o any) (any, error) {
	i, ok := o.(*networkingv1.Ingress)
	if !ok {
		return o, nil
	}
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: i.Name, Namespace: i.Namespace, UID: i.UID},
		Spec:       networkingv1.IngressSpec{IngressClassName: i.Spec.IngressClassName, Rules: i.Spec.Rules, DefaultBackend: i.Spec.DefaultBackend},
	}, nil
}

// claimVolumes keeps the names of persistent volume claims a pod mounts, and nothing else about its
// volumes: Secret, ConfigMap, projected, host-path and every other source is dropped here.
func claimVolumes(in []corev1.Volume) []corev1.Volume {
	var out []corev1.Volume
	for _, v := range in {
		if pvc := v.PersistentVolumeClaim; pvc != nil {
			out = append(out, corev1.Volume{Name: v.Name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.ClaimName}}})
		}
	}
	return out
}

func stripPVC(o any) (any, error) {
	c, ok := o.(*corev1.PersistentVolumeClaim)
	if !ok {
		return o, nil
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: c.Namespace, UID: c.UID},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: c.Spec.StorageClassName, VolumeName: c.Spec.VolumeName, AccessModes: c.Spec.AccessModes,
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: c.Spec.Resources.Requests[corev1.ResourceStorage]}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: c.Status.Phase},
	}, nil
}

// stripPV keeps only which nodes a volume is tied to. The volume source (NFS server and path, CSI
// attributes, host path) is where storage credentials and addresses live and is discarded.
func stripPV(o any) (any, error) {
	v, ok := o.(*corev1.PersistentVolume)
	if !ok {
		return o, nil
	}
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: v.Name},
		Spec:       corev1.PersistentVolumeSpec{NodeAffinity: v.Spec.NodeAffinity},
	}, nil
}

func stripHPA(o any) (any, error) {
	h, ok := o.(*autoscalingv2.HorizontalPodAutoscaler)
	if !ok {
		return o, nil
	}
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: h.Name, Namespace: h.Namespace, UID: h.UID},
		Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: h.Spec.ScaleTargetRef, MinReplicas: h.Spec.MinReplicas, MaxReplicas: h.Spec.MaxReplicas, Metrics: h.Spec.Metrics},
		Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: h.Status.CurrentReplicas},
	}, nil
}

func stripPDB(o any) (any, error) {
	b, ok := o.(*policyv1.PodDisruptionBudget)
	if !ok {
		return o, nil
	}
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: b.Name, Namespace: b.Namespace, UID: b.UID},
		Spec:       policyv1.PodDisruptionBudgetSpec{MinAvailable: b.Spec.MinAvailable, MaxUnavailable: b.Spec.MaxUnavailable, Selector: b.Spec.Selector},
		Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: b.Status.DisruptionsAllowed},
	}, nil
}
