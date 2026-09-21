// Package facts accumulates the raw observations an agent streams: one full
// snapshot on connect, then deltas.
package facts

import (
	"fmt"
	"strings"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// State is everything currently known about one cluster.
type State struct {
	Cluster    *continuumv1.ClusterFacts
	Nodes      map[string]*continuumv1.NodeFacts
	Namespaces map[string]*continuumv1.NamespaceFacts
	Workloads  map[string]*continuumv1.WorkloadFacts
	Modules    []*continuumv1.ModuleStatus
	Seq        uint64

	// Encoded sizes of what each map holds, kept up to date by Apply so that limits can be checked in
	// proportion to a change, not to the whole state.
	nodeBytes, nsBytes, wlBytes int64
}

func New() *State {
	return &State{
		Nodes:      map[string]*continuumv1.NodeFacts{},
		Namespaces: map[string]*continuumv1.NamespaceFacts{},
		Workloads:  map[string]*continuumv1.WorkloadFacts{},
	}
}

// Apply merges a Sync into the state. A full sync replaces everything. It does not enforce limits: call
// Check first with the limits that apply.
func (s *State) Apply(m *continuumv1.Sync) {
	if m.Full {
		s.Nodes = map[string]*continuumv1.NodeFacts{}
		s.Namespaces = map[string]*continuumv1.NamespaceFacts{}
		s.Workloads = map[string]*continuumv1.WorkloadFacts{}
		s.nodeBytes, s.nsBytes, s.wlBytes = 0, 0, 0
	}
	if m.Cluster != nil {
		s.Cluster = m.Cluster
	}
	for _, n := range m.Nodes {
		s.nodeBytes += put(s.Nodes, n.Key, n)
	}
	for _, k := range m.DeletedNodes {
		s.nodeBytes -= drop(s.Nodes, k)
	}
	for _, n := range m.Namespaces {
		s.nsBytes += put(s.Namespaces, n.Key, n)
	}
	for _, k := range m.DeletedNamespaces {
		s.nsBytes -= drop(s.Namespaces, k)
	}
	for _, w := range m.Workloads {
		s.wlBytes += put(s.Workloads, w.Key, w)
	}
	for _, k := range m.DeletedWorkloads {
		s.wlBytes -= drop(s.Workloads, k)
	}
	if len(m.Modules) > 0 {
		s.Modules = m.Modules
	}
	s.Seq = m.Seq
}

// put stores v under k and returns how many bytes the map grew by.
func put[T proto.Message](m map[string]T, k string, v T) int64 {
	d := int64(proto.Size(v))
	if old, ok := m[k]; ok {
		d -= int64(proto.Size(old))
	}
	m[k] = v
	return d
}

// drop removes k and returns the bytes that freed.
func drop[T proto.Message](m map[string]T, k string) int64 {
	old, ok := m[k]
	if !ok {
		return 0
	}
	delete(m, k)
	return int64(proto.Size(old))
}

// Marshal serialises the state as one full Sync (for the snapshot table).
func (s *State) Marshal() ([]byte, error) {
	m := &continuumv1.Sync{Seq: s.Seq, Full: true, Cluster: s.Cluster, Modules: s.Modules}
	for _, n := range s.Nodes {
		m.Nodes = append(m.Nodes, n)
	}
	for _, n := range s.Namespaces {
		m.Namespaces = append(m.Namespaces, n)
	}
	for _, w := range s.Workloads {
		m.Workloads = append(m.Workloads, w)
	}
	return proto.Marshal(m)
}

func Unmarshal(b []byte) (*State, error) {
	var m continuumv1.Sync
	if err := proto.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	s := New()
	s.Apply(&m)
	return s, nil
}

// DebugJSON is for logs and tests only.
func DebugJSON(m proto.Message) string { return protojson.Format(m) }

func (s *State) ClusterUID() string {
	if s.Cluster == nil {
		return ""
	}
	return s.Cluster.Uid
}

// Drift is what a full picture from the agent shows the server had wrong: things the server holds
// that the cluster no longer has, things the cluster has that the server never heard of, and things
// both know but describe differently.
type Drift struct {
	Missing int // the cluster has it, the server did not
	Extra   int // the server has it, the cluster does not
	Changed int
	Samples []string
}

func (d Drift) Total() int { return d.Missing + d.Extra + d.Changed }

func (d Drift) String() string {
	var p []string
	if d.Missing > 0 {
		p = append(p, fmt.Sprintf("%d the server had not heard of", d.Missing))
	}
	if d.Extra > 0 {
		p = append(p, fmt.Sprintf("%d it still held but the cluster no longer has", d.Extra))
	}
	if d.Changed > 0 {
		p = append(p, fmt.Sprintf("%d it held in an outdated form", d.Changed))
	}
	return strings.Join(p, ", ")
}

// CheckDrift compares the state as the server holds it with a full picture from the agent. It
// changes nothing; applying the full picture afterwards is what repairs the difference.
func (s *State) CheckDrift(full *continuumv1.Sync) Drift {
	var d Drift
	note := func(kind, key string) {
		if len(d.Samples) < 5 {
			d.Samples = append(d.Samples, kind+" "+key)
		}
	}
	cmp := func(kind string, get func(k string) (proto.Message, bool), incoming map[string]proto.Message, each func(func(string))) {
		seen := map[string]bool{}
		for k, m := range incoming {
			seen[k] = true
			old, ok := get(k)
			switch {
			case !ok:
				d.Missing++
				note(kind, k)
			case !proto.Equal(old, m):
				d.Changed++
				note(kind, k)
			}
		}
		each(func(k string) {
			if !seen[k] {
				d.Extra++
				note(kind, k)
			}
		})
	}
	nodes := map[string]proto.Message{}
	for _, n := range full.Nodes {
		nodes[n.Key] = n
	}
	cmp("node", func(k string) (proto.Message, bool) { m, ok := s.Nodes[k]; return m, ok }, nodes, func(f func(string)) {
		for k := range s.Nodes {
			f(k)
		}
	})
	nss := map[string]proto.Message{}
	for _, n := range full.Namespaces {
		nss[n.Key] = n
	}
	cmp("namespace", func(k string) (proto.Message, bool) { m, ok := s.Namespaces[k]; return m, ok }, nss, func(f func(string)) {
		for k := range s.Namespaces {
			f(k)
		}
	})
	ws := map[string]proto.Message{}
	for _, w := range full.Workloads {
		ws[w.Key] = w
	}
	cmp("workload", func(k string) (proto.Message, bool) { m, ok := s.Workloads[k]; return m, ok }, ws, func(f func(string)) {
		for k := range s.Workloads {
			f(k)
		}
	})
	return d
}
