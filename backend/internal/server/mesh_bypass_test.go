package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/model"
)

// TestFlowTableCarriesMeshBypassSyns mirrors TestFlowTableCarriesFailedAttempts (observed_test.go) for the
// new counter: cumulative sums across batches, window holds only the latest batch's own contribution.
func TestFlowTableCarriesMeshBypassSyns(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")
	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 443, Protocol: "tcp", Method: "ebpf", MeshBypassSyns: 2}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.MeshBypassSyns != 2 || e.WindowMeshBypassSyns != 2 {
		t.Errorf("meshBypassSyns=%d/%d, want 2/2", e.MeshBypassSyns, e.WindowMeshBypassSyns)
	}
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 443, Protocol: "tcp", Method: "ebpf", MeshBypassSyns: 1}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.MeshBypassSyns != 3 || e.WindowMeshBypassSyns != 1 {
		t.Errorf("cumulative meshBypassSyns=%d window=%d, want 3/1", e.MeshBypassSyns, e.WindowMeshBypassSyns)
	}
}

// TestObservedTopologySetsTentativeMeshBypass checks that observedTopology, which never sees a
// model.Service, sets Dependency.MeshBypass purely from the raw counter - the refinement against actual
// mesh configuration is applyMeshBypassFacts' own job, tested separately below.
func TestObservedTopologySetsTentativeMeshBypass(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf", MeshBypassSyns: 1}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	d := findDep(deps, 443)
	if d == nil {
		t.Fatal("dependency not found")
	}
	if !d.MeshBypass {
		t.Error("expected the tentative MeshBypass flag to be set from a non-zero MeshBypassSyns")
	}
}

func findDep(deps []model.Dependency, port int) *model.Dependency {
	for i := range deps {
		if deps[i].Port == port {
			return &deps[i]
		}
	}
	return nil
}

func TestApplyMeshBypassFactsConfirmsOnlyWhenTheCallerIsActuallyMeshed(t *testing.T) {
	svc := func(id string, mesh *model.ServiceMesh) model.Service {
		return model.Service{ID: id, Mesh: mesh}
	}
	dep := func(from string, port int, bypass bool) model.Dependency {
		return model.Dependency{From: from, FromKind: "service", Port: port, MeshBypass: bypass}
	}

	cases := []struct {
		name string
		svc  model.Service
		dep  model.Dependency
		want bool
	}{
		{"sidecar-meshed, port not excluded: confirmed", svc("s1", &model.ServiceMesh{Mesh: "istio", Proxy: facts.ProxySidecar}), dep("s1", 443, true), true},
		{"no mesh at all: cleared", svc("s2", nil), dep("s2", 443, true), false},
		{"ambient mode (no per-pod sidecar to redirect to): cleared", svc("s3", &model.ServiceMesh{Mesh: "istio", Proxy: facts.ProxyAmbient}), dep("s3", 443, true), false},
		{"explicitly bypassing the mesh: cleared", svc("s4", &model.ServiceMesh{Mesh: "istio", Proxy: facts.ProxySidecar, Bypass: true}), dep("s4", 443, true), false},
		{"this exact port is declared excluded: cleared", svc("s5", &model.ServiceMesh{Mesh: "istio", Proxy: facts.ProxySidecar, ExcludedPorts: []string{"out:443"}}), dep("s5", 443, true), false},
		{"a different port is excluded: stays confirmed", svc("s6", &model.ServiceMesh{Mesh: "istio", Proxy: facts.ProxySidecar, ExcludedPorts: []string{"out:5432"}}), dep("s6", 443, true), true},
		{"never tentatively set in the first place: stays false", svc("s7", &model.ServiceMesh{Mesh: "istio", Proxy: facts.ProxySidecar}), dep("s7", 443, false), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := []model.Dependency{tc.dep}
			applyMeshBypassFacts(deps, []model.Service{tc.svc})
			if deps[0].MeshBypass != tc.want {
				t.Errorf("MeshBypass = %v, want %v", deps[0].MeshBypass, tc.want)
			}
		})
	}
}

func TestApplyMeshBypassFactsLeavesExternalCallersAlone(t *testing.T) {
	deps := []model.Dependency{{From: "1.2.3.4", FromKind: "external", Port: 443, MeshBypass: true}}
	applyMeshBypassFacts(deps, nil)
	if deps[0].MeshBypass {
		t.Error("an external caller has no service to check mesh facts against, and must never be confirmed")
	}
}
