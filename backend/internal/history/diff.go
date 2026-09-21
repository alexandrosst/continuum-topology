package history

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"continuum/internal/model"
	"continuum/internal/store"
)

const (
	maxEventsPerDiff = 200
	// A service that disappears from one cluster and appears in another within this time is a move.
	migrationWindow = time.Hour
	restartBurst    = 3
)

type removed struct {
	cluster string
	name    string
	at      time.Time
}

// Differ compares consecutive topologies and describes what changed. It remembers what recently
// disappeared so that a move between clusters that spans two comparisons is still recognised.
type Differ struct {
	recent map[string]removed // namespace/name/kind -> where it was
}

func NewDiffer() *Differ { return &Differ{recent: map[string]removed{}} }

func svcKey(s model.Service) string { return s.Namespace + "/" + s.Name + "/" + s.Kind }

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Diff returns the events that explain how cur differs from prev. A nil prev (the first snapshot
// ever) yields none: there is no "before" to compare with.
func (d *Differ) Diff(prev *model.Topology, cur model.Topology, now time.Time) []store.Event {
	if prev == nil {
		return nil
	}
	var evs []store.Event
	add := func(e store.Event) {
		e.At = now
		if e.Severity == "" {
			e.Severity = "info"
		}
		evs = append(evs, e)
	}

	pc, cc := map[string]model.Cluster{}, map[string]model.Cluster{}
	for _, c := range prev.Clusters {
		pc[c.ID] = c
	}
	for _, c := range cur.Clusters {
		cc[c.ID] = c
	}
	clusterName := func(id string) string {
		if c, ok := cc[id]; ok {
			return c.Name
		}
		return pc[id].Name
	}

	// ---- clusters ----
	newCluster, goneCluster := map[string]bool{}, map[string]bool{}
	for _, c := range cur.Clusters {
		p, had := pc[c.ID]
		if !had {
			newCluster[c.ID] = true
			nn, ns := 0, 0
			for _, n := range cur.Nodes {
				if n.ClusterID == c.ID {
					nn++
				}
			}
			for _, s := range cur.Services {
				if s.ClusterID == c.ID {
					ns++
				}
			}
			add(store.Event{Kind: "cluster-added", TargetKind: "cluster", TargetID: c.ID, Name: c.Name, ClusterID: c.ID, ClusterName: c.Name,
				Detail: fmt.Sprintf("%s joined with %s and %s", c.Name, plural(nn, "node", "nodes"), plural(ns, "service", "services")), Severity: "notice"})
			continue
		}
		if !p.Stale && c.Stale {
			add(store.Event{Kind: "cluster-unreachable", TargetKind: "cluster", TargetID: c.ID, Name: c.Name, ClusterID: c.ID, ClusterName: c.Name,
				Detail: "its agent stopped reporting; the last known state is kept and marked stale", Severity: "warning"})
		}
		if p.Stale && !c.Stale {
			add(store.Event{Kind: "cluster-reachable", TargetKind: "cluster", TargetID: c.ID, Name: c.Name, ClusterID: c.ID, ClusterName: c.Name, Detail: "its agent is reporting again"})
		}
		if p.Status != c.Status {
			sev := "info"
			if c.Status != "healthy" {
				sev = "warning"
			}
			add(store.Event{Kind: "cluster-status", TargetKind: "cluster", TargetID: c.ID, Name: c.Name, ClusterID: c.ID, ClusterName: c.Name,
				Detail: fmt.Sprintf("%s → %s", p.Status, c.Status), Severity: sev})
		}
		if p.Version != c.Version && p.Version != "" && c.Version != "" {
			add(store.Event{Kind: "cluster-version", TargetKind: "cluster", TargetID: c.ID, Name: c.Name, ClusterID: c.ID, ClusterName: c.Name,
				Detail: fmt.Sprintf("Kubernetes %s → %s", p.Version, c.Version), Severity: "notice"})
		}
	}
	for _, c := range prev.Clusters {
		if _, ok := cc[c.ID]; !ok {
			goneCluster[c.ID] = true
			add(store.Event{Kind: "cluster-removed", TargetKind: "cluster", TargetID: c.ID, Name: c.Name, ClusterID: c.ID, ClusterName: c.Name,
				Detail: c.Name + " is no longer part of the topology (its agent was revoked or removed)", Severity: "warning"})
		}
	}
	skipCluster := func(id string) bool { return newCluster[id] || goneCluster[id] }

	// ---- nodes ----
	pn, cn := map[string]model.Node{}, map[string]model.Node{}
	for _, n := range prev.Nodes {
		pn[n.ID] = n
	}
	for _, n := range cur.Nodes {
		cn[n.ID] = n
	}
	leftNodes := map[string]bool{} // nodes that went away or became unhealthy in this comparison
	for _, n := range cur.Nodes {
		if skipCluster(n.ClusterID) {
			continue
		}
		p, had := pn[n.ID]
		if !had {
			add(store.Event{Kind: "node-added", TargetKind: "node", TargetID: n.ID, Name: n.Name, ClusterID: n.ClusterID, ClusterName: clusterName(n.ClusterID),
				Detail: fmt.Sprintf("%s joined %s (%s, %s)", n.Name, clusterName(n.ClusterID), n.Role, n.Kind), Severity: "notice"})
			continue
		}
		if p.Status != n.Status {
			sev := "info"
			if n.Status != "healthy" {
				sev = "warning"
				leftNodes[n.ID] = true
			}
			add(store.Event{Kind: "node-status", TargetKind: "node", TargetID: n.ID, Name: n.Name, ClusterID: n.ClusterID, ClusterName: clusterName(n.ClusterID),
				Detail: fmt.Sprintf("%s: %s → %s", n.Name, p.Status, n.Status), Severity: sev})
		}
	}
	for _, n := range prev.Nodes {
		if _, ok := cn[n.ID]; !ok && !skipCluster(n.ClusterID) {
			leftNodes[n.ID] = true
			add(store.Event{Kind: "node-removed", TargetKind: "node", TargetID: n.ID, Name: n.Name, ClusterID: n.ClusterID, ClusterName: clusterName(n.ClusterID),
				Detail: fmt.Sprintf("%s left %s", n.Name, clusterName(n.ClusterID)), Severity: "warning"})
		}
	}
	nodeName := func(id string) string {
		if n, ok := cn[id]; ok {
			return n.Name
		}
		if n, ok := pn[id]; ok {
			return n.Name
		}
		return id
	}

	// ---- services ----
	ps, cs := map[string]model.Service{}, map[string]model.Service{}
	for _, s := range prev.Services {
		ps[s.ID] = s
	}
	for _, s := range cur.Services {
		cs[s.ID] = s
	}
	gone := map[string]model.Service{} // by namespace/name/kind, removed in this comparison
	for _, s := range prev.Services {
		if _, ok := cs[s.ID]; !ok && !skipCluster(s.ClusterID) {
			gone[svcKey(s)] = s
		}
	}
	for _, s := range cur.Services {
		if skipCluster(s.ClusterID) {
			continue
		}
		p, had := ps[s.ID]
		cname := clusterName(s.ClusterID)
		base := store.Event{TargetKind: "service", TargetID: s.ID, Name: s.Name, ClusterID: s.ClusterID, ClusterName: cname}
		if !had {
			k := svcKey(s)
			if g, ok := gone[k]; ok && g.ClusterID != s.ClusterID {
				e := base
				e.Kind, e.Severity = "service-migrated", "notice"
				e.Detail = fmt.Sprintf("%s moved from %s to %s", s.Name, clusterName(g.ClusterID), cname)
				add(e)
				delete(gone, k)
				continue
			}
			if r, ok := d.recent[k]; ok && r.cluster != s.ClusterID && now.Sub(r.at) <= migrationWindow {
				e := base
				e.Kind, e.Severity = "service-migrated", "notice"
				e.Detail = fmt.Sprintf("%s moved from %s to %s", s.Name, r.name, cname)
				add(e)
				delete(d.recent, k)
				continue
			}
			e := base
			e.Kind, e.Severity = "service-added", "notice"
			e.Detail = fmt.Sprintf("%s (%s) started in %s", s.Name, s.Kind, cname)
			add(e)
			continue
		}
		if p.Replicas != s.Replicas {
			e := base
			e.Kind = "service-scaled"
			e.Detail = fmt.Sprintf("%s: %d → %d replicas", s.Name, p.Replicas, s.Replicas)
			if s.Autoscaler != nil {
				e.Cause = fmt.Sprintf("autoscaler (%d–%d)", s.Autoscaler.Min, s.Autoscaler.Max)
			} else {
				e.Cause = "changed by a person or a pipeline (no autoscaler is attached)"
			}
			add(e)
		}
		if p.Image != s.Image || (p.ImageDigest != "" && s.ImageDigest != "" && p.ImageDigest != s.ImageDigest) {
			e := base
			e.Kind, e.Severity, e.Cause = "service-image", "notice", "rollout"
			if p.Image != s.Image {
				e.Detail = fmt.Sprintf("%s: %s → %s", s.Name, p.Image, s.Image)
			} else {
				e.Detail = fmt.Sprintf("%s: same image tag, new digest", s.Name)
			}
			add(e)
		}
		if p.Status != s.Status {
			sev := "info"
			if s.Status != "healthy" {
				sev = "warning"
			}
			e := base
			e.Kind, e.Severity = "service-status", sev
			e.Detail = fmt.Sprintf("%s: %s → %s (%d/%d ready)", s.Name, p.Status, s.Status, s.ReadyReplicas, s.Replicas)
			add(e)
		}
		if s.Restarts-p.Restarts >= restartBurst {
			e := base
			e.Kind, e.Severity = "service-restarts", "warning"
			e.Detail = fmt.Sprintf("%s restarted %d times", s.Name, s.Restarts-p.Restarts)
			add(e)
		}
		// Placement: the same service now runs on other nodes of the same cluster.
		if p.Replicas == s.Replicas && p.ClusterID == s.ClusterID {
			from, to := setDiff(p.NodeIDs, s.NodeIDs), setDiff(s.NodeIDs, p.NodeIDs)
			if len(from) > 0 && len(to) > 0 {
				e := base
				e.Kind = "service-rescheduled"
				e.Detail = fmt.Sprintf("%s moved from %s to %s", s.Name, names(from, nodeName), names(to, nodeName))
				for _, f := range from {
					if leftNodes[f] {
						e.Cause = fmt.Sprintf("node %s went away or became unhealthy", nodeName(f))
						e.Severity = "notice"
						break
					}
				}
				if e.Cause == "" {
					e.Cause = "rescheduled by Kubernetes (a drain, an eviction or a manual move)"
				}
				add(e)
			}
		}
	}
	keys := make([]string, 0, len(gone))
	for k := range gone {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := gone[k]
		d.recent[k] = removed{cluster: s.ClusterID, name: clusterName(s.ClusterID), at: now}
		add(store.Event{Kind: "service-removed", TargetKind: "service", TargetID: s.ID, Name: s.Name, ClusterID: s.ClusterID, ClusterName: clusterName(s.ClusterID),
			Detail: fmt.Sprintf("%s stopped running in %s", s.Name, clusterName(s.ClusterID)), Severity: "notice"})
	}
	for k, r := range d.recent { // forget old removals
		if now.Sub(r.at) > migrationWindow {
			delete(d.recent, k)
		}
	}

	// ---- observed dependencies ----
	pd := map[string]model.Dependency{}
	for _, x := range prev.Dependencies {
		pd[x.ID] = x
	}
	endName := func(id, kind string) string {
		if kind == "service" {
			if s, ok := cs[id]; ok {
				return s.Name
			}
			if s, ok := ps[id]; ok {
				return s.Name
			}
		}
		for _, e := range cur.ExternalEndpoints {
			if e.ID == id {
				return e.Host
			}
		}
		return id
	}
	for _, x := range cur.Dependencies {
		if x.Noise != "" || !contains(x.Sources, "observed") {
			continue
		}
		p, had := pd[x.ID]
		label := fmt.Sprintf("%s → %s (%s %d)", endName(x.From, x.FromKind), endName(x.To, x.ToKind), x.Protocol, x.Port)
		clusterID := ""
		if s, ok := cs[x.From]; ok {
			clusterID = s.ClusterID
		}
		if !had {
			sev := "info"
			if x.CrossCluster {
				sev = "notice"
			}
			add(store.Event{Kind: "dependency-seen", TargetKind: "dependency", TargetID: x.ID, Name: label, ClusterID: clusterID, ClusterName: clusterName(clusterID),
				Detail: label + " seen in traffic for the first time", Severity: sev})
		} else if !p.Stale && x.Stale {
			add(store.Event{Kind: "dependency-quiet", TargetKind: "dependency", TargetID: x.ID, Name: label, ClusterID: clusterID, ClusterName: clusterName(clusterID),
				Detail: label + " has gone quiet"})
		} else if p.Stale && !x.Stale {
			add(store.Event{Kind: "dependency-seen", TargetKind: "dependency", TargetID: x.ID, Name: label, ClusterID: clusterID, ClusterName: clusterName(clusterID),
				Detail: label + " is back in traffic"})
		}
	}

	if len(evs) > maxEventsPerDiff {
		n := len(evs) - maxEventsPerDiff
		evs = evs[:maxEventsPerDiff]
		evs = append(evs, store.Event{At: now, Kind: "many-changes", TargetKind: "cluster", Detail: fmt.Sprintf("%d more changes in the same moment were not listed", n), Severity: "notice"})
	}
	return evs
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func setDiff(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func names(ids []string, name func(string) string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = name(id)
	}
	if len(out) > 3 {
		return strings.Join(out[:3], ", ") + fmt.Sprintf(" and %d more", len(out)-3)
	}
	return strings.Join(out, ", ")
}
