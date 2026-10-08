package fusionapi

import (
	"context"
	"fmt"
	"sort"
	"time"
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
	// Changes lists events recorded between since and until, newest last, at most limit of them. clusters narrows them to
	// those Ikhnos cluster ids when it is not empty.
	Changes(ctx context.Context, since, until time.Time, clusters []string, limit int) ([]ChangeEvent, error)
}

// TopologyView is the part of the estate the fused read joins: services and the traffic seen between them.
type TopologyView struct {
	At       time.Time
	Services []TopoService
	Links    []TopoLink
	// Externals names the addresses outside the clusters that services were seen talking to, by id.
	Externals map[string]string
}

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
}

// TopoLink is traffic seen from one entity to another.
type TopoLink struct {
	From, To         string // topology ids
	FromKind, ToKind string // service | external
	Protocol         string
	Port             int
	Confidence       string
	Stale            bool
	Noise            string // dns | system: machinery rather than the applications' own
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

// matchService finds the topology service behind a resource: the name must match, and so must the namespace and cluster
// when the resource has them. Telemetry names a service by its service.name, which for a deployed workload is usually its
// name or its app label; the name wins over the label.
func (v *TopologyView) matchService(r *Resource) *TopoService {
	if r.Service == "" {
		return nil
	}
	fits := func(sv *TopoService) bool {
		return (r.Namespace == "" || sv.Namespace == r.Namespace) && (r.Cluster == "" || sv.Cluster == r.Cluster)
	}
	var byLabel *TopoService
	for i := range v.Services {
		sv := &v.Services[i]
		if !fits(sv) {
			continue
		}
		if sv.Name == r.Service {
			return sv
		}
		if byLabel == nil && (sv.Labels["app"] == r.Service || sv.Labels["app.kubernetes.io/name"] == r.Service) {
			byLabel = sv
		}
	}
	return byLabel
}

// topologyFor builds a resource's neighbours, leaving out what the Scope may not see.
func (v *TopologyView) topologyFor(s Scope, r *Resource) *ResourceTopology {
	sv := v.matchService(r)
	if sv == nil {
		return nil
	}
	byID := make(map[string]*TopoService, len(v.Services))
	for i := range v.Services {
		byID[v.Services[i].ID] = &v.Services[i]
	}
	neighbour := func(id, kind string, l TopoLink) (Neighbour, bool) {
		n := Neighbour{ID: id, Kind: kind, Protocol: l.Protocol, Port: l.Port, Confidence: l.Confidence, Stale: l.Stale}
		if kind == "external" {
			n.Name = v.Externals[id]
			if n.Name == "" {
				n.Name = id
			}
			return n, s.Unrestricted() // an address outside the clusters has no namespace or cluster to check against
		}
		o := byID[id]
		if o == nil {
			return n, false
		}
		n.Name, n.Namespace, n.Cluster = o.Name, o.Namespace, o.Cluster
		return n, s.NamespaceVisible(o.Namespace) && s.ClusterVisible(o.Cluster)
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

// attachTopology puts each resource's neighbours on it. Returns how many resources the topology knew.
func attachTopology(v *TopologyView, s Scope, looked []*Resource) (matched int) {
	for _, r := range looked {
		if rt := v.topologyFor(s, r); rt != nil {
			r.Topology = rt
			matched++
		}
	}
	return matched
}

// relevantChanges keeps the events that are about the trace: those of its services (matched through the topology), of the
// nodes its pods ran on, and of its clusters. A Scope limited to namespaces sees only the events of services in them.
func relevantChanges(evs []ChangeEvent, v *TopologyView, s Scope, tr *Trace) []ChangeEvent {
	svcByID := map[string]*Resource{}
	svcNS := map[string]string{}
	if v != nil {
		for _, r := range tr.Resources {
			if sv := v.matchService(r); sv != nil {
				svcByID[sv.ID] = r
				svcNS[sv.ID] = sv.Namespace
			}
		}
	}
	nodes := map[string]*Resource{}
	clusters := map[string]bool{}
	for _, r := range tr.Resources {
		if r.Node != "" {
			nodes[r.Node] = r
		}
		if r.Cluster != "" {
			clusters[r.Cluster] = true
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
			r, ok := svcByID[e.TargetID]
			if !ok {
				continue
			}
			e.Namespace, e.Resource = svcNS[e.TargetID], r.Key
			if !s.NamespaceVisible(e.Namespace) {
				continue
			}
		case "node":
			if limited {
				continue
			}
			r := nodes[e.Name]
			if r == nil {
				r = nodes[e.TargetID]
			}
			if r == nil {
				continue
			}
			e.Resource = r.Key
		case "cluster":
			if limited || !clusters[e.TargetID] && !clusters[e.Cluster] {
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
		if n := attachTopology(view, s, looked); n == 0 && len(looked) > 0 {
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
	// A few more than asked for are read: the events of other services of the cluster are dropped afterwards.
	evs, err := opts.Extras.Changes(ctx, since, until, clusters, 2000)
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
		f.Joins[SourceTopology] = "associated, not proven: the service Ikhnos knows by the same name, namespace and cluster, and the traffic it has seen to and from it as of now (the topology is not kept per trace)"
	}
	if o.Changes {
		f.Sources[SourceChanges] = SourceNotRequested
		f.Joins[SourceChanges] = fmt.Sprintf("associated, not proven: events Ikhnos recorded about the trace's services, nodes and clusters from %s before the trace to %s after it", o.ChangesBefore, o.Pad)
	}
}
