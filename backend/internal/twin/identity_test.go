package twin

import (
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/store"
)

func node(name, provider, sysUUID, machine string) *continuumv1.NodeFacts {
	return &continuumv1.NodeFacts{Key: name, Name: name, ProviderId: provider, SystemUuid: sysUUID, MachineId: machine}
}

func TestNodeIdentityOrderAndWhatIsNotTrusted(t *testing.T) {
	for _, c := range []struct {
		name string
		n    *continuumv1.NodeFacts
		want NodeBasis
	}{
		{"cloud provider id wins", node("n", "aws:///eu-west-1a/i-0abc123def", "11111111-2222-3333-4444-555555555555", "abcdef0123456789"), BasisProviderID},
		{"k3s provider id is only the name", node("n", "k3s://n", "11111111-2222-3333-4444-555555555555", "abcdef0123456789"), BasisSystemUUID},
		{"kind provider id is only the name", node("n", "kind://docker/kind/n", "", "abcdef0123456789"), BasisMachineID},
		{"a provider id ending in the name is the name in disguise", node("n", "custom://cluster/n", "", ""), BasisName},
		{"system uuid before machine id", node("n", "", "11111111-2222-3333-4444-555555555555", "abcdef0123456789"), BasisSystemUUID},
		{"all-zero uuid is junk", node("n", "", "00000000-0000-0000-0000-000000000000", "abcdef0123456789"), BasisMachineID},
		{"a well-known firmware default is junk", node("n", "", "03000200-0400-0500-0006-000700080009", ""), BasisName},
		{"too short is junk", node("n", "", "", "abc"), BasisName},
		{"nothing at all: the name", node("n", "", "", ""), BasisName},
	} {
		if got := IdentityOf(c.n).Basis; got != c.want {
			t.Errorf("%s: basis %s, want %s", c.name, got, c.want)
		}
	}
	// The identifier is never exposed, only a digest that does not contain it.
	id := IdentityOf(node("n", "aws:///eu-west-1a/i-0abc123def", "", ""))
	if strings.Contains(id.String(), "i-0abc123def") || id.Digest == "" {
		t.Errorf("identity leaks the identifier: %s", id)
	}
	// Case and padding do not change an identity.
	a, b := IdentityOf(node("n", "", "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", "")), IdentityOf(node("n", "", " aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee ", ""))
	if a != b {
		t.Errorf("%v != %v", a, b)
	}
}

var t0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func TestFirstSightKeepsTheIdNodesAlwaysHad(t *testing.T) {
	r := NewRegistry()
	got := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("edge-1", "aws:///x/i-0abc123def", "", ""), node("edge-2", "", "", "")}, t0)
	if got["edge-1"].ID != LegacyNodeID("cl-1", "edge-1") || got["edge-2"].ID != LegacyNodeID("cl-1", "edge-2") {
		t.Errorf("existing workspaces reference these ids: %+v", got)
	}
	if got["edge-1"].Replaced || got["edge-1"].Renamed {
		t.Errorf("%+v", got["edge-1"])
	}
}

func TestARenamedMachineKeepsItsIdAndGainsAnAlias(t *testing.T) {
	r := NewRegistry()
	p := "aws:///eu/i-0abc123def"
	first := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("ip-10-0-0-1", p, "", "")}, t0)["ip-10-0-0-1"]
	second := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("worker-a", p, "", "")}, t0.Add(time.Hour))["worker-a"]
	if second.ID != first.ID {
		t.Fatalf("a rename must not create a node: %s vs %s", second.ID, first.ID)
	}
	if !second.Renamed || len(second.Aliases) != 1 || second.Aliases[0] != "ip-10-0-0-1" {
		t.Errorf("%+v", second)
	}
	// Renamed again, and back: aliases accumulate without repeating.
	third := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("worker-b", p, "", "")}, t0.Add(2*time.Hour))["worker-b"]
	back := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("worker-a", p, "", "")}, t0.Add(3*time.Hour))["worker-a"]
	if third.ID != first.ID || back.ID != first.ID || len(back.Aliases) != 3 {
		t.Errorf("%+v %+v", third, back)
	}
	// Stable when nothing changed.
	again := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("worker-a", p, "", "")}, t0.Add(4*time.Hour))["worker-a"]
	if again.Renamed || again.ID != first.ID {
		t.Errorf("%+v", again)
	}
}

func TestADifferentMachineUnderAnOldNameIsANewNode(t *testing.T) {
	r := NewRegistry()
	old := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("edge-1", "aws:///eu/i-0000000001", "", "")}, t0)["edge-1"]
	// The instance was replaced; the new one took the same name.
	fresh := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("edge-1", "aws:///eu/i-0000000002", "", "")}, t0.Add(time.Hour))["edge-1"]
	if fresh.ID == old.ID || !fresh.Replaced {
		t.Fatalf("a replaced machine must not inherit the old record: %+v vs %+v", fresh, old)
	}
	// And the old machine, if it comes back under another name, still has its own id.
	oldAgain := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("edge-9", "aws:///eu/i-0000000001", "", ""), node("edge-1", "aws:///eu/i-0000000002", "", "")}, t0.Add(2*time.Hour))
	if oldAgain["edge-9"].ID != old.ID || oldAgain["edge-1"].ID != fresh.ID {
		t.Errorf("%+v", oldAgain)
	}
}

func TestNameSwapInOnePassGivesEachMachineItsOwnId(t *testing.T) {
	r := NewRegistry()
	a, b := "aws:///eu/i-0000000001", "aws:///eu/i-0000000002"
	first := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("n1", a, "", "")}, t0)["n1"]
	// The first machine is renamed n2 while a new machine takes n1, in one snapshot.
	got := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("n2", a, "", ""), node("n1", b, "", "")}, t0.Add(time.Hour))
	if got["n2"].ID != first.ID {
		t.Errorf("the renamed machine keeps its id: %+v", got["n2"])
	}
	if got["n1"].ID == first.ID || !got["n1"].Replaced {
		t.Errorf("the new machine must not take the old machine's id: %+v", got["n1"])
	}
}

func TestClonedMachineIDsIdentifyNeither(t *testing.T) {
	r := NewRegistry()
	got := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("a", "", "", "cafebabecafebabe"), node("b", "", "", "cafebabecafebabe")}, t0)
	if got["a"].Identity.Basis != BasisName || got["b"].Identity.Basis != BasisName || got["a"].ID == got["b"].ID {
		t.Errorf("%+v", got)
	}
	if len(r.Dirty()) != 0 {
		t.Error("name-based nodes need no remembered identity")
	}
}

func TestSameNodeNamesInTwoClustersNeverCollide(t *testing.T) {
	r := NewRegistry()
	same := func() []*continuumv1.NodeFacts {
		return []*continuumv1.NodeFacts{node("node-1", "", "", ""), node("node-2", "", "", "")}
	}
	a, b := r.Resolve("cl-aaaa", same(), t0), r.Resolve("cl-bbbb", same(), t0)
	seen := map[string]string{}
	for c, m := range map[string]map[string]NodeRecord{"cl-aaaa": a, "cl-bbbb": b} {
		for n, rec := range m {
			if prev, dup := seen[rec.ID]; dup {
				t.Errorf("%s/%s and %s share %s", c, n, prev, rec.ID)
			}
			seen[rec.ID] = c + "/" + n
		}
	}
	// Even the same machine identity in two clusters (a cloned image) stays separate.
	ident := "aws:///eu/i-0abcdef012"
	x, y := r.Resolve("cl-aaaa", []*continuumv1.NodeFacts{node("n", ident, "", "")}, t0)["n"], r.Resolve("cl-bbbb", []*continuumv1.NodeFacts{node("n", ident, "", "")}, t0)["n"]
	if x.ID == y.ID {
		t.Errorf("one identity in two clusters must be two nodes")
	}
}

func TestRegistrySurvivesARestart(t *testing.T) {
	r := NewRegistry()
	p := "aws:///eu/i-0abc123def"
	first := r.Resolve("cl-1", []*continuumv1.NodeFacts{node("old", p, "", "")}, t0)["old"]
	r.Resolve("cl-1", []*continuumv1.NodeFacts{node("new", p, "", "")}, t0.Add(time.Hour))
	saved := r.Dirty()
	if len(saved) != 1 || saved[0].Name != "new" || len(saved[0].Aliases) != 1 {
		t.Fatalf("%+v", saved)
	}
	if len(r.Dirty()) != 0 {
		t.Error("Dirty must reset")
	}
	// A new process loads what was stored and keeps the id.
	r2 := NewRegistry()
	r2.Load([]store.Identity(saved))
	again := r2.Resolve("cl-1", []*continuumv1.NodeFacts{node("new", p, "", "")}, t0.Add(2*time.Hour))["new"]
	if again.ID != first.ID || len(again.Aliases) != 1 || again.Aliases[0] != "old" || again.Renamed {
		t.Errorf("%+v", again)
	}
}
