package fusionapi

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"continuum/internal/model"
)

// The topology of one Ikhnos application: its services, the traffic they have with each other and with everything else, and
// what changed around them. It is a set of pointers, not telemetry: each service says by which labels its metrics, logs and
// traces are found, and the caller reads them with `application=<id>` on any other route.

// Most of each part that one answer carries; the answer says when a part was cut (AppTruncated).
const (
	maxAppLinks      = 2000
	maxAppNeighbours = 200
	maxAppExternals  = 50
	maxAppChanges    = 100

	DefaultTopologyWindow = 24 * time.Hour
	MaxTopologyWindow     = 7 * 24 * time.Hour
)

// AppTopology is the answer.
type AppTopology struct {
	Application AppInfo        `json:"application"`
	AsOf        time.Time      `json:"asOf"`
	Source      string         `json:"source"` // live | history
	SnapshotAt  *time.Time     `json:"snapshotAt,omitempty"`
	Health      AppHealth      `json:"health"`
	Services    []AppTopoSvc   `json:"services"`
	Links       []AppTopoLink  `json:"links"`
	Neighbours  []AppTopoOther `json:"neighbours"`
	Externals   []AppTopoExt   `json:"externals"`
	Changes     []ChangeEvent  `json:"changes"`
	Unresolved  int            `json:"unresolvedMembers"`
	Truncated   AppTruncated   `json:"truncated"`

	ids []string // what events about the application are about, for Targets
}

// AppInfo is the application itself.
type AppInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Origin      string `json:"origin,omitempty"`
	Confidence  string `json:"confidence,omitempty"`
}

// AppHealth rolls the services up: healthy, degraded (some not ready), down (none ready), unknown (not in the topology).
type AppHealth struct {
	Status     string          `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Services   AppHealthCounts `json:"services"`
	Restarts   int             `json:"restarts"`
	StaleLinks int             `json:"staleLinks"`
}

// AppHealthCounts counts the services by state.
type AppHealthCounts struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Degraded int `json:"degraded"`
	Down     int `json:"down"`
	Unknown  int `json:"unknown"`
}

// AppTopoSvc is a service of the application.
type AppTopoSvc struct {
	Key       string       `json:"key"`
	ID        string       `json:"id,omitempty"`
	Name      string       `json:"name"`
	Namespace string       `json:"namespace,omitempty"`
	Cluster   string       `json:"cluster,omitempty"`
	Kind      string       `json:"kind,omitempty"`
	Image     string       `json:"image,omitempty"`
	Status    string       `json:"status,omitempty"`
	Replicas  int          `json:"replicas"`
	Ready     int          `json:"readyReplicas"`
	Restarts  int          `json:"restarts"`
	Telemetry AppTelemetry `json:"telemetry"`
}

// AppTelemetry says how to find a service's telemetry: Member is the `member` label of the application info series, Filter
// the labels the telemetry itself carries.
type AppTelemetry struct {
	Member string          `json:"member"`
	Filter AppTelemetryKey `json:"filter"`
}

// AppTelemetryKey is the label values that select a service's series: its service names (its name and aliases) within its
// namespace and cluster, and its workload for the metrics that name that instead.
type AppTelemetryKey struct {
	Cluster     string            `json:"continuum_cluster_id,omitempty"`
	Namespace   string            `json:"k8s_namespace_name,omitempty"`
	ServiceName []string          `json:"service_name"`
	Workload    map[string]string `json:"workload,omitempty"`
}

// AppTopoLink is traffic seen between two things, one at least a service of the application. A service is named by its key;
// an address outside the clusters by its id in `externals`.
type AppTopoLink struct {
	From         string      `json:"from"`
	FromKind     string      `json:"fromKind"` // service | external
	To           string      `json:"to"`
	ToKind       string      `json:"toKind"`
	Direction    string      `json:"direction"` // internal | outbound | inbound
	Port         int         `json:"port,omitempty"`
	Protocol     string      `json:"protocol,omitempty"`
	Confidence   string      `json:"confidence,omitempty"`
	Stale        bool        `json:"stale,omitempty"`
	CrossCluster bool        `json:"crossCluster,omitempty"`
	Traffic      LinkTraffic `json:"traffic"`
}

// AppTopoOther is a service outside the application that has traffic with it.
type AppTopoOther struct {
	Key          string   `json:"key"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Namespace    string   `json:"namespace,omitempty"`
	Cluster      string   `json:"cluster,omitempty"`
	Applications []string `json:"applications"`
}

// AppTopoExt is an address outside the clusters that services of the application were seen talking to.
type AppTopoExt struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Port     int      `json:"port,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	CalledBy []string `json:"calledBy"`
}

// AppTruncated says which parts were cut at their maximum.
type AppTruncated struct {
	Links      bool `json:"links"`
	Neighbours bool `json:"neighbours"`
	Externals  bool `json:"externals"`
	Changes    bool `json:"changes"`
}

// BuildAppTopology works out the answer from the application and the topology (as of now or as of a past moment), for a caller
// with this Scope: a neighbour the Scope may not see is left out, with the link to it, and so are all addresses outside the
// clusters unless the Scope is unrestricted. Machinery links (dns, system) are left out unless noise is set. It is one pass
// over the services and one over the links.
func BuildAppTopology(g *AppGroup, v *TopologyView, s Scope, noise bool) *AppTopology {
	t := &AppTopology{Application: AppInfo{ID: g.ID, Name: g.Name, Description: g.Description, Origin: g.Origin, Confidence: g.Confidence},
		AsOf: v.At, Source: "live", Services: []AppTopoSvc{}, Links: []AppTopoLink{}, Neighbours: []AppTopoOther{}, Externals: []AppTopoExt{},
		Changes: []ChangeEvent{}, Unresolved: g.Unresolved, ids: []string{g.ID}}
	byID := v.byID()
	byKey := map[model.ServiceKey]*TopoService{}
	for i := range v.Services {
		if k := v.Services[i].Key(); byKey[k] == nil {
			byKey[k] = &v.Services[i]
		}
	}

	member := map[string]string{} // topology service id -> key, for the services of the application
	var notReady []string
	for _, m := range g.Members {
		key := m.Key()
		svc := AppTopoSvc{Key: key.String(), ID: m.ID, Name: m.Name, Namespace: m.Namespace, Cluster: m.Cluster, Kind: m.Kind,
			Telemetry: AppTelemetry{Member: key.String(), Filter: AppTelemetryKey{Cluster: m.Cluster, Namespace: m.Namespace, ServiceName: append([]string{m.Name}, m.Aliases...)}}}
		if w := workloadLabel[m.Kind]; w != "" {
			svc.Telemetry.Filter.Workload = map[string]string{w: m.Name}
		}
		sv := byID[m.ID]
		if sv == nil {
			sv = byKey[key]
		}
		h := &t.Health.Services
		h.Total++
		switch {
		case sv == nil || sv.Replicas == 0:
			h.Unknown++
		case sv.Ready == 0:
			h.Down++
			notReady = append(notReady, m.Name)
		case sv.Ready < sv.Replicas:
			h.Degraded++
			notReady = append(notReady, m.Name)
		default:
			h.Healthy++
		}
		if sv != nil {
			svc.ID, svc.Kind, svc.Image, svc.Status = sv.ID, sv.Kind, sv.Image, sv.Status
			svc.Replicas, svc.Ready, svc.Restarts = sv.Replicas, sv.Ready, sv.Restarts
			member[sv.ID] = svc.Key
			t.Health.Restarts += sv.Restarts
			t.ids = append(t.ids, sv.ID)
		}
		t.Services = append(t.Services, svc)
	}
	sort.Slice(t.Services, func(i, j int) bool { return t.Services[i].Key < t.Services[j].Key })
	t.Health.Status, t.Health.Reason = healthOf(t.Health.Services, notReady)

	// the links with a service of the application at one end or both, whose other end the Scope may see
	type cand struct {
		AppTopoLink
		own, other, otherKind string // the application's service (its key) and what is on the far side (its topology id), when the link leaves or enters it
	}
	keyOf := func(id string) string {
		if k, ok := member[id]; ok {
			return k
		}
		if o := byID[id]; o != nil {
			return o.Key().String()
		}
		return id // an address outside the clusters is named by its id
	}
	var cands []cand
	for _, l := range v.Links {
		if l.Noise != "" && !noise {
			continue
		}
		_, fromIn := member[l.From]
		_, toIn := member[l.To]
		fromIn, toIn = fromIn && l.FromKind != "external", toIn && l.ToKind != "external"
		c := cand{AppTopoLink: AppTopoLink{From: keyOf(l.From), FromKind: l.FromKind, To: keyOf(l.To), ToKind: l.ToKind, Direction: "internal", Port: l.Port, Protocol: l.Protocol,
			Confidence: l.Confidence, Stale: l.Stale, CrossCluster: l.CrossCluster, Traffic: l.Traffic}}
		switch {
		case fromIn && toIn:
		case fromIn:
			c.Direction, c.own, c.other, c.otherKind = "outbound", c.From, l.To, l.ToKind
		case toIn:
			c.Direction, c.own, c.other, c.otherKind = "inbound", c.To, l.From, l.FromKind
		default:
			continue
		}
		if c.other != "" && !endVisible(s, byID, c.other, c.otherKind) {
			continue
		}
		cands = append(cands, c)
		t.ids = append(t.ids, l.ID)
	}
	// live links first, the busiest first
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Stale != cands[j].Stale {
			return !cands[i].Stale
		}
		return cands[i].Traffic.Bytes > cands[j].Traffic.Bytes
	})
	if t.Truncated.Links = len(cands) > maxAppLinks; t.Truncated.Links {
		cands = cands[:maxAppLinks]
	}

	// who is on the other side of those links
	near, far := map[string]bool{}, map[string]*AppTopoExt{}
	for _, c := range cands {
		t.Links = append(t.Links, c.AppTopoLink)
		if c.Stale {
			t.Health.StaleLinks++
		}
		if c.other == "" {
			continue
		}
		if o := byID[c.other]; o != nil {
			if !near[o.ID] {
				near[o.ID] = true
				t.Neighbours = append(t.Neighbours, AppTopoOther{Key: o.Key().String(), ID: o.ID, Name: o.Name, Namespace: o.Namespace, Cluster: o.Cluster,
					Applications: append([]string{}, o.Applications...)})
			}
			continue
		}
		x := far[c.other]
		if x == nil {
			x = &AppTopoExt{ID: c.other, Name: v.Externals[c.other], Port: c.Port, Protocol: c.Protocol, CalledBy: []string{}}
			if x.Name == "" {
				x.Name = c.other
			}
			far[c.other] = x
		}
		if !slices.Contains(x.CalledBy, c.own) {
			x.CalledBy = append(x.CalledBy, c.own)
		}
	}
	for _, x := range far {
		t.Externals = append(t.Externals, *x)
	}
	sort.Slice(t.Neighbours, func(i, j int) bool { return t.Neighbours[i].Key < t.Neighbours[j].Key })
	sort.Slice(t.Externals, func(i, j int) bool {
		if t.Externals[i].Name != t.Externals[j].Name {
			return t.Externals[i].Name < t.Externals[j].Name
		}
		return t.Externals[i].ID < t.Externals[j].ID
	})
	if t.Truncated.Neighbours = len(t.Neighbours) > maxAppNeighbours; t.Truncated.Neighbours {
		t.Neighbours = t.Neighbours[:maxAppNeighbours]
	}
	if t.Truncated.Externals = len(t.Externals) > maxAppExternals; t.Truncated.Externals {
		t.Externals = t.Externals[:maxAppExternals]
	}
	return t
}

func healthOf(c AppHealthCounts, notReady []string) (status, reason string) {
	known := c.Total - c.Unknown
	switch {
	case c.Total == 0:
		return "unknown", "the application has no services"
	case known == 0:
		return "unknown", "none of its services is in the topology"
	case c.Down == known:
		return "down", "no service has a replica ready: " + names(notReady)
	case c.Down+c.Degraded > 0:
		return "degraded", fmt.Sprintf("%d of %d services are not fully ready: %s", c.Down+c.Degraded, known, names(notReady))
	case c.Unknown > 0:
		return "healthy", fmt.Sprintf("%d service(s) are not in the topology", c.Unknown)
	}
	return "healthy", ""
}

func names(l []string) string {
	if sort.Strings(l); len(l) > 3 {
		l = append(l[:3:3], "...")
	}
	return strings.Join(l, ", ")
}

// Targets are the ids that events about the application are about: itself, its services and the links it shows.
func (t *AppTopology) Targets() []string { return t.ids }

// MaxAppChanges is the most events one answer carries.
const MaxAppChanges = maxAppChanges

// AddChanges sets the events, newest first, and says when there were more than an answer carries.
func (t *AppTopology) AddChanges(evs []ChangeEvent) {
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Time.After(evs[j].Time) })
	if t.Truncated.Changes = len(evs) > maxAppChanges; t.Truncated.Changes {
		evs = evs[:maxAppChanges]
	}
	t.Changes = evs
}
