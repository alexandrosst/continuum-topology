package twin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/model"
	"continuum/internal/store"
	"continuum/internal/workspace"
)

// Contract names the shape of the document Build produces. It changes only for an incompatible change; adding
// fields does not change it (readers must ignore fields they do not know).
const Contract = "continuum.model/v1"

// The reasons the model gives when a capacity figure is unknown. Placement advice recognises them (to say what fixes
// each one), so they are constants rather than free text.
const (
	WhyNodesNotRead  = "nodes are not read at this access tier"
	WhyNoNodes       = "no nodes are known"
	WhyNoAllocatable = "no node reported its allocatable resources"
	WhyPodsNotRead   = "pods are not read at this access tier"
)

// Confidence says how a value came to be known, from most to least certain.
type Confidence string

const (
	// Measured: read from the thing itself by an instrument (a node probe, a timed connection, counted traffic).
	Measured Confidence = "measured"
	// Reported: an agent read it from the Kubernetes API, or a person stated it. Taken at its word.
	Reported Confidence = "reported"
	// Inferred: worked out by a rule from other facts. The signal is named.
	Inferred Confidence = "inferred"
	// Guess: inferred from weak signals; likely to be wrong sometimes.
	Guess Confidence = "guess"
	// Unknown: not known. The value is null, and that is different from zero, empty or false.
	Unknown Confidence = "unknown"
)

// Source says where a value came from.
type Source string

const (
	FromAgent    Source = "agent"    // read from the Kubernetes API by the cluster's agent (agentId says which)
	FromProbe    Source = "probe"    // read from the machine by the node probe
	FromMeasured Source = "measured" // timed or counted by the agent (paths, traffic)
	FromInferred Source = "inferred" // derived by the server from other facts
	FromDeclared Source = "declared" // stated by a person in the workspace
)

// Attr is one fact about one entity.
type Attr struct {
	// Value is null when Confidence is unknown. Never zero-as-unknown.
	Value      any        `json:"value"`
	Unit       string     `json:"unit,omitempty"`
	Source     Source     `json:"source"`
	AgentID    string     `json:"agentId,omitempty"`
	Confidence Confidence `json:"confidence"`
	// ObservedAt is the last time the source vouched for the value (declared values: when the workspace was saved is
	// not tracked, so it is empty).
	ObservedAt string `json:"observedAt,omitempty"`
	// State is the observation state of the entity at generation time, repeated so an attribute can be judged alone.
	State State `json:"state"`
	// Evidence is the signal behind an inferred value, or why a value is unknown.
	Evidence string `json:"evidence,omitempty"`
	// Shadowed is the observed value that a declaration overrides. The declared value wins; nothing is lost.
	Shadowed *Attr `json:"shadowed,omitempty"`
}

// IdentityDoc is what makes an entity the same entity over time.
type IdentityDoc struct {
	// Basis: kube-system-uid | provider-id | system-uuid | machine-id | name | workload | declared
	Basis   string   `json:"basis"`
	Key     string   `json:"key,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// Entity is one thing in the estate.
type Entity struct {
	// Kind: cluster | node | namespace | service | device | site | application | path | dependency | external
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ClusterID string `json:"clusterId,omitempty"`
	// Origin: observed (an agent reports it), declared (a person authored it), or observed+declared (an observed
	// record a person has overridden or assigned).
	Origin string `json:"origin"`
	// State is live | disconnected | stale | revoked | gone for observed entities, declared for authored ones.
	State          State           `json:"state"`
	StateReason    string          `json:"stateReason,omitempty"`
	StateSince     string          `json:"stateSince,omitempty"`
	LastObservedAt string          `json:"lastObservedAt,omitempty"`
	GoneAt         string          `json:"goneAt,omitempty"`
	AgentID        string          `json:"agentId,omitempty"`
	Identity       *IdentityDoc    `json:"identity,omitempty"`
	Attributes     map[string]Attr `json:"attributes"`
}

// ObservationDoc states the rules the states were computed with.
type ObservationDoc struct {
	StaleAfterSeconds      int `json:"staleAfterSeconds"`
	TombstoneRetentionDays int `json:"tombstoneRetentionDays"`
}

// Model is the effective model: declared and observed, combined, with provenance.
type Model struct {
	Contract     string         `json:"contract"`
	ModelVersion int64          `json:"modelVersion"`
	GeneratedAt  string         `json:"generatedAt"`
	Observation  ObservationDoc `json:"observation"`
	Entities     []Entity       `json:"entities"`
	Warnings     []string       `json:"warnings"`
}

// AgentInfo is what the builder needs to know about an agent.
type AgentInfo struct {
	ID        string
	Name      string
	ClusterID string
	Tier      int
}

// Input is everything Build reads. It reads nothing else, so the same input always gives the same model.
type Input struct {
	Now        time.Time
	StaleAfter time.Duration
	Retention  time.Duration
	// Topology is what the interpreter produced for every cluster that is still known (including revoked ones).
	Topology model.Topology
	// Facts are the raw facts by cluster id (exact quantities come from here).
	Facts  map[string]*facts.State
	Agents map[string]AgentInfo
	// Observations are the states by agent id.
	Observations map[string]Observation
	// Nodes are the identity decisions by record id.
	Nodes      map[string]NodeRecord
	Tombstones []store.Tombstone
	Declared   workspace.Declared
}

const (
	gib = float64(1 << 30)
)

type builder struct {
	in       Input
	out      []Entity
	warnings []string
	byID     map[string]string // kind|id -> origin, to spot duplicates
}

// Build computes the effective model. modelVersion is filled in by the caller (it depends on history).
func Build(in Input) Model {
	b := &builder{in: in, byID: map[string]string{}}
	b.observed()
	b.tombstones()
	b.declared()
	b.lookalikes()
	sort.Slice(b.out, func(i, j int) bool {
		if ki, kj := kindOrder(b.out[i].Kind), kindOrder(b.out[j].Kind); ki != kj {
			return ki < kj
		}
		return b.out[i].ID < b.out[j].ID
	})
	sort.Strings(b.warnings)
	w := b.warnings
	if w == nil {
		w = []string{}
	}
	return Model{
		Contract:    Contract,
		GeneratedAt: in.Now.UTC().Format(time.RFC3339),
		Observation: ObservationDoc{StaleAfterSeconds: int(in.StaleAfter / time.Second), TombstoneRetentionDays: int(in.Retention / (24 * time.Hour))},
		Entities:    b.out,
		Warnings:    w,
	}
}

var kindRank = map[string]int{"site": 0, "cluster": 1, "node": 2, "namespace": 3, "application": 4, "service": 5, "device": 6, "external": 7, "path": 8, "dependency": 9}

func kindOrder(k string) int {
	if r, ok := kindRank[k]; ok {
		return r
	}
	return 99
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---- attribute helpers ----

type ctx struct {
	e     *Entity
	obs   Observation
	agent string
}

func (c *ctx) set(name string, a Attr) {
	a.State = c.e.State
	if a.Source == FromAgent || a.Source == FromProbe || a.Source == FromMeasured {
		a.AgentID = c.agent
		a.ObservedAt = ts(c.obs.LastObserved)
	}
	if a.Source == FromInferred {
		a.ObservedAt = ts(c.obs.LastObserved)
	}
	c.e.Attributes[name] = a
}

// rep records a value an agent read from the API.
func (c *ctx) rep(name string, v any, unit string) {
	c.set(name, Attr{Value: v, Unit: unit, Source: FromAgent, Confidence: Reported})
}

// unk records that a value is not known, and why.
func (c *ctx) unk(name, unit, why string) {
	c.set(name, Attr{Value: nil, Unit: unit, Source: FromAgent, Confidence: Unknown, Evidence: why})
}

// inf records a value the server derived, with the evidence behind it.
func (c *ctx) inf(name string, v any, ev model.Evidence, probed bool) {
	a := Attr{Value: v, Source: FromInferred, Evidence: strings.TrimSpace(ev.Signal + evDetail(ev))}
	switch {
	case probed:
		a.Source = FromProbe
		switch ev.Confidence {
		case "high":
			a.Confidence = Measured
		case "medium":
			a.Confidence = Inferred
		default:
			a.Confidence = Guess
		}
	case ev.Confidence == "low":
		a.Confidence = Guess
	default:
		a.Confidence = Inferred
	}
	c.set(name, a)
}

func evDetail(e model.Evidence) string {
	if e.Detail == "" {
		return ""
	}
	return " (" + e.Detail + ")"
}

func cores(millis int64) float64 { return math.Round(float64(millis)) / 1000 }

func newEntity(kind, id, name, clusterID string) Entity {
	return Entity{Kind: kind, ID: id, Name: name, ClusterID: clusterID, Attributes: map[string]Attr{}}
}

func (b *builder) add(e Entity) {
	k := e.Kind + "|" + e.ID
	if prev, dup := b.byID[k]; dup {
		b.warnings = append(b.warnings, fmt.Sprintf("two %s records share the id %s (%s and %s): identities collide; the first one is used", e.Kind, e.ID, prev, e.Origin))
		return
	}
	b.byID[k] = e.Origin
	b.out = append(b.out, e)
}

// ---- observed records ----

func (b *builder) state(e *Entity, agentID string) (Observation, string) {
	obs, ok := b.in.Observations[agentID]
	if !ok {
		obs = Observation{State: Stale, Reason: "no observation of its agent", Age: 0}
	}
	e.State, e.StateReason, e.AgentID = obs.State, obs.Reason, agentID
	e.LastObservedAt = ts(obs.LastObserved)
	return obs, agentID
}

func (b *builder) observed() {
	t := b.in.Topology
	// per-cluster aggregates of node capacity
	type agg struct {
		nodes                    int
		cpu, mem, cpuReq, memReq float64
		known, podsKnown         int
	}
	aggs := map[string]*agg{}
	for i := range t.Nodes {
		n := &t.Nodes[i]
		a := aggs[n.ClusterID]
		if a == nil {
			a = &agg{}
			aggs[n.ClusterID] = a
		}
		a.nodes++
		if f := b.nodeFacts(n); f != nil && (f.CpuAllocatableMillis > 0 || f.MemoryAllocatableBytes > 0) {
			a.known++
			a.cpu += cores(f.CpuAllocatableMillis)
			a.mem += float64(f.MemoryAllocatableBytes)
			if f.PodCount != nil {
				a.podsKnown++
				a.cpuReq += cores(f.CpuRequestedMillis)
				a.memReq += float64(f.MemoryRequestedBytes)
			}
		}
	}

	for i := range t.Clusters {
		cl := &t.Clusters[i]
		e := newEntity("cluster", cl.ID, cl.Name, "")
		e.Origin = "observed"
		obs, agent := b.state(&e, cl.AgentID)
		c := &ctx{e: &e, obs: obs, agent: agent}
		e.Identity = &IdentityDoc{Basis: "kube-system-uid", Key: cl.Key}
		tier := b.in.Agents[agent].Tier
		if cl.Version == "" {
			c.unk("version", "", "the agent did not report the API server version")
		} else {
			c.rep("version", cl.Version, "")
		}
		if cl.APIEndpoint != "" {
			c.rep("apiEndpoint", cl.APIEndpoint, "")
		}
		if cl.CreatedAt != "" {
			c.rep("createdAt", cl.CreatedAt, "")
		}
		b.evAttr(c, "distribution", cl.Distribution, cl.Evidence["distribution"], false)
		b.evAttr(c, "provider", cl.Provider, cl.Evidence["provider"], false)
		b.evAttr(c, "tier", cl.Tier, cl.Evidence["tier"], false)
		if cl.Region != "" {
			b.evAttr(c, "region", cl.Region, cl.Evidence["region"], false)
		} else {
			c.unk("region", "", "no node carries a region label")
		}
		if cl.PodCIDR != "" {
			b.evAttr(c, "podCidr", cl.PodCIDR, cl.Evidence["podCidr"], false)
		}
		if cl.CNI != "" {
			c.inf("cni", cl.CNI, model.Evidence{Signal: "workloads running in the cluster", Confidence: "medium"}, false)
		}
		if cl.Ingress != "" {
			c.inf("ingress", cl.Ingress, model.Evidence{Signal: "workloads and ingress classes in the cluster", Confidence: "medium"}, false)
		}
		if len(cl.StorageClasses) > 0 {
			c.rep("storageClasses", cl.StorageClasses, "")
		}
		if cl.Status == "unknown" {
			c.unk("status", "", "no nodes are known")
		} else {
			c.inf("status", cl.Status, model.Evidence{Signal: "readiness of the cluster's nodes", Confidence: "high"}, false)
		}
		if a := aggs[cl.ID]; tier < 1 || a == nil || a.nodes == 0 {
			why := WhyNodesNotRead
			if tier >= 1 {
				why = WhyNoNodes
			}
			for _, n := range []struct{ name, unit string }{{"nodeCount", "count"}, {"cpuAllocatable", "cores"}, {"memoryAllocatable", "bytes"}, {"cpuRequested", "cores"}, {"memoryRequested", "bytes"}} {
				c.unk(n.name, n.unit, why)
			}
		} else {
			c.rep("nodeCount", a.nodes, "count")
			if a.known == 0 {
				c.unk("cpuAllocatable", "cores", WhyNoAllocatable)
				c.unk("memoryAllocatable", "bytes", WhyNoAllocatable)
			} else {
				c.rep("cpuAllocatable", round3(a.cpu), "cores")
				c.rep("memoryAllocatable", a.mem, "bytes")
			}
			if a.podsKnown == 0 || a.podsKnown < a.known {
				c.unk("cpuRequested", "cores", "pods are not read for every node (access tier below 2)")
				c.unk("memoryRequested", "bytes", "pods are not read for every node (access tier below 2)")
			} else {
				c.rep("cpuRequested", round3(a.cpuReq), "cores")
				c.rep("memoryRequested", a.memReq, "bytes")
			}
		}
		b.add(e)
	}

	for i := range t.Nodes {
		n := &t.Nodes[i]
		e := newEntity("node", n.ID, n.Name, n.ClusterID)
		e.Origin = "observed"
		obs, agent := b.state(&e, n.AgentID)
		c := &ctx{e: &e, obs: obs, agent: agent}
		if r, ok := b.in.Nodes[n.ID]; ok {
			e.Identity = &IdentityDoc{Basis: string(r.Identity.Basis), Key: r.Identity.Digest, Aliases: r.Aliases}
			if r.Identity.Basis == BasisName {
				e.Identity.Key = ""
			}
			if r.Replaced {
				e.Identity.Note = "a different machine than the one that used this name before: it has its own record"
			}
		} else {
			e.Identity = &IdentityDoc{Basis: string(BasisName)}
		}
		f := b.nodeFacts(n)
		switch {
		case f == nil:
			c.rep("cpu", n.CPU, "cores")
		default:
			if f.CpuCapacityMillis > 0 {
				c.rep("cpuCapacity", cores(f.CpuCapacityMillis), "cores")
			} else {
				c.unk("cpuCapacity", "cores", "the node reported no CPU capacity")
			}
			if f.MemoryCapacityBytes > 0 {
				c.rep("memoryCapacity", float64(f.MemoryCapacityBytes), "bytes")
			} else {
				c.unk("memoryCapacity", "bytes", "the node reported no memory capacity")
			}
			if f.CpuAllocatableMillis > 0 || f.MemoryAllocatableBytes > 0 {
				c.rep("cpuAllocatable", cores(f.CpuAllocatableMillis), "cores")
				c.rep("memoryAllocatable", float64(f.MemoryAllocatableBytes), "bytes")
			} else {
				c.unk("cpuAllocatable", "cores", "the node reported no allocatable resources")
				c.unk("memoryAllocatable", "bytes", "the node reported no allocatable resources")
			}
			if f.PodCount != nil {
				c.rep("podCount", int(*f.PodCount), "count")
				c.rep("cpuRequested", cores(f.CpuRequestedMillis), "cores")
				c.rep("memoryRequested", float64(f.MemoryRequestedBytes), "bytes")
			} else {
				why := WhyPodsNotRead
				c.unk("podCount", "count", why)
				c.unk("cpuRequested", "cores", why)
				c.unk("memoryRequested", "bytes", why)
			}
			if f.PodCapacity > 0 {
				c.rep("podCapacity", int(f.PodCapacity), "count")
			}
			c.rep("ready", f.Ready, "")
			if len(f.ExtendedResources) > 0 || len(n.Accelerators) > 0 {
				c.rep("accelerators", n.Accelerators, "")
			}
		}
		c.rep("role", n.Role, "")
		c.rep("status", n.Status, "")
		b.evAttr(c, "kind", n.Kind, n.Evidence["kind"], n.Probed)
		if n.HardwareModel != "" {
			b.evAttr(c, "hardwareModel", n.HardwareModel, n.Evidence["hardwareModel"], n.Probed)
		}
		if n.Probed {
			b.evAttr(c, "virtualization", n.Virtualization, n.Evidence["virtualization"], true)
			b.evAttr(c, "connectivity", n.Connectivity, n.Evidence["connectivity"], true)
			c.set("hasBattery", Attr{Value: n.HasBattery, Source: FromProbe, Confidence: Measured})
		}
		for name, v := range map[string]string{"arch": n.Arch, "os": n.OS, "kernel": n.Kernel, "runtime": n.Runtime, "kubeletVersion": n.KubeletVersion} {
			if v == "" {
				c.unk(name, "", "the node did not report it")
			} else {
				c.rep(name, v, "")
			}
		}
		if n.IP != "" {
			c.rep("ip", n.IP, "")
		}
		if n.InstanceType != "" {
			c.rep("instanceType", n.InstanceType, "")
		}
		if n.Zone != "" {
			c.rep("zone", n.Zone, "")
		}
		c.rep("taints", nonNil(n.Taints), "")
		if len(n.Conditions) > 0 {
			c.rep("conditions", n.Conditions, "")
		}
		b.add(e)
	}

	for i := range t.Namespaces {
		n := &t.Namespaces[i]
		e := newEntity("namespace", n.ID, n.Name, n.ClusterID)
		e.Origin = "observed"
		obs, agent := b.state(&e, n.AgentID)
		c := &ctx{e: &e, obs: obs, agent: agent}
		if n.Mesh != "" {
			c.rep("mesh", n.Mesh, "")
		}
		if n.Mtls != "" {
			c.rep("mtls", n.Mtls, "")
		}
		b.add(e)
	}

	for i := range t.Services {
		s := &t.Services[i]
		e := newEntity("service", s.ID, s.Name, s.ClusterID)
		e.Origin = "observed"
		obs, agent := b.state(&e, s.AgentID)
		c := &ctx{e: &e, obs: obs, agent: agent}
		e.Identity = &IdentityDoc{Basis: "workload", Key: s.Key}
		c.rep("namespace", s.Namespace, "")
		c.rep("kind", s.Kind, "")
		c.rep("replicas", int(s.Replicas), "count")
		c.rep("readyReplicas", int(s.ReadyReplicas), "count")
		if s.Status == "unknown" {
			c.unk("status", "", "the workload has no replicas to judge")
		} else {
			c.inf("status", s.Status, model.Evidence{Signal: "ready replicas against wanted replicas", Confidence: "high"}, false)
		}
		w := b.workloadFacts(s)
		cpuReq, memReq, cpuLim, memLim := s.CPURequestM, s.MemRequestMi<<20, s.CPULimitM, s.MemLimitMi<<20
		if w != nil {
			cpuReq, memReq, cpuLim, memLim = w.CpuRequestMillis, w.MemoryRequestBytes, w.CpuLimitMillis, w.MemoryLimitBytes
		}
		perReplica := " (per replica, summed over containers)"
		if cpuReq > 0 {
			c.rep("cpuRequest", cores(cpuReq), "cores")
		} else {
			c.unk("cpuRequest", "cores", "no CPU request is set, so nothing is reserved and the need is not known")
		}
		if memReq > 0 {
			c.rep("memoryRequest", float64(memReq), "bytes")
		} else {
			c.unk("memoryRequest", "bytes", "no memory request is set, so nothing is reserved and the need is not known")
		}
		c.set("cpuLimit", limitAttr(cpuLim > 0, cores(cpuLim), "cores", perReplica))
		c.set("memoryLimit", limitAttr(memLim > 0, float64(memLim), "bytes", perReplica))
		if s.Image != "" {
			c.rep("image", s.Image, "")
		}
		if s.Exposure != "" {
			c.rep("exposure", s.Exposure, "")
		}
		if len(s.Ports) > 0 {
			c.rep("ports", s.Ports, "")
		}
		c.rep("restarts", int(s.Restarts), "count")
		if len(s.NodeIDs) > 0 {
			c.rep("nodeIds", s.NodeIDs, "")
		}
		var vol float64
		for _, v := range s.Volumes {
			vol += v.SizeGb * gib
		}
		c.rep("volumeBytes", math.Round(vol), "bytes")
		if s.ApplicationHint != "" {
			c.inf("applicationId", s.ApplicationHint, s.Evidence["application"], false)
		}
		b.add(e)
	}

	for i := range t.ExternalEndpoints {
		x := &t.ExternalEndpoints[i]
		e := newEntity("external", x.ID, x.Host, "")
		e.Origin = "observed"
		e.State = Live
		if x.AgentID != "" {
			b.state(&e, x.AgentID)
		}
		c := &ctx{e: &e, obs: b.in.Observations[x.AgentID], agent: x.AgentID}
		c.rep("host", x.Host, "")
		if x.Port > 0 {
			c.rep("port", x.Port, "")
		}
		c.inf("kind", x.Kind, model.Evidence{Signal: "the address seen in traffic", Confidence: "low"}, false)
		b.add(e)
	}

	for i := range t.Paths {
		p := &t.Paths[i]
		e := newEntity("path", p.ID, p.FromName+" → "+p.Host, p.FromCluster)
		e.Origin = "observed"
		// A path is as fresh as its last measurement, and no fresher than its agent.
		agent := ""
		for _, c := range t.Clusters {
			if c.ID == p.FromCluster {
				agent = c.AgentID
			}
		}
		obs, _ := b.state(&e, agent)
		if p.Stale && obs.State == Live {
			e.State, e.StateReason = Stale, "no recent measurement"
		}
		if at, err := time.Parse(time.RFC3339, p.At); err == nil {
			obs.LastObserved = at
			e.LastObservedAt = ts(at)
		}
		c := &ctx{e: &e, obs: obs, agent: agent}
		m := func(name string, v float64, unit string) {
			c.set(name, Attr{Value: v, Unit: unit, Source: FromMeasured, Confidence: Measured})
		}
		m("rttMin", p.RTTMin, "ms")
		m("rttP50", p.RTTP50, "ms")
		m("rttP95", p.RTTP95, "ms")
		m("loss", p.LossPct, "percent")
		c.set("samples", Attr{Value: p.Samples, Unit: "count", Source: FromMeasured, Confidence: Measured})
		c.set("host", Attr{Value: p.Host, Source: FromMeasured, Confidence: Reported})
		if p.ToCluster != "" {
			c.set("toCluster", Attr{Value: p.ToCluster, Source: FromMeasured, Confidence: Inferred, Evidence: "the address belongs to another onboarded cluster"})
		}
		b.add(e)
	}

	for i := range t.Dependencies {
		d := &t.Dependencies[i]
		e := newEntity("dependency", d.ID, d.Label, "")
		if e.Name == "" {
			e.Name = d.From + " → " + d.To
		}
		e.Origin = "observed"
		e.State = Live
		if d.Stale {
			e.State, e.StateReason = Stale, "no traffic seen for longer than the quiet window"
		}
		if at, err := time.Parse(time.RFC3339, d.LastSeen); err == nil {
			e.LastObservedAt = ts(at)
		}
		c := &ctx{e: &e, obs: Observation{State: e.State}}
		c.set("from", Attr{Value: d.From, Source: FromMeasured, Confidence: Reported})
		c.set("to", Attr{Value: d.To, Source: FromMeasured, Confidence: Reported})
		c.set("protocol", Attr{Value: d.Protocol, Source: FromMeasured, Confidence: Reported})
		if d.Port > 0 {
			c.set("port", Attr{Value: d.Port, Source: FromMeasured, Confidence: Reported})
		}
		if d.Stats != nil && d.Stats.BytesPerSec > 0 {
			c.set("bytesPerSec", Attr{Value: d.Stats.BytesPerSec, Unit: "bytes/s", Source: FromMeasured, Confidence: Measured})
		} else {
			c.set("bytesPerSec", Attr{Value: nil, Unit: "bytes/s", Source: FromMeasured, Confidence: Unknown, Evidence: "no byte counts for this link (the collector could not count them, or nothing was sent)"})
		}
		if d.Stats != nil && d.Stats.ConnectionsPerMin > 0 {
			c.set("connectionsPerMin", Attr{Value: d.Stats.ConnectionsPerMin, Unit: "per minute", Source: FromMeasured, Confidence: Measured})
		}
		if d.Noise != "" {
			c.set("noise", Attr{Value: d.Noise, Source: FromMeasured, Confidence: Inferred, Evidence: "classified as machinery traffic, not the applications' own"})
		}
		b.add(e)
	}
}

func limitAttr(set bool, v float64, unit, note string) Attr {
	if !set {
		return Attr{Value: nil, Unit: unit, Source: FromAgent, Confidence: Reported, Evidence: "no limit is set"}
	}
	return Attr{Value: v, Unit: unit, Source: FromAgent, Confidence: Reported, Evidence: strings.TrimSpace(note)}
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// evAttr records a value the interpreter derived; without evidence it is only "inferred" with no signal.
func (b *builder) evAttr(c *ctx, name string, v any, ev model.Evidence, probed bool) {
	if ev.Signal == "" {
		ev = model.Evidence{Signal: "the agent's facts", Confidence: "medium"}
	}
	if s, ok := v.(string); ok && s == "" {
		c.unk(name, "", "not detected")
		return
	}
	c.inf(name, v, ev, probed)
}

func (b *builder) nodeFacts(n *model.Node) *continuumv1.NodeFacts {
	st := b.in.Facts[n.ClusterID]
	if st == nil {
		return nil
	}
	return st.Nodes[strings.TrimPrefix(n.Key, n.ClusterID+"/node/")]
}

func (b *builder) workloadFacts(s *model.Service) *continuumv1.WorkloadFacts {
	st := b.in.Facts[s.ClusterID]
	if st == nil {
		return nil
	}
	return st.Workloads[strings.TrimPrefix(s.Key, s.ClusterID+"/")]
}

// ---- tombstones ----

func (b *builder) tombstones() {
	for _, t := range b.in.Tombstones {
		if b.in.Now.Sub(t.GoneAt) > b.in.Retention {
			continue
		}
		e := newEntity(t.Kind, t.ID, t.Name, t.ClusterID)
		e.Origin = "observed"
		e.State = Gone
		e.StateReason = t.Reason
		e.GoneAt, e.StateSince, e.AgentID = ts(t.GoneAt), ts(t.GoneAt), t.AgentID
		e.LastObservedAt = ts(t.LastSeen)
		// A record that is back under the same id is live; the tombstone is stale news.
		if _, live := b.byID[t.Kind+"|"+t.ID]; live {
			continue
		}
		b.add(e)
	}
}

// ---- declared records and precedence ----

// declaredFields maps the names the workspace uses for a field to the model's attribute name and unit.
var declaredFields = map[string]struct {
	Attr, Unit string
	Scale      float64
}{
	"cpu":          {"cpuCapacity", "cores", 1},
	"memoryGb":     {"memoryCapacity", "bytes", gib},
	"cpuRequestM":  {"cpuRequest", "cores", 0.001},
	"cpuLimitM":    {"cpuLimit", "cores", 0.001},
	"memRequestMi": {"memoryRequest", "bytes", 1 << 20},
	"memLimitMi":   {"memoryLimit", "bytes", 1 << 20},
	"replicas":     {"replicas", "count", 1},
	"rttMs":        {"rtt", "ms", 1},
}

// metaFields are bookkeeping in a record, never a fact about the thing.
var metaFields = map[string]bool{"id": true, "orgId": true, "source": true, "key": true, "lastSeen": true, "detectedAt": true, "agentId": true,
	"revision": true, "stale": true, "deletedAt": true, "evidence": true, "overrides": true, "labels": true, "clusterId": true, "name": true}

func attrFor(field string, v any) (string, Attr) {
	a := Attr{Value: v, Source: FromDeclared, Confidence: Reported}
	name := field
	if d, ok := declaredFields[field]; ok {
		name, a.Unit = d.Attr, d.Unit
		if f, ok := v.(float64); ok {
			a.Value = f * d.Scale
		}
	}
	return name, a
}

func (b *builder) declared() {
	d := b.in.Declared
	// what a person said about observed records
	idx := map[string]int{}
	for i := range b.out {
		idx[b.out[i].Kind+"|"+b.out[i].ID] = i
	}
	ids := make([]string, 0, len(d.Refs))
	for id := range d.Refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ref := d.Refs[id]
		i, ok := idx[ref.Kind+"|"+id]
		if !ok {
			continue // a reference to something no longer known: kept in the workspace, nothing to apply to
		}
		e := &b.out[i]
		e.Origin = "observed+declared"
		set := func(field string, v any) {
			name, a := attrFor(field, v)
			a.State = e.State
			if field == "name" {
				if s, ok := v.(string); ok && s != "" {
					e.Name = s
				}
			}
			if prev, ok := e.Attributes[name]; ok {
				p := prev
				p.Shadowed = nil
				a.Shadowed = &p
			}
			e.Attributes[name] = a
		}
		keys := make([]string, 0, len(ref.Overrides))
		for k := range ref.Overrides {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			set(k, ref.Overrides[k])
		}
		if ref.SiteID != "" {
			set("siteId", ref.SiteID)
		}
		if ref.ApplicationID != "" {
			set("applicationId", ref.ApplicationID)
		}
	}
	// what a person authored
	for _, kind := range []string{"site", "cluster", "node", "namespace", "service", "device", "application"} {
		for _, r := range d.Records[kind] {
			id, _ := r["id"].(string)
			if id == "" {
				continue
			}
			if _, deleted := r["deletedAt"]; deleted {
				continue
			}
			name, _ := r["name"].(string)
			cid, _ := r["clusterId"].(string)
			e := newEntity(kind, id, name, cid)
			e.Origin, e.State = "declared", Declared
			e.Identity = &IdentityDoc{Basis: "declared"}
			keys := make([]string, 0, len(r))
			for k := range r {
				if !metaFields[k] {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				n, a := attrFor(k, r[k])
				a.State = e.State
				e.Attributes[n] = a
			}
			if ov, ok := r["overrides"].(map[string]any); ok {
				for k, v := range ov {
					n, a := attrFor(k, v)
					a.State = e.State
					e.Attributes[n] = a
				}
			}
			b.add(e)
		}
	}
}

// lookalikes notes clusters that carry the same name but are different clusters, so nobody assumes one is the
// other's successor and nothing is merged.
func (b *builder) lookalikes() {
	byName := map[string][]int{}
	for i := range b.out {
		if b.out[i].Kind == "cluster" && b.out[i].State != Gone {
			byName[strings.ToLower(b.out[i].Name)] = append(byName[strings.ToLower(b.out[i].Name)], i)
		}
	}
	for _, idxs := range byName {
		if len(idxs) < 2 {
			continue
		}
		var ids []string
		for _, i := range idxs {
			ids = append(ids, b.out[i].ID)
		}
		sort.Strings(ids)
		for _, i := range idxs {
			e := &b.out[i]
			if e.Identity == nil {
				e.Identity = &IdentityDoc{}
			}
			var others []string
			for _, id := range ids {
				if id != e.ID {
					others = append(others, id)
				}
			}
			e.Identity.Note = "another cluster with the same name (" + strings.Join(others, ", ") + ") exists: a different cluster, not merged"
		}
		b.warnings = append(b.warnings, fmt.Sprintf("%d clusters are called %q (%s): they are different clusters (different identities) and are kept apart", len(idxs), b.out[idxs[0]].Name, strings.Join(ids, ", ")))
	}
}

// ---- version fingerprint and exclusions ----

// Fingerprint identifies the content of the model, ignoring everything that changes only with the clock: the
// generation time, observation times and the ages written into reasons. Two models with equal fingerprints are
// the same model for a planner.
func Fingerprint(m Model) string {
	h := sha256.New()
	w := func(parts ...any) { fmt.Fprintln(h, parts...) }
	w(m.Contract, m.Observation.StaleAfterSeconds, m.Observation.TombstoneRetentionDays)
	for _, e := range m.Entities {
		w("e", e.Kind, e.ID, e.Name, e.ClusterID, e.Origin, e.State, e.StateSince, e.GoneAt, e.AgentID)
		if e.Identity != nil {
			w("i", e.Identity.Basis, e.Identity.Key, strings.Join(e.Identity.Aliases, ","), e.Identity.Note)
		}
		names := make([]string, 0, len(e.Attributes))
		for n := range e.Attributes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			a := e.Attributes[n]
			v, _ := json.Marshal(a.Value)
			w("a", n, string(v), a.Unit, a.Source, a.AgentID, a.Confidence, a.State, a.Evidence)
			if a.Shadowed != nil {
				sv, _ := json.Marshal(a.Shadowed.Value)
				w("s", n, string(sv))
			}
		}
	}
	for _, x := range m.Warnings {
		w("w", x)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Exclusion is a cluster that must not be a target of a placement, and why.
type Exclusion struct {
	ClusterID string `json:"cluster"`
	Name      string `json:"name"`
	State     State  `json:"state"`
	Reason    string `json:"reason"`
	// Kind says which kind of exclusion it is: "not-live" (the cluster is stale, disconnected, revoked or gone) or
	// "capacity-unknown" (it is live, but nothing says what it has free: placement advice treats it as "can't tell"
	// rather than as a place that is ruled out).
	Kind string `json:"kind"`
}

// The kinds of Exclusion.
const (
	ExcludedNotLive         = "not-live"
	ExcludedCapacityUnknown = "capacity-unknown"
)

// Exclusions lists the clusters that are not eligible targets: anything not live, and observed clusters whose
// capacity is unknown (a target whose room cannot be checked is not a target, and unknown is not zero).
func Exclusions(m Model) []Exclusion {
	var out []Exclusion
	for _, e := range m.Entities {
		if e.Kind != "cluster" {
			continue
		}
		switch {
		case e.State == Gone:
			out = append(out, Exclusion{e.ID, e.Name, e.State, "the cluster is gone: " + e.StateReason, ExcludedNotLive})
		case !e.State.Actionable():
			out = append(out, Exclusion{e.ID, e.Name, e.State, e.StateReason, ExcludedNotLive})
		case e.Origin != "declared" && e.Attributes["cpuAllocatable"].Confidence == Unknown:
			out = append(out, Exclusion{e.ID, e.Name, e.State, "capacity unknown: " + e.Attributes["cpuAllocatable"].Evidence, ExcludedCapacityUnknown})
		}
	}
	return out
}
