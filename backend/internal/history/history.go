// Package history turns the live topology into a recorded past: compact snapshots, the change
// events between them, and the retention rules that keep the record bounded.
package history

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"continuum/internal/model"
	"continuum/internal/store"
)

// Compact drops what a snapshot does not need (evidence strings, labels of nodes and services, open
// suggestions) so a record of a large estate stays small. Cluster labels are kept: they are where
// region and provider hints come from.
func Compact(t model.Topology) model.Topology {
	out := model.Topology{
		Clusters:          make([]model.Cluster, len(t.Clusters)),
		Nodes:             make([]model.Node, len(t.Nodes)),
		Namespaces:        make([]model.Namespace, len(t.Namespaces)),
		Services:          make([]model.Service, len(t.Services)),
		Suggestions:       []model.Suggestion{},
		Dependencies:      append([]model.Dependency{}, t.Dependencies...),
		ExternalEndpoints: make([]model.ExternalEndpoint, len(t.ExternalEndpoints)),
		Paths:             append([]model.Path{}, t.Paths...),
	}
	for i, c := range t.Clusters {
		c.Evidence = nil
		c.ClearObservation()
		out.Clusters[i] = c
	}
	for i, n := range t.Nodes {
		n.Evidence, n.Labels = nil, nil
		n.ClearObservation()
		out.Nodes[i] = n
	}
	for i, n := range t.Namespaces {
		n.Evidence, n.Labels = nil, nil
		n.ClearObservation()
		out.Namespaces[i] = n
	}
	for i, s := range t.Services {
		s.Evidence, s.Labels = nil, nil
		s.ClearObservation()
		out.Services[i] = s
	}
	for i, e := range t.ExternalEndpoints {
		e.Evidence = nil
		e.ClearObservation()
		out.ExternalEndpoints[i] = e
	}
	sort.Slice(out.Clusters, func(i, j int) bool { return out.Clusters[i].ID < out.Clusters[j].ID })
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	sort.Slice(out.Namespaces, func(i, j int) bool { return out.Namespaces[i].ID < out.Namespaces[j].ID })
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].ID < out.Services[j].ID })
	sort.Slice(out.Dependencies, func(i, j int) bool { return out.Dependencies[i].ID < out.Dependencies[j].ID })
	sort.Slice(out.ExternalEndpoints, func(i, j int) bool { return out.ExternalEndpoints[i].ID < out.ExternalEndpoints[j].ID })
	sort.Slice(out.Paths, func(i, j int) bool { return out.Paths[i].ID < out.Paths[j].ID })
	return out
}

// Encode compresses a snapshot for storage and returns a fingerprint of its content. The
// fingerprint ignores volatile fields (last-seen stamps and per-window rates) so that an unchanged
// estate is recognised as unchanged.
func Encode(t model.Topology) (data []byte, fingerprint string, err error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), Fingerprint(t), nil
}

// Fingerprint hashes what makes two snapshots "the same estate": records and their state, without
// timestamps, revisions or traffic rates.
func Fingerprint(t model.Topology) string {
	h := sha256.New()
	w := func(parts ...any) { fmt.Fprintln(h, parts...) }
	for _, c := range t.Clusters {
		w("c", c.ID, c.Name, c.Status, c.Version, c.Stale, c.Region, c.Tier)
	}
	for _, n := range t.Nodes {
		w("n", n.ID, n.ClusterID, n.Status, n.Kind, n.CPU, n.MemoryGb, n.Stale)
	}
	for _, s := range t.Services {
		w("s", s.ID, s.ClusterID, s.Status, s.Replicas, s.ReadyReplicas, s.Image, s.ImageDigest, strings.Join(s.NodeIDs, ","), s.Stale)
	}
	for _, d := range t.Dependencies {
		w("d", d.ID, d.Stale, d.Protocol, d.Port, d.Noise)
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:12])
}

// Decode reverses Encode.
func Decode(data []byte) (model.Topology, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return model.Topology{}, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, 512<<20))
	if err != nil {
		return model.Topology{}, err
	}
	var t model.Topology
	if err := json.Unmarshal(raw, &t); err != nil {
		return model.Topology{}, err
	}
	return t, nil
}

// Retention decides which snapshots to delete. Everything from the last 24 hours is kept; from
// there up to 7 days one per hour; beyond that one per day, up to retentionDays. If what remains
// is still over maxBytes the oldest go first. The newest snapshot is never deleted.
func Retention(points []store.HistoryPoint, now time.Time, retentionDays int, maxBytes int64) []time.Time {
	if len(points) == 0 {
		return nil
	}
	if retentionDays < 1 {
		retentionDays = 30
	}
	keep := make([]bool, len(points))
	lastInBucket := map[string]int{}
	for i, p := range points {
		age := now.Sub(p.At)
		switch {
		case age > time.Duration(retentionDays)*24*time.Hour:
			// gone
		case age <= 24*time.Hour:
			keep[i] = true
		case age <= 7*24*time.Hour:
			lastInBucket["h"+p.At.UTC().Format("2006-01-02T15")] = i
		default:
			lastInBucket["d"+p.At.UTC().Format("2006-01-02")] = i
		}
	}
	for _, i := range lastInBucket {
		keep[i] = true
	}
	keep[len(points)-1] = true
	if maxBytes > 0 {
		var total int64
		for i, p := range points {
			if keep[i] {
				total += int64(p.Bytes)
			}
		}
		for i := 0; i < len(points)-1 && total > maxBytes; i++ {
			if keep[i] {
				keep[i] = false
				total -= int64(points[i].Bytes)
			}
		}
	}
	var del []time.Time
	for i, p := range points {
		if !keep[i] {
			del = append(del, p.At)
		}
	}
	return del
}
