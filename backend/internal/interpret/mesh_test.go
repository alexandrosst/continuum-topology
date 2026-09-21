package interpret

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/model"
)

type (
	modelService           = model.Service
	modelNamespace         = model.Namespace
	modelCreateApplication = model.CreateApplication
)

func TestInterpretCarriesTheMeshAndKeepsItsControlPlaneOutOfApplications(t *testing.T) {
	st := k3sFixture()
	st.Namespaces["meshed"] = &continuumv1.NamespaceFacts{Key: "meshed", Name: "meshed", Labels: map[string]string{"istio-injection": "enabled"}}
	st.Namespaces["opt"] = &continuumv1.NamespaceFacts{Key: "opt", Name: "opt", Annotations: map[string]string{"linkerd.io/inject": "disabled"}}
	st.Workloads["istio-system/Deployment/istiod"] = &W{Key: "istio-system/Deployment/istiod", Namespace: "istio-system", Kind: "Deployment", Name: "istiod", Replicas: 1, ReadyReplicas: 1,
		Mesh: &continuumv1.WorkloadMesh{Mesh: "istio", ControlPlane: true, Source: "workload"}}
	st.Workloads["meshed/Deployment/web"] = &W{Key: "meshed/Deployment/web", Namespace: "meshed", Kind: "Deployment", Name: "web", Replicas: 1, ReadyReplicas: 1,
		Mesh: &continuumv1.WorkloadMesh{Mesh: "istio", Proxy: "sidecar", ExcludedPorts: []string{"out:5432"}, Source: "pods"}}
	st.Cluster.Mesh = &continuumv1.MeshFacts{Kind: "istio", Mode: "sidecar", Version: "1.22.3", Mtls: "strict", PolicyRead: true,
		NamespaceMtls: map[string]string{"meshed": "permissive"},
		ControlPlane:  []string{"istio-system/Deployment/istiod", "kube-system/Deployment/gateway"}}

	out := Interpret(Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "edge", State: st, Now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)})

	cm := out.Clusters[0].Mesh
	if cm == nil || cm.Kind != "istio" || cm.Mtls != "strict" || cm.Version != "1.22.3" || !cm.PolicyRead || cm.NamespaceMtls["meshed"] != "permissive" {
		t.Fatalf("cluster mesh: %+v", cm)
	}
	if len(cm.ControlPlane) != 1 || cm.ControlPlane[0] != ServiceID("cl-x", "istio-system/Deployment/istiod") {
		t.Fatalf("the control plane is listed by service id, system namespaces left out: %v", cm.ControlPlane)
	}
	var web, istiod *modelService
	for i := range out.Services {
		switch out.Services[i].Name {
		case "web":
			web = &out.Services[i]
		case "istiod":
			istiod = &out.Services[i]
		}
	}
	if web == nil || web.Mesh == nil || web.Mesh.Proxy != "sidecar" || web.Mesh.Source != "pods" || len(web.Mesh.ExcludedPorts) != 1 {
		t.Fatalf("web: %+v", web)
	}
	if istiod == nil || istiod.Mesh == nil || !istiod.Mesh.ControlPlane || istiod.ApplicationHint != "" {
		t.Fatalf("istiod: %+v", istiod)
	}
	for _, sg := range out.Suggestions {
		if ca, ok := sg.Apply.(modelCreateApplication); ok {
			for _, id := range ca.ServiceIDs {
				if id == istiod.ID {
					t.Errorf("the control plane was offered for grouping into %q", ca.Application.Name)
				}
			}
		}
	}
	ns := map[string]modelNamespace{}
	for _, n := range out.Namespaces {
		ns[n.Name] = n
	}
	if n := ns["meshed"]; n.Mesh != "istio" || n.MeshProxy != "sidecar" || n.Mtls != "permissive" || n.MeshOff {
		t.Errorf("meshed namespace: %+v", n)
	}
	if n := ns["opt"]; n.Mesh != "linkerd" || !n.MeshOff {
		t.Errorf("an opted-out namespace: %+v", n)
	}
}

func TestNoMeshInTheFactsMeansNoMeshInTheTopology(t *testing.T) {
	out := Interpret(Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "edge", State: k3sFixture(), Now: time.Now()})
	if out.Clusters[0].Mesh != nil {
		t.Fatalf("%+v", out.Clusters[0].Mesh)
	}
	for _, s := range out.Services {
		if s.Mesh != nil {
			t.Errorf("%s has mesh data", s.Name)
		}
	}
}
