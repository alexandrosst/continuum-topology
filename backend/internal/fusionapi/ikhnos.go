package fusionapi

import (
	"context"
	"fmt"
	"sort"
	"time"

	"continuum/internal/model"
)

// What Ikhnos itself knows about the things a trace ran on, joined to the fused object: which services each service
// calls and is called by (the observed topology), and what changed around the trace (scalings, restarts, node and
// cluster events). The data lives in the server, not in the three stores, so the server hands it over through Extras.

// Keys of Fused.Sources for these parts.
const (
	SourceTopology = "topology"
	SourceChanges  = "changes"
)

// Extras is what the server provides beyond the stores. A nil Extras means this server has none, and a fused read that
// asks for them says "unavailable".
type Extras interface {
	// Topology is the estate as the server last worked it out.
	Topology(ctx context.Context) (*TopologyView, error)
	// Applications are the Ikhnos applications of the organisation (groups of services a person or discovery made) and the
	// services in each, as of the last saved workspace.
	Applications(ctx context.Context) ([]AppGroup, error)
	// Changes lists events recorded between since and until, newest last, at most limit of them. clusters narrows them to
	// those Ikhnos cluster ids, and targetIDs to the events about those ids (services, dependencies, applications, nodes,
	// clusters), when they are not empty.
	Changes(ctx context.Context, since, until time.Time, clusters, targetIDs []string, limit int) ([]ChangeEvent, error)
}

// TopologyView is the part of the estate the fused read joins: services and the traffic seen between them.
type TopologyView struct {
	At       time.Time
	Services []TopoService
	Nodes    []TopoNode
	Links    []TopoLink
	// Externals names the addresses outside the clusters that services were seen talking to, by id.
	Externals map[string]string
}

// TopoNode is one node of the topology.
type TopoNode struct{ ID, Name, Cluster string }

// TopoService is one service of the topology.
type TopoService struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Cluster   string            `json:"cluster,omitempty"`
	Kind      string            `json:"kind,omitempty"` // Deployment, StatefulSet ...
	Image     string            `json:"image,omitempty"`
	Status    string            `json:"status,omitempty"`
	Replicas  int               `json:"replicas"`
	Ready     int               `json:"readyReplicas"`
	Restarts  int               `json:"restarts,omitempty"`
	Labels    map[string]string `json:"-"`
	// Applications names the Ikhnos applications this service is in, AppIDs says which they are.
	Applications []string `json:"applications,omitempty"`
	AppIDs       []string `json:"-"`
}

// Key is the identity telemetry knows the service by.
func (s TopoService) Key() model.ServiceKey {
	return model.ServiceKey{Cluster: s.Cluster, Namespace: s.Namespace, Name: s.Name}
}

// AppGroup is an Ikhnos application: a named group of services. What FUSION calls an "application" elsewhere is a single
// service.name; this is the grouping a person made in Ikhnos, which telemetry itself does not carry.
type AppGroup struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Members     []AppMember `json:"services"`
	// Origin and Confidence say how the application came to be (a person, or discovery).
	Origin, Confidence string `json:"-"`
	// Unresolved counts the members Ikhnos has an id for but cannot tie to a service (a cluster that is not connected, a
	// service that has gone): they cannot be matched with telemetry, so they are not in Members.
	Unresolved int `json:"-"`
}

// AppMember is one service of an application, by the identity telemetry carries.
type AppMember struct {
	// ID is the service's id in Ikhnos (the topology's, or the record's of a service a person wrote in by hand).
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	Kind      string `json:"kind,omitempty"`
	// Aliases are other names the telemetry of this service may carry as its service.name (its app label), besides Name.
	Aliases []string `json:"aliases,omitempty"`
}

// Key is the identity telemetry knows the service by.
func (m AppMember) Key() model.ServiceKey {
	return model.ServiceKey{Cluster: m.Cluster, Namespace: m.Namespace, Name: m.Name}
}

// ServiceNames is every service.name telemetry of this application may carry: each member's name and aliases, sorted and without repeats.
func (g AppGroup) ServiceNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range g.Members {
		for _, n := range append([]string{m.Name}, m.Aliases...) {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Namespaces and Clusters are those the members run in, sorted and without repeats.
func (g AppGroup) Namespaces() []string {
	return g.distinct(func(m AppMember) string { return m.Namespace })
}
func (g AppGroup) Clusters() []string {
	return g.distinct(func(m AppMember) string { return m.Cluster })
}

func (g AppGroup) distinct(f func(AppMember) string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range g.Members {
		if v := f(m); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// TopoLink is traffic seen from one entity to another.
type TopoLink struct {
	ID               string // the dependency's id, which events about it carry
	From, To         string // topology ids
	FromKind, ToKind string // service | external
	Protocol         string
	Port             int
	Confidence       string
	Stale            bool
	Noise            string // dns | system: machinery rather than the applications' own
	CrossCluster     bool   // the two ends are in different clusters
	Traffic          LinkTraffic
}

// LinkTraffic is what was measured on a link, as of the topology it comes from. A zero is "not measured" as often as it is "none":
// only eBPF sees retransmits and round trips.
type LinkTraffic struct {
	Bytes             uint64  `json:"bytes"`
	Connections       uint64  `json:"connections"`
	BytesPerSec       float64 `json:"bytesPerSec"`
	ConnectionsPerMin float64 `json:"connectionsPerMin"`
	RttMs             float64 `json:"rttMs"`
	RetransmitsPerMin float64 `json:"retransmitsPerMin"`
}

// ChangeEvent is something that happened to part of the estate.
type ChangeEvent struct {
	Time        time.Time `json:"time"`
	Kind        string    `json:"kind"`       // service-scaled, node-status, cluster-added ...
	TargetKind  string    `json:"targetKind"` // cluster | node | service | dependency | agent
	TargetID    string    `json:"targetId,omitempty"`
	Name        string    `json:"name,omitempty"`
	Cluster     string    `json:"cluster,omitempty"`
	ClusterName string    `json:"clusterName,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Cause       string    `json:"cause,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	// Namespace is that of the service the event is about, when it is about one and the topology knows it.
	Namespace string `json:"namespace,omitempty"`
	// Resource is the key of the trace resource the event is about, when it is about one.
	Resource string `json:"resource,omitempty"`
}

// ResourceTopology is what Ikhnos knows about the service behind a resource.
type ResourceTopology struct {
	// Service is the service as Ikhnos sees it, matched by name, namespace and cluster.
	Service *TopoService `json:"service,omitempty"`
	// Calls are the services and external addresses it was seen calling; CalledBy those seen calling it.
	Calls    []Neighbour `json:"calls"`
	CalledBy []Neighbour `json:"calledBy"`
}

// Neighbour is one end of observed traffic.
type Neighbour struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"` // service | external
	Namespace  string `json:"namespace,omitempty"`
	Cluster    string `json:"cluster,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Port       int    `json:"port,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	Stale      bool   `json:"stale,omitempty"`
}

const (
	defaultChangesBefore = 30 * time.Minute
	maxChangesBefore     = 24 * time.Hour
	defaultMaxChanges    = 50
	hardMaxChanges       = 500
	maxNeighbours        = 50
)

// matchService finds the topology service behind a resource by its name (failing that, its app label) and the namespace
// and cluster it carries. One that names no namespace or cluster matches by the rest only when that leaves a single service;
// with several it matches none, and ambiguous says so, rather than the match being whichever service came first. A resource
// that does carry its whole key can only fit the services of that key.
func (v *TopologyView) matchService(r *Resource) (sv *TopoService, ambiguous bool) {
	if r.Service == "" {
		return nil, false
	}
	var byName, byLabel []*TopoService
	for i := range v.Services {
		c := &v.Services[i]
		if (r.Namespace != "" && c.Namespace != r.Namespace) || (r.Cluster != "" && c.Cluster != r.Cluster) {
			continue
		}
		if c.Name == r.Service {
			byName = append(byName, c)
		} else if c.Labels["app"] == r.Service || c.Labels["app.kubernetes.io/name"] == r.Service {
			byLabel = append(byLabel, c)
		}
	}
	for _, found := range [][]*TopoService{byName, byLabel} { // the name wins over the label
		switch len(found) {
		case 0:
		case 1:
			return found[0], false
		default:
			return nil, true
		}
	}
	return nil, false
}

func (v *TopologyView) byID() map[string]*TopoService {
	m := make(map[string]*TopoService, len(v.Services))
	for i := range v.Services {
		m[v.Services[i].ID] = &v.Services[i]
	}
	return m
}

// endVisible says whether the Scope may see one end of a link: a service in a namespace and cluster it may see, or an address
// outside the clusters, which has neither to check and so is for an unrestricted Scope only.
func endVisible(s Scope, byID map[string]*TopoService, id, kind string) bool {
	if kind == "external" {
		return s.Unrestricted()
	}
	o := byID[id]
	return o != nil && s.NamespaceVisible(o.Namespace) && s.ClusterVisible(o.Cluster)
}

// topologyFor builds a resource's neighbours, leaving out what the Scope may not see.
func (v *TopologyView) topologyFor(s Scope, r *Resource) *ResourceTopology {
	sv, _ := v.matchService(r)
	if sv == nil {
		return nil
	}
	byID := v.byID()
	neighbour := func(id, kind string, l TopoLink) (Neighbour, bool) {
		n := Neighbour{ID: id, Kind: kind, Protocol: l.Protocol, Port: l.Port, Confidence: l.Confidence, Stale: l.Stale}
		if kind == "external" {
			n.Name = v.Externals[id]
			if n.Name == "" {
				n.Name = id
			}
		} else if o := byID[id]; o != nil {
			n.Name, n.Namespace, n.Cluster = o.Name, o.Namespace, o.Cluster
		}
		return n, endVisible(s, byID, id, kind)
	}
	rt := &ResourceTopology{Service: sv, Calls: []Neighbour{}, CalledBy: []Neighbour{}}
	for _, l := range v.Links {
		if l.Noise != "" {
			continue
		}
		switch {
		case l.From == sv.ID && len(rt.Calls) < maxNeighbours:
			if n, ok := neighbour(l.To, l.ToKind, l); ok {
				rt.Calls = append(rt.Calls, n)
			}
		case l.To == sv.ID && l.FromKind != "external" && len(rt.CalledBy) < maxNeighbours:
			if n, ok := neighbour(l.From, l.FromKind, l); ok {
				rt.CalledBy = append(rt.CalledBy, n)
			}
		}
	}
	byName := func(l []Neighbour) {
		sort.Slice(l, func(i, j int) bool {
			if l[i].Name != l[j].Name {
				return l[i].Name < l[j].Name
			}
			return l[i].ID < l[j].ID
		})
	}
	byName(rt.Calls)
	byName(rt.CalledBy)
	return rt
}

// attachTopology puts each resource's neighbours on it. Returns how many resources the topology knew, and how many it could
// not tell from several services.
func attachTopology(v *TopologyView, s Scope, looked []*Resource) (matched, ambiguous int) {
	for _, r := range looked {
		if rt := v.topologyFor(s, r); rt != nil {
			r.Topology = rt
			matched++
		} else if _, amb := v.matchService(r); amb {
			ambiguous++
		}
	}
	return matched, ambiguous
}

// touched is what a trace is about in the topology, by the ids that events about it carry.
type touched struct {
	services map[string]*Resource // service id
	deps     map[string]*Resource // dependency id: a link of one of the services
	apps     map[string]*Resource // application id
	nodes    map[string]*Resource // node id, and node name for a node the topology does not know
	clusters map[string]bool
	ns       map[string]string // service id -> namespace
}

func touchedBy(v *TopologyView, tr *Trace) touched {
	t := touched{map[string]*Resource{}, map[string]*Resource{}, map[string]*Resource{}, map[string]*Resource{}, map[string]bool{}, map[string]string{}}
	for _, r := range tr.Resources {
		if v != nil {
			if sv, _ := v.matchService(r); sv != nil {
				t.services[sv.ID], t.ns[sv.ID] = r, sv.Namespace
				for _, a := range sv.AppIDs {
					t.apps[a] = r
				}
			}
			for _, n := range v.Nodes {
				if r.Node != "" && n.Name == r.Node && (r.Cluster == "" || n.Cluster == r.Cluster) {
					t.nodes[n.ID] = r
				}
			}
		}
		if r.Node != "" {
			t.nodes[r.Node] = r
		}
		if r.Cluster != "" {
			t.clusters[r.Cluster] = true
		}
	}
	if v != nil {
		for _, l := range v.Links {
			if l.Noise != "" || l.ID == "" {
				continue
			}
			if r := t.services[l.From]; r != nil {
				t.deps[l.ID] = r
			} else if r := t.services[l.To]; r != nil {
				t.deps[l.ID] = r
			}
		}
	}
	return t
}

// ids are the targets events about the trace are about: all of them but the nodes the topology does not know by id.
func (t touched) ids() []string {
	var out []string
	for _, m := range []map[string]*Resource{t.services, t.deps, t.apps, t.nodes} {
		for id := range m {
			out = append(out, id)
		}
	}
	for c := range t.clusters {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// relevantChanges keeps the events that are about the trace: those of its services (matched through the topology), of the
// traffic seen to and from them, of the applications they are in, of the nodes its pods ran on, and of its clusters. A Scope
// limited to namespaces sees only the events of services in them, and of links between what it may see.
func relevantChanges(evs []ChangeEvent, v *TopologyView, s Scope, tr *Trace) []ChangeEvent {
	t := touchedBy(v, tr)
	var byID map[string]*TopoService
	var link map[string]TopoLink
	if v != nil {
		byID, link = v.byID(), map[string]TopoLink{}
		for _, l := range v.Links {
			link[l.ID] = l
		}
	}
	limited := len(s.Namespaces) > 0
	var out []ChangeEvent
	for _, e := range evs {
		if !s.ClusterVisible(e.Cluster) {
			continue
		}
		switch e.TargetKind {
		case "service":
			r, ok := t.services[e.TargetID]
			if !ok {
				continue
			}
			e.Namespace, e.Resource = t.ns[e.TargetID], r.Key
			if !s.NamespaceVisible(e.Namespace) {
				continue
			}
		case "dependency":
			r, ok := t.deps[e.TargetID]
			if !ok || !endVisible(s, byID, link[e.TargetID].From, link[e.TargetID].FromKind) || !endVisible(s, byID, link[e.TargetID].To, link[e.TargetID].ToKind) {
				continue
			}
			e.Resource = r.Key
		case "application":
			r, ok := t.apps[e.TargetID]
			if !ok || limited { // it holds services the Scope may not see
				continue
			}
			e.Resource = r.Key
		case "node":
			if limited {
				continue
			}
			r := t.nodes[e.TargetID]
			if r == nil {
				r = t.nodes[e.Name]
			}
			if r == nil {
				continue
			}
			e.Resource = r.Key
		case "cluster":
			if limited || !t.clusters[e.TargetID] && !t.clusters[e.Cluster] {
				continue
			}
		default:
			continue
		}
		out = append(out, e)
	}
	return out
}

// fuseIkhnos reads the topology and the changes a fused read asked for and puts them on the fused object. It is a no-op
// for a read that asked for neither.
func (c *Client) fuseIkhnos(ctx context.Context, s Scope, f *Fused, looked []*Resource, opts FuseOptions, warn func(string, error), setSource func(string, string), note func(string, ...any)) {
	var view *TopologyView
	if opts.Topology || opts.Changes {
		if opts.Extras == nil {
			for _, k := range []string{SourceTopology, SourceChanges} {
				if (k == SourceTopology && opts.Topology) || (k == SourceChanges && opts.Changes) {
					setSource(k, SourceUnavailable)
					note("%s: this server has no topology to read", k)
				}
			}
			return
		}
		var err error
		if view, err = opts.Extras.Topology(ctx); err != nil {
			if opts.Topology {
				warn(SourceTopology, err)
			}
			if opts.Changes {
				warn(SourceChanges, err)
			}
			return
		}
	}
	if opts.Topology {
		setSource(SourceTopology, SourceOK)
		n, amb := attachTopology(view, s, looked)
		if amb > 0 {
			note("topology: %d resource(s) were left unmatched because several services of Ikhnos have their name and they carry no namespace or cluster to tell them apart", amb)
		}
		if n == 0 && amb == 0 && len(looked) > 0 {
			note("topology: none of the trace's services is known to Ikhnos by that name, namespace and cluster")
		}
	}
	if !opts.Changes {
		return
	}
	setSource(SourceChanges, SourceOK)
	since, until := f.Start.Add(-opts.ChangesBefore), f.End.Add(opts.Pad)
	var clusters []string
	seen := map[string]bool{}
	for _, r := range f.Resources {
		if r.Cluster != "" && !seen[r.Cluster] {
			seen[r.Cluster] = true
			clusters = append(clusters, r.Cluster)
		}
	}
	ids := touchedBy(view, f.Trace).ids()
	if len(ids) == 0 {
		return // nothing of the trace is known to have events
	}
	// Only the events about the trace's own targets are read; what is left to drop afterwards is what the Scope may not see.
	evs, err := opts.Extras.Changes(ctx, since, until, clusters, ids, 2000)
	if err != nil {
		warn(SourceChanges, err)
		return
	}
	kept := relevantChanges(evs, view, s, f.Trace)
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Time.Before(kept[j].Time) })
	if len(kept) > opts.MaxChanges {
		// keep the newest: what happened last is closest to the trace
		kept = kept[len(kept)-opts.MaxChanges:]
		note("changes: more than %d events; the newest are kept", opts.MaxChanges)
	}
	f.Changes = kept
}

func (o *FuseOptions) ikhnosJoins(f *Fused) {
	if o.Topology {
		f.Sources[SourceTopology] = SourceNotRequested
		f.Joins[SourceTopology] = "associated, not proven: the service Ikhnos knows by the same name, namespace and cluster, and the traffic it has seen to and from it as of now (the topology is not kept per trace). A resource that carries no namespace or cluster matches only when exactly one service has its name; when several do it has no match, and a warning says so"
	}
	if o.Changes {
		f.Sources[SourceChanges] = SourceNotRequested
		f.Joins[SourceChanges] = fmt.Sprintf("associated, not proven: events Ikhnos recorded about the trace's services, the traffic seen to and from them, the applications they are in, and its nodes and clusters, from %s before the trace to %s after it", o.ChangesBefore, o.Pad)
	}
}
