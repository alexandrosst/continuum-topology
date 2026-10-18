package server

import (
	"bytes"
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

// DecisionAdditions documents what the server adds to a placement decision request before it reaches an external
// decider. Nothing the browser sent is renamed or removed except that clusters which are not eligible targets are
// taken out of every service's "candidates": whatever the client believed, a decider is only ever offered live
// targets, and is told which clusters were left out and why.
//
//	modelVersion  the version of the effective model the check was made against
//	excluded      [{cluster, name, state, reason, kind, fix?}] clusters that are not eligible targets. kind is "not-live" or
//	              "capacity-unknown": a live cluster that reports nothing about its free capacity is not ruled out, it
//	              is "can't tell" (fix says what would change that)
//	clusters[].state, clusters[].stateReason, clusters[].eligible
//	clusters[].free  {cpu, memory} free capacity as facts with an interval, confidence and age
//	services[].clusterState  the state of the cluster the service runs in
//	services[].capacity      the server's own fits / cantTell / doesNotFit for cpu and memory at each other live cluster,
//	                         with the facts used, what would change it and what would fix a "can't tell"
//	services[].candidates    keeps only clusters where that verdict is "fits" (when the server knows the workload)
//
// See docs/advice.md for the rules.
type excludedDoc struct {
	twin.Exclusion
	Fix *advice.Fix `json:"fix,omitempty"`
}

func EnrichDecision(body []byte, m twin.Model) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var req map[string]any
	if err := dec.Decode(&req); err != nil || req == nil {
		return body, nil // not an object: nothing to add to, forwarded as it came
	}
	excl := twin.Exclusions(m)
	bad := map[string]twin.Exclusion{}
	for _, e := range excl {
		bad[e.ClusterID] = e
	}
	state := map[string]twin.Entity{}
	for _, e := range m.Entities {
		if e.Kind == "cluster" {
			state[e.ID] = e
		}
	}
	view := buildCapView(m)
	req["modelVersion"] = m.ModelVersion
	docs := make([]excludedDoc, 0, len(excl))
	for _, e := range excl {
		d := excludedDoc{Exclusion: e}
		if e.Kind == twin.ExcludedCapacityUnknown {
			if c := view.clusters[e.ClusterID]; c != nil {
				d.Fix = c.fixFor(c.pool(advice.CPU, view.now, view.window))
			}
		}
		docs = append(docs, d)
	}
	req["excluded"] = docs
	if cs, ok := req["clusters"].([]any); ok {
		for _, c := range cs {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			id, _ := cm["id"].(string)
			if e, ok := state[id]; ok {
				cm["state"] = string(e.State)
				if e.StateReason != "" {
					cm["stateReason"] = e.StateReason
				}
			}
			x, isBad := bad[id]
			cm["eligible"] = !isBad
			if isBad {
				cm["stateReason"] = x.Reason
			}
			if vc := view.clusters[id]; vc != nil {
				cm["free"] = view.clusterFree(vc)
			}
		}
	}
	// the clusters a workload could be judged against: everything that is not out because it is not live
	var targets []*capCluster
	for id, c := range view.clusters {
		if x, isBad := bad[id]; isBad && x.Kind == twin.ExcludedNotLive {
			continue
		}
		targets = append(targets, c)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })
	docsLeft := maxCapacityDocs
	if ss, ok := req["services"].([]any); ok {
		for _, s := range ss {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			home, _ := sm["cluster"].(string)
			if home != "" {
				if e, ok := state[home]; ok {
					sm["clusterState"] = string(e.State)
				}
			}
			var verdicts map[string]advice.Verdict
			if sid, _ := sm["id"].(string); sid != "" && view.services[sid] != nil {
				if mob, _ := sm["mobility"].(string); mob != "pinned" {
					verdicts = map[string]advice.Verdict{}
					var caps []capacityDoc
					for _, c := range targets {
						if c.id == home {
							continue
						}
						d := view.assess(view.services[sid], c)
						verdicts[c.id] = d.Verdict
						if docsLeft > 0 {
							caps = append(caps, d)
							docsLeft--
						}
					}
					if caps == nil {
						caps = []capacityDoc{}
					}
					sm["capacity"] = caps
					if docsLeft <= 0 {
						req["capacityTruncated"] = true
					}
				}
			}
			if cand, ok := sm["candidates"].([]any); ok {
				kept := make([]any, 0, len(cand))
				for _, c := range cand {
					id, _ := c.(string)
					if isExcluded(bad, id) {
						continue
					}
					if v, judged := verdicts[id]; judged && v != advice.Fits {
						continue
					}
					kept = append(kept, c)
				}
				sm["candidates"] = kept
			}
			// undecided: clusters the server itself cannot certify (verdict cantTell), union'd with whatever the
			// client already believed was undecided. Never invented for a pinned service (verdicts is nil then).
			undecided := map[string]bool{}
			if u, ok := sm["undecided"].([]any); ok {
				for _, c := range u {
					if id, _ := c.(string); id != "" {
						undecided[id] = true
					}
				}
			}
			for id, v := range verdicts {
				if v == advice.CantTell {
					undecided[id] = true
				}
			}
			if len(undecided) > 0 || sm["undecided"] != nil {
				ids := make([]string, 0, len(undecided))
				for id := range undecided {
					if !isExcluded(bad, id) {
						ids = append(ids, id)
					}
				}
				sort.Strings(ids)
				out := make([]any, len(ids))
				for i, id := range ids {
					out[i] = id
				}
				sm["undecided"] = out
			}
		}
	}
	return json.Marshal(req)
}

func isExcluded(bad map[string]twin.Exclusion, id string) bool { _, ok := bad[id]; return ok }
