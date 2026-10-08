package facts

import (
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/proto"
)

func node(k string) *continuumv1.NodeFacts { return &continuumv1.NodeFacts{Key: k, Name: k} }
func wl(k string, pad int) *continuumv1.WorkloadFacts {
	return &continuumv1.WorkloadFacts{Key: k, Name: k, Labels: map[string]string{"pad": strings.Repeat("p", pad)}}
}

func recount(s *State) int64 {
	var n int64
	for _, x := range s.Nodes {
		n += int64(proto.Size(x))
	}
	for _, x := range s.Namespaces {
		n += int64(proto.Size(x))
	}
	for _, x := range s.Workloads {
		n += int64(proto.Size(x))
	}
	c, m := s.otherBytes()
	return n + c + m
}

func TestCheckRefusesAMessageThatWouldExceedTheTotalAndChangesNothing(t *testing.T) {
	l := Limits{Nodes: 10, Namespaces: 10, Workloads: 10, Bytes: 1 << 20}
	s := New()
	var first []*continuumv1.NodeFacts
	for i := 0; i < 8; i++ {
		first = append(first, node(fmt.Sprint("n", i)))
	}
	full := &continuumv1.Sync{Full: true, Nodes: first}
	if err := s.Check(full, l); err != nil {
		t.Fatal(err)
	}
	s.Apply(full)
	// each delta is tiny, together they are not
	delta := &continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{node("a"), node("b"), node("c")}}
	err := s.Check(delta, l)
	var le *LimitError
	if !errors.As(err, &le) || le.What != "nodes" || le.Have != 11 || le.Max != 10 {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Errorf("the message should say what to do: %v", err)
	}
	if len(s.Nodes) != 8 {
		t.Fatal("a refused check changed the state")
	}
	// replacing or deleting existing keys is free, and a delete makes room in the same message
	ok := &continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{node("n0"), node("x"), node("y")}, DeletedNodes: []string{"n1", "n2", "n3"}}
	if err := s.Check(ok, l); err != nil {
		t.Fatalf("a message that ends at 7 nodes: %v", err)
	}
	// a key added and deleted in one message ends up gone
	if err := s.Check(&continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{node("q1"), node("q2"), node("q3"), node("q4")}, DeletedNodes: []string{"q1", "q2", "q3", "q4"}}, l); err != nil {
		t.Fatalf("added then deleted: %v", err)
	}
	// duplicates inside one message count once
	dup := &continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{node("d"), node("d"), node("d"), node("d")}}
	if err := s.Check(dup, l); err != nil {
		t.Fatalf("one distinct key: %v", err)
	}
	// a full sync starts from nothing
	var many []*continuumv1.NodeFacts
	for i := 0; i < 11; i++ {
		many = append(many, node(fmt.Sprint("m", i)))
	}
	if err := s.Check(&continuumv1.Sync{Full: true, Nodes: many}, l); err == nil {
		t.Fatal("11 nodes in a full sync were accepted")
	}
	if err := s.Check(&continuumv1.Sync{Full: true, Nodes: many[:10]}, l); err != nil {
		t.Fatalf("10 nodes in a full sync: %v", err)
	}
}

func TestUniqueKeyFloodStopsAtTheLimit(t *testing.T) {
	l := Limits{Nodes: 100, Namespaces: 100, Workloads: 500, Bytes: 1 << 30}
	s := New()
	accepted := 0
	for i := 0; i < 2000; i++ {
		m := &continuumv1.Sync{Workloads: []*continuumv1.WorkloadFacts{wl(fmt.Sprint("ns/Deployment/", i), 1)}}
		if err := s.Check(m, l); err != nil {
			break
		}
		s.Apply(m)
		accepted++
	}
	if accepted != 500 || len(s.Workloads) != 500 {
		t.Fatalf("accepted %d, hold %d", accepted, len(s.Workloads))
	}
}

func TestTotalBytesAreBoundedEvenWhenCountsAreNot(t *testing.T) {
	l := Limits{Nodes: 1000, Namespaces: 1000, Workloads: 1000, Bytes: 100 << 10}
	s := New()
	var refused error
	for i := 0; i < 100 && refused == nil; i++ {
		m := &continuumv1.Sync{Workloads: []*continuumv1.WorkloadFacts{wl(fmt.Sprint("w", i), 8<<10)}} // 8 KiB each, so ~12 fit
		if refused = s.Check(m, l); refused == nil {
			s.Apply(m)
		}
	}
	var le *LimitError
	if !errors.As(refused, &le) || !strings.Contains(le.What, "bytes") {
		t.Fatalf("refused = %v", refused)
	}
	if s.Bytes() > l.Bytes {
		t.Fatalf("held %d bytes", s.Bytes())
	}
	// an oversized cluster record or module list counts too
	huge := &continuumv1.Sync{Cluster: &continuumv1.ClusterFacts{Uid: "u", StorageClasses: []string{strings.Repeat("s", 90<<10)}}}
	if err := s.Check(huge, l); err == nil {
		t.Fatal("a huge cluster record was accepted")
	}
}

func TestByteAccountingMatchesTheStateAfterAnySequenceOfMessages(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	s := New()
	for i := 0; i < 400; i++ {
		m := &continuumv1.Sync{Full: rng.Intn(15) == 0, Seq: uint64(i)}
		for j := rng.Intn(6); j > 0; j-- {
			k := fmt.Sprint("k", rng.Intn(12))
			m.Nodes = append(m.Nodes, &continuumv1.NodeFacts{Key: k, Name: strings.Repeat("n", rng.Intn(50))})
			m.Workloads = append(m.Workloads, wl(k, rng.Intn(300)))
			m.Namespaces = append(m.Namespaces, &continuumv1.NamespaceFacts{Key: k, Name: strings.Repeat("s", rng.Intn(20))})
		}
		for j := rng.Intn(3); j > 0; j-- {
			k := fmt.Sprint("k", rng.Intn(12))
			m.DeletedNodes, m.DeletedWorkloads, m.DeletedNamespaces = append(m.DeletedNodes, k), append(m.DeletedWorkloads, k), append(m.DeletedNamespaces, k)
		}
		if rng.Intn(9) == 0 {
			m.Cluster = &continuumv1.ClusterFacts{Uid: "u", Version: strings.Repeat("v", rng.Intn(40))}
		}
		// the projection must agree with what Apply then does
		wantN, wantB := project(s.Nodes, s.nodeBytes, m.Full, m.Nodes, func(n *continuumv1.NodeFacts) string { return n.Key }, m.DeletedNodes)
		wantW, wantWB := project(s.Workloads, s.wlBytes, m.Full, m.Workloads, func(w *continuumv1.WorkloadFacts) string { return w.Key }, m.DeletedWorkloads)
		s.Apply(m)
		if len(s.Nodes) != wantN || s.nodeBytes != wantB || len(s.Workloads) != wantW || s.wlBytes != wantWB {
			t.Fatalf("step %d: projected (%d,%d,%d,%d), got (%d,%d,%d,%d)", i, wantN, wantB, wantW, wantWB, len(s.Nodes), s.nodeBytes, len(s.Workloads), s.wlBytes)
		}
		if s.Bytes() != recount(s) {
			t.Fatalf("step %d: accounted %d, actual %d", i, s.Bytes(), recount(s))
		}
	}
	// a state loaded from its stored form has the same accounting
	b, _ := s.Marshal()
	back, err := Unmarshal(b)
	if err != nil || back.Bytes() != s.Bytes() {
		t.Fatalf("after a round trip: %v %d vs %d", err, back.Bytes(), s.Bytes())
	}
}

func TestSanitizeCutsStringsMapsAndListsInEveryField(t *testing.T) {
	long := strings.Repeat("é", MaxString) // 2*MaxString bytes
	w := &continuumv1.WorkloadFacts{Key: "k", Name: long, Exposure: long,
		Images:      []*continuumv1.ContainerImage{{Image: long}},
		Mesh:        &continuumv1.WorkloadMesh{Source: long},
		Labels:      map[string]string{long: "dropped", "ok": long},
		Hosts:       []string{"ok", long},
		NodeNames:   make([]string, MaxRepeated+1),
		Reachable:   []*continuumv1.Address{{Ip: long}},
		Annotations: map[string]string{"a": long, "b": "short"}}
	if err := Sanitize(w); err != nil {
		t.Fatal(err)
	}
	valid := func(name, s string, max int) {
		t.Helper()
		if len(s) > max || len(s) < max-1 || !utf8.ValidString(s) || !strings.HasPrefix(s, "éé") {
			t.Errorf("%s: %d bytes, valid utf-8 %v", name, len(s), utf8.ValidString(s))
		}
	}
	valid("name", w.Name, MaxString)
	valid("exposure", w.Exposure, MaxString)
	valid("image", w.Images[0].Image, MaxString)
	valid("nested mesh", w.Mesh.Source, MaxString)
	valid("list entry", w.Hosts[1], MaxString)
	valid("nested address", w.Reachable[0].Ip, MaxString)
	valid("map value", w.Annotations["a"], MaxMapValue) // a long label or annotation value is cut at MaxMapValue
	valid("label value", w.Labels["ok"], MaxMapValue)
	if w.Annotations["b"] != "short" || w.Hosts[0] != "ok" || len(w.Labels) != 1 {
		t.Errorf("short values must be untouched and a label whose key is too long dropped: %v %v", w.Annotations, w.Labels)
	}
	if len(w.NodeNames) != MaxRepeated {
		t.Errorf("a list of %d entries was not cut to %d", len(w.NodeNames), MaxRepeated)
	}

	tooMany := map[string]string{}
	for i := 0; i <= MaxMapEntries+5; i++ {
		tooMany[fmt.Sprintf("%05d", i)] = "v"
	}
	ns := &continuumv1.NamespaceFacts{Key: "k", Labels: tooMany}
	cl := &continuumv1.ClusterFacts{Mesh: &continuumv1.MeshFacts{NamespaceMtls: maps.Clone(tooMany)}}
	for _, m := range []proto.Message{ns, cl} {
		if err := Sanitize(m); err != nil {
			t.Fatal(err)
		}
	}
	if len(ns.Labels) != MaxMapEntries || len(cl.Mesh.NamespaceMtls) != MaxMapEntries || ns.Labels["00000"] == "" || ns.Labels[fmt.Sprintf("%05d", MaxMapEntries)] != "" {
		t.Errorf("a map over the cap keeps its first %d keys: %d, %d", MaxMapEntries, len(ns.Labels), len(cl.Mesh.NamespaceMtls))
	}

	// A normal fact passes untouched.
	ok := &continuumv1.WorkloadFacts{Key: "ns/Deployment/a", Name: "a", Labels: map[string]string{"app": "a"}, Images: []*continuumv1.ContainerImage{{Image: "nginx:1"}}}
	want := proto.Clone(ok)
	if err := Sanitize(ok); err != nil || !proto.Equal(ok, want) {
		t.Fatalf("a normal fact changed: %v %v", err, ok)
	}
}

func TestSanitizeSyncDoesNotCapTheMessagesOwnLists(t *testing.T) {
	var ws []*continuumv1.WorkloadFacts
	for i := 0; i < MaxRepeated+10; i++ {
		ws = append(ws, wl(fmt.Sprint("w", i), 1))
	}
	s := &continuumv1.Sync{Workloads: ws, DeletedNodes: []string{"n", strings.Repeat("k", MaxString+1)}}
	if err := SanitizeSync(s); err != nil {
		t.Fatalf("a large cluster's full sync: %v", err)
	}
	if len(s.Workloads) != MaxRepeated+10 {
		t.Fatalf("the message's own list was cut to %d", len(s.Workloads))
	}
	if len(s.DeletedNodes) != 1 || s.DeletedNodes[0] != "n" {
		t.Fatalf("an over-long deleted key names nothing and is dropped: %v", s.DeletedNodes)
	}
}

func TestCheckStateCatchesAPoisonedStoredState(t *testing.T) {
	s := New()
	for i := 0; i < 20; i++ {
		s.Apply(&continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{node(fmt.Sprint(i))}})
	}
	if err := s.CheckState(Limits{Nodes: 20, Namespaces: 1, Workloads: 1, Bytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckState(Limits{Nodes: 19, Namespaces: 1, Workloads: 1, Bytes: 1 << 20}); err == nil {
		t.Fatal("too many nodes accepted")
	}
	if err := s.CheckState(Limits{Nodes: 20, Namespaces: 1, Workloads: 1, Bytes: 10}); err == nil {
		t.Fatal("too many bytes accepted")
	}
}
