package facts

import (
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
)

// A Snapshot is encoded after the lock that guarded the State is released, so later changes to the
// State, including a full replacement and deletions, must not show up in it.
func TestSnapshotIsUnaffectedByLaterChangesToTheState(t *testing.T) {
	s := New()
	s.Apply(&ikhnosv1.Sync{Seq: 1, Full: true, Nodes: []*ikhnosv1.NodeFacts{node("a"), node("b")}, Workloads: []*ikhnosv1.WorkloadFacts{wl("w", 8)}})
	snap := s.Snapshot()

	s.Apply(&ikhnosv1.Sync{Seq: 2, DeletedNodes: []string{"a"}, Nodes: []*ikhnosv1.NodeFacts{{Key: "b", Name: "renamed"}}})
	s.Apply(&ikhnosv1.Sync{Seq: 3, Full: true, Nodes: []*ikhnosv1.NodeFacts{node("z")}})

	if snap.Seq != 1 || len(snap.Nodes) != 2 || len(snap.Workloads) != 1 {
		t.Fatalf("snapshot changed with the state: seq %d, %d nodes, %d workloads", snap.Seq, len(snap.Nodes), len(snap.Workloads))
	}
	for _, n := range snap.Nodes {
		if n.Name == "renamed" {
			t.Fatal("snapshot saw a later edit")
		}
	}
	b, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(b)
	if err != nil || back.Seq != 3 || len(back.Nodes) != 1 || back.Nodes["z"] == nil {
		t.Fatalf("Marshal round trip: %v %+v", err, back)
	}
}
