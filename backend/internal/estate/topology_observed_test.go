package estate

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/connectivity"
	"continuum/internal/facts"
)

// Traffic is drawn into the topology only from a cluster whose agent is allowed to report it (access tier 2 and above). A cluster
// below that is still shown, and its flows are held back from the picture rather than deleted.
func TestOnlyAClusterAtTierTwoOrAboveDrawsObservedTraffic(t *testing.T) {
	r := newHubRig(t)
	wide, _, _ := r.approvedAgent(t, fp)
	narrow, _, _ := r.approvedAgent(t, fp2)
	if _, err := r.hub.SetAgentTier(r.ctx, "alex", narrow, 1, func(int) string { return "" }); err != nil {
		t.Fatal(err)
	}
	// Each cluster has two workloads and one edge between them, told apart by the port. The tier-2 cluster also calls the Kubernetes
	// API port of an address that is a node of the tier-1 cluster: a cluster the server knows of, so not a suspicion.
	for id, port := range map[string]uint32{wide: 8080, narrow: 9090} {
		st := facts.New()
		if id == narrow {
			st.Nodes["p"] = &continuumv1.NodeFacts{Key: "p", InternalIps: []string{"198.51.100.60"}}
		}
		for _, n := range []string{"x", "y"} {
			w := wk("a", "Deployment", n)
			st.Workloads[w.Key] = w
		}
		v := NewView()
		v.State = st
		v.Connected = true
		v.LastSync = time.Now()
		fs := []*continuumv1.Flow{flowOf(wep("a/Deployment/x"), wep("a/Deployment/y"), port, 5)}
		if id == wide {
			fs = append(fs, flowOf(wep("a/Deployment/x"), xep("198.51.100.60"), 6443, 50))
		} else {
			fs = append(fs, flowOf(wep("a/Deployment/x"), xep("203.0.113.9"), 6443, 50)) // what would be suspected, if this cluster's traffic counted
		}
		v.Flows.Apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: fs}, time.Now().Add(-time.Minute))
		if id == wide {
			now := time.Now()
			v.Paths["p"] = &connectivity.PathTrack{T: connectivity.IssuedTarget{ID: "p", Host: "198.51.100.60", Port: 6443, Source: "manual"}, At: now}
		}
		r.hub.Mu.Lock()
		r.hub.Views[id] = v
		r.hub.Mu.Unlock()
	}

	doc, err := r.hub.State(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	drawn := map[int]bool{}
	for _, d := range doc.Topology.Dependencies {
		drawn[d.Port] = true
	}
	if !drawn[8080] || drawn[9090] {
		t.Fatalf("the tier-2 cluster's traffic (8080) is drawn and the tier-1 cluster's (9090) is not: %+v", doc.Topology.Dependencies)
	}
	// A measured path to an address of another cluster says which cluster it leads to, tier or not.
	other, err := r.hub.C.AgentInOrg(r.ctx, narrow)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Topology.Paths) != 1 || doc.Topology.Paths[0].ToCluster != other.ClusterID {
		t.Fatalf("the path to a node of the other cluster does not name it (%q): %+v", other.ClusterID, doc.Topology.Paths)
	}
	for _, sg := range doc.Topology.Suggestions {
		if sg.Kind == "infrastructure" {
			t.Fatalf("a node of a cluster the server knows of is suspected: %+v", sg)
		}
	}
}
