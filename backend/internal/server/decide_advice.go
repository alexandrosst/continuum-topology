package server

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"continuum/internal/advice"
	"continuum/internal/twin"
)

// What the server adds to a decision request about capacity, worked out from the effective model and the rules in
// package advice (see docs/advice.md). The browser computes the same rules from its own copy of the model and sends
// its complete view as services[].advice; this is the server's own reading of cpu and memory, from the model it
// holds, and is the one a decider should not have to trust the browser for.
//
//	clusters[].free      { cpu, memory }: free capacity as a fact with an interval, class, age and source
//	services[].capacity  [{cluster, verdict, confidence, dimensions, facts, wouldChange, fixes, reasons}]
//	                     for every live or declared cluster the service does not run in
//	services[].candidates keeps only clusters where the server's own verdict is "fits"
//
// The server's pool is every node of the cluster: it does not know the workload's node selectors, which the browser
// applies. Where the two differ, the worse verdict is the one to believe.

const maxCapacityDocs = 4000

type capNode struct {
	id, name string
	cpu, mem advice.Node
}

type capCluster struct {
	id, name, state string
	origin          string
	nodes           []capNode
	// why explains a capacity that is unknown at the cluster level, from the cluster's own attributes.
	whyAlloc, whyReq string
}

type capService struct {
	id, name, clusterID, state string
	cpu, mem                   advice.Need
	cpuAt, memAt               string
}

type capView struct {
	now      time.Time
	window   time.Duration
	clusters map[string]*capCluster
	services map[string]*capService
}

func attrNum(a twin.Attr) (*float64, bool) {
	switch v := a.Value.(type) {
	case float64:
		return &v, true
	case float32:
		f := float64(v)
		return &f, true
	case int:
		f := float64(v)
		return &f, true
	case int64:
		f := float64(v)
		return &f, true
	case json.Number:
		f, err := v.Float64()
		return &f, err == nil
	}
	return nil, false
}

func attrTime(a twin.Attr) time.Time {
	t, _ := time.Parse(time.RFC3339, a.ObservedAt)
	return t
}

// factOf turns a model attribute into an advice fact. A missing attribute, or one whose value is null, is unknown.
func factOf(name string, e twin.Entity, a twin.Attr, ok bool) advice.Fact {
	f := advice.Fact{Attribute: name, Entity: e.ID, EntityName: e.Name, Class: advice.Unknown, State: string(e.State)}
	if !ok {
		return f
	}
	f.Unit, f.Source, f.Evidence, f.ObservedAt = a.Unit, string(a.Source), a.Evidence, attrTime(a)
	if a.Confidence == twin.Unknown {
		return f
	}
	if v, isNum := attrNum(a); isNum {
		f.Value, f.Class = v, advice.Class(a.Confidence)
	}
	return f
}

// declaredResources reads {"cpu": 8, "memoryGb": 32} from a declared node's allocatable or requested attribute.
func declaredResources(a twin.Attr, ok bool) (cpu, mem *float64) {
	if !ok {
		return nil, nil
	}
	m, isMap := a.Value.(map[string]any)
	if !isMap {
		return nil, nil
	}
	num := func(k string) *float64 {
		switch v := m[k].(type) {
		case float64:
			return &v
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return &f
			}
		}
		return nil
	}
	cpu, mem = num("cpu"), num("memoryGb")
	if mem != nil {
		b := *mem * (1 << 30)
		mem = &b
	}
	return cpu, mem
}

func buildCapView(m twin.Model) *capView {
	now, _ := time.Parse(time.RFC3339, m.GeneratedAt)
	v := &capView{now: now, window: time.Duration(m.Observation.StaleAfterSeconds) * time.Second, clusters: map[string]*capCluster{}, services: map[string]*capService{}}
	for _, e := range m.Entities {
		if e.Kind == "cluster" {
			c := &capCluster{id: e.ID, name: e.Name, state: string(e.State), origin: e.Origin}
			c.whyAlloc = e.Attributes["cpuAllocatable"].Evidence
			c.whyReq = e.Attributes["cpuRequested"].Evidence
			v.clusters[e.ID] = c
		}
	}
	for _, e := range m.Entities {
		switch e.Kind {
		case "node":
			c := v.clusters[e.ClusterID]
			if c == nil || e.State == twin.Gone {
				continue
			}
			n := capNode{id: e.ID, name: e.Name}
			if e.Origin == "declared" {
				a, aok := e.Attributes["allocatable"]
				r, rok := e.Attributes["requested"]
				ac, am := declaredResources(a, aok)
				rc, rm := declaredResources(r, rok)
				mk := func(attr string, val *float64, unit string) advice.Fact {
					f := advice.Fact{Attribute: attr, Entity: e.ID, EntityName: e.Name, Unit: unit, Source: "declared", State: string(e.State), Class: advice.Unknown}
					if val != nil {
						f.Value, f.Class = val, advice.Reported
					}
					return f
				}
				n.cpu = advice.Node{ID: e.ID, Name: e.Name, Allocatable: mk("cpuAllocatable", ac, "cores"), Requested: mk("cpuRequested", rc, "cores"), RequestedMissing: "the workspace does not say what is already requested"}
				n.mem = advice.Node{ID: e.ID, Name: e.Name, Allocatable: mk("memoryAllocatable", am, "bytes"), Requested: mk("memoryRequested", rm, "bytes"), RequestedMissing: "the workspace does not say what is already requested"}
			} else {
				ca, aok := e.Attributes["cpuAllocatable"]
				cr, rok := e.Attributes["cpuRequested"]
				ma, maok := e.Attributes["memoryAllocatable"]
				mr, mrok := e.Attributes["memoryRequested"]
				n.cpu = advice.Node{ID: e.ID, Name: e.Name, Allocatable: factOf("cpuAllocatable", e, ca, aok), Requested: factOf("cpuRequested", e, cr, rok), RequestedMissing: cr.Evidence}
				n.mem = advice.Node{ID: e.ID, Name: e.Name, Allocatable: factOf("memoryAllocatable", e, ma, maok), Requested: factOf("memoryRequested", e, mr, mrok), RequestedMissing: mr.Evidence}
			}
			c.nodes = append(c.nodes, n)
		case "service":
			if e.State == twin.Gone {
				continue
			}
			s := &capService{id: e.ID, name: e.Name, clusterID: e.ClusterID, state: string(e.State)}
			rep := 1.0
			if a, ok := e.Attributes["replicas"]; ok {
				if r, isNum := attrNum(a); isNum && *r > 1 {
					rep = *r
				}
			}
			need := func(name string) (advice.Need, string) {
				a, ok := e.Attributes[name]
				if !ok || a.Confidence == twin.Unknown {
					return advice.Need{Class: advice.Unknown}, ""
				}
				val, isNum := attrNum(a)
				if !isNum {
					return advice.Need{Class: advice.Unknown}, ""
				}
				t := *val * rep
				return advice.Need{Value: &t, Class: advice.Class(a.Confidence)}, a.ObservedAt
			}
			s.cpu, s.cpuAt = need("cpuRequest")
			s.mem, s.memAt = need("memoryRequest")
			v.services[e.ID] = s
		}
	}
	for _, c := range v.clusters {
		sort.Slice(c.nodes, func(i, j int) bool { return c.nodes[i].id < c.nodes[j].id })
	}
	return v
}

func (c *capCluster) pool(dim advice.Dimension, now time.Time, window time.Duration) advice.Pool {
	nodes := make([]advice.Node, 0, len(c.nodes))
	for _, n := range c.nodes {
		if dim.Name == "cpu" {
			nodes = append(nodes, n.cpu)
		} else {
			nodes = append(nodes, n.mem)
		}
	}
	p := advice.NewPool(nodes, now, window)
	if len(nodes) == 0 && c.whyAlloc != "" {
		p.Notes = []string{c.whyAlloc}
	}
	return p
}

// fixFor says what a person can do about the reason a capacity figure is missing.
func (c *capCluster) fixFor(p advice.Pool) *advice.Fix {
	if p.NominalKnown || p.KnownNodes > 0 && p.Hi < math.Inf(1) {
		return nil
	}
	switch {
	case len(c.nodes) == 0 && c.whyAlloc == twin.WhyNodesNotRead:
		return &advice.Fix{Action: "raise-agent-tier", Text: "Raise the agent's access tier to 1 or higher so it reads the cluster's nodes and what they have free", Link: "/agents"}
	case len(c.nodes) == 0 && c.origin == "declared":
		return &advice.Fix{Action: "declare-capacity", Text: "Say what the cluster's nodes have (allocatable CPU and memory) and what is already requested, or connect an agent that reports them", Link: "/nodes"}
	case len(c.nodes) == 0:
		return &advice.Fix{Action: "connect-agent", Text: "No node is reported for this cluster: check that its agent is connected and approved", Link: "/agents"}
	}
	for _, n := range c.nodes {
		if !n.cpu.Allocatable.Known() {
			return &advice.Fix{Action: "check-agent", Text: "A node reports no allocatable resources: check the agent's permissions and version", Link: "/agents"}
		}
	}
	if c.whyReq == twin.WhyPodsNotRead || strings.Contains(c.whyReq, "access tier") {
		return &advice.Fix{Action: "raise-agent-tier", Text: "Raise the agent's access tier to 2 so it reads pods and can say what is already requested", Link: "/agents"}
	}
	if c.origin == "declared" {
		return &advice.Fix{Action: "declare-capacity", Text: "Say what is already requested on the cluster's nodes, or connect an agent that reports it", Link: "/nodes"}
	}
	return &advice.Fix{Action: "raise-agent-tier", Text: "Raise the agent's access tier to 2 so it reads pods and can say what is already requested", Link: "/agents"}
}

// capacityDoc is the server's verdict for one service at one cluster.
type capacityDoc struct {
	Cluster string `json:"cluster"`
	Name    string `json:"name"`
	advice.Doc
}

// assess judges cpu and memory of a service at a cluster, with the facts it used.
func (v *capView) assess(s *capService, c *capCluster) capacityDoc {
	where := "cluster " + c.name
	var dims []advice.DimResult
	var facts []advice.FactDoc
	for _, d := range []struct {
		dim  advice.Dimension
		need advice.Need
		at   string
	}{{advice.CPU, s.cpu, s.cpuAt}, {advice.Memory, s.mem, s.memAt}} {
		p := c.pool(d.dim, v.now, v.window)
		r := advice.Fit(d.dim, d.need, p, where)
		if r.Verdict == advice.CantTell {
			if d.need.Value == nil {
				r.Fix = &advice.Fix{Action: "set-requests", Text: fmt.Sprintf("Set a %s request on %s so what it needs is known", d.dim.Name, s.name)}
			} else {
				r.Fix = c.fixFor(p)
			}
		}
		dims = append(dims, r)
		ev := "allocatable minus requested over " + fmt.Sprint(p.Nodes) + " node(s)"
		if len(p.Notes) > 0 {
			ev = strings.Join(p.Notes, "; ")
		}
		facts = append(facts, advice.FreeFact(d.dim, p, c.id, c.name, c.state, ev), advice.NeedFact(d.dim, d.need, s.id, s.name, s.state, d.at))
	}
	a := advice.Assess(dims, nil)
	if a.Verdict == advice.DoesNotFit {
		facts = nil // the reason says why; the facts are kept for the answers a decider might act on
		for _, r := range dims {
			if r.Verdict == advice.DoesNotFit {
				facts = append(facts, advice.FreeFact(r.Dimension, r.Pool, c.id, c.name, c.state, strings.Join(r.Pool.Notes, "; ")))
			}
		}
	}
	return capacityDoc{Cluster: c.id, Name: c.name, Doc: a.Document(facts)}
}

// clusterFree is clusters[].free: the free capacity of a cluster as facts.
func (v *capView) clusterFree(c *capCluster) map[string]advice.FactDoc {
	out := map[string]advice.FactDoc{}
	for _, dim := range []advice.Dimension{advice.CPU, advice.Memory} {
		p := c.pool(dim, v.now, v.window)
		ev := "allocatable minus requested over " + fmt.Sprint(p.Nodes) + " node(s)"
		if len(p.Notes) > 0 {
			ev = strings.Join(p.Notes, "; ")
		}
		out[dim.Name] = advice.FreeFact(dim, p, c.id, c.name, c.state, ev)
	}
	return out
}
