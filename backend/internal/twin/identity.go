package twin

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/interpret"
	"continuum/internal/store"
)

// Stable identity, and what it is made of.
//
//	organisation  the id of the tenant; every id below is derived from it, so two organisations can never share one.
//	cluster       the UID of its kube-system namespace (agents pin it; an approved agent for a UID is unique per
//	              organisation). A cluster re-created under the same name has a new UID and is a DIFFERENT cluster:
//	              it gets its own id, and the model says which other clusters carry the same name.
//	node          the most stable identifier the node offers, in this order (Basis):
//	                1. provider-id   the cloud's instance id (aws:///…/i-…), unless it is only the node's name in
//	                                 disguise (k3s://name, kind://…/name), which is no more stable than the name
//	                2. system-uuid   SMBIOS product UUID: tied to the machine, differs between cloned VMs
//	                3. machine-id    /etc/machine-id: stable per OS install, but cloned images share it
//	                4. name          the node's name, when nothing better is reported
//	              If two nodes of one cluster report the same identifier (cloned machine-ids), neither is trusted and
//	              both fall back to their names. The identifier itself is never shown or stored, only a digest.
//	workload      cluster + namespace + kind + name. The wire carries no workload UID; a workload deleted and created
//	              again under the same name is the same service, which is what people who wrote a manifest mean.
//
// Record ids for nodes stay what they always were (a hash of cluster and name) the first time an identity is seen,
// so existing workspaces keep working, and are then remembered per identity:
//
//   - the same machine under a new name keeps its id and gains an alias (a rename is not a new node),
//   - a different machine that takes a name another machine used gets a NEW id (a replaced node is not the old one).

// NodeBasis names what a node's identity is made of.
type NodeBasis string

const (
	BasisProviderID NodeBasis = "provider-id"
	BasisSystemUUID NodeBasis = "system-uuid"
	BasisMachineID  NodeBasis = "machine-id"
	BasisName       NodeBasis = "name"
)

// NodeIdentity is the stable identity of a node: a basis and a digest of the identifier.
type NodeIdentity struct {
	Basis  NodeBasis
	Digest string
}

// String is the form stored: "<basis>:<digest>".
func (i NodeIdentity) String() string { return string(i.Basis) + ":" + i.Digest }

// nameDerived are providerID schemes that only repeat the node's name.
var nameDerived = []string{"k3s://", "rke2://", "k0s://", "kind://", "minikube://", "microk8s://", "talos://", "docker://", "kubeadm://"}

// junkIDs are identifiers firmware and images report for many machines at once.
var junkIDs = map[string]bool{
	"":                                     true,
	"00000000-0000-0000-0000-000000000000": true,
	"ffffffff-ffff-ffff-ffff-ffffffffffff": true,
	"03000200-0400-0500-0006-000700080009": true, // a well-known default in some firmware
	"00000000000000000000000000000000":     true,
	"unknown":                              true,
	"none":                                 true,
	"not settable":                         true,
}

func usable(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return !junkIDs[id] && len(id) >= 8
}

func digest(v string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(v))))
	return hex.EncodeToString(h[:8])
}

// IdentityOf picks the most stable identity a node reports.
func IdentityOf(n *continuumv1.NodeFacts) NodeIdentity {
	if p := strings.TrimSpace(n.ProviderId); p != "" && usable(p) {
		derived := false
		for _, s := range nameDerived {
			if strings.HasPrefix(strings.ToLower(p), s) {
				derived = true
			}
		}
		if !derived && !strings.HasSuffix(p, "/"+n.Name) {
			return NodeIdentity{BasisProviderID, digest(p)}
		}
	}
	if usable(n.SystemUuid) {
		return NodeIdentity{BasisSystemUUID, digest(n.SystemUuid)}
	}
	if usable(n.MachineId) {
		return NodeIdentity{BasisMachineID, digest(n.MachineId)}
	}
	return NodeIdentity{BasisName, n.Name}
}

// LegacyNodeID is the id nodes have always had: a hash of the cluster and the node's name.
func LegacyNodeID(cluster, name string) string { return interpret.NodeID(cluster, name) }

func extendedNodeID(cluster, name string, ident NodeIdentity) string {
	return "nd-" + interpret.Hash(cluster, name, ident.String())
}

// NodeRecord is what the registry decided for one node.
type NodeRecord struct {
	ID       string
	Identity NodeIdentity
	// Aliases are the names this machine had before, most recent first.
	Aliases []string
	// Renamed is set when this snapshot is the first to show the machine under a new name.
	Renamed bool
	// Replaced is set when a different machine now carries a name another one had, and so was given a new id.
	Replaced bool
}

// Registry remembers, per cluster, which record id each node identity was given. It is safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	entries map[string]map[string]*store.Identity // cluster -> ident -> entry
	owner   map[string]map[string]string          // cluster -> record id -> ident
	dirty   map[string]map[string]bool            // cluster -> ident
}

func NewRegistry() *Registry {
	return &Registry{entries: map[string]map[string]*store.Identity{}, owner: map[string]map[string]string{}, dirty: map[string]map[string]bool{}}
}

// Load fills the registry from what the store kept.
func (r *Registry) Load(ids []store.Identity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range ids {
		e := ids[i]
		r.put(&e)
	}
}

func (r *Registry) put(e *store.Identity) {
	if r.entries[e.ClusterID] == nil {
		r.entries[e.ClusterID] = map[string]*store.Identity{}
		r.owner[e.ClusterID] = map[string]string{}
	}
	r.entries[e.ClusterID][e.Ident] = e
	r.owner[e.ClusterID][e.RecordID] = e.Ident
}

// Resolve assigns a record id to every node of one cluster's snapshot. It is deterministic for a given registry
// state and snapshot (nodes are visited in name order).
func (r *Registry) Resolve(cluster string, nodes []*continuumv1.NodeFacts, now time.Time) map[string]NodeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	sorted := append([]*continuumv1.NodeFacts(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	idents := make(map[string]NodeIdentity, len(sorted))
	count := map[string]int{}
	for _, n := range sorted {
		id := IdentityOf(n)
		idents[n.Key] = id
		if id.Basis != BasisName {
			count[id.String()]++
		}
	}
	// An identifier shared by two nodes of one cluster identifies neither.
	for _, n := range sorted {
		if id := idents[n.Key]; id.Basis != BasisName && count[id.String()] > 1 {
			idents[n.Key] = NodeIdentity{BasisName, n.Name}
		}
	}

	out := make(map[string]NodeRecord, len(sorted))
	taken := map[string]bool{} // ids handed out in this pass
	for _, n := range sorted {
		id := idents[n.Key]
		if id.Basis == BasisName {
			out[n.Key] = NodeRecord{ID: LegacyNodeID(cluster, n.Name), Identity: id}
			taken[out[n.Key].ID] = true
			continue
		}
		key := id.String()
		if e := r.entries[cluster][key]; e != nil {
			rec := NodeRecord{ID: e.RecordID, Identity: id, Aliases: append([]string(nil), e.Aliases...)}
			if e.Name != n.Name {
				e.Aliases = prepend(e.Aliases, e.Name)
				e.Name = n.Name
				rec.Aliases, rec.Renamed = append([]string(nil), e.Aliases...), true
				r.mark(cluster, key)
			}
			e.LastSeen = now
			out[n.Key] = rec
			taken[rec.ID] = true
			continue
		}
		// A machine seen for the first time. It takes the id its name has always given, unless another machine
		// already holds that id.
		cand := LegacyNodeID(cluster, n.Name)
		rec := NodeRecord{Identity: id}
		if holder, used := r.owner[cluster][cand]; (used && holder != key) || taken[cand] {
			cand = extendedNodeID(cluster, n.Name, id)
			rec.Replaced = true
		}
		rec.ID = cand
		e := &store.Identity{ClusterID: cluster, Ident: key, RecordID: cand, Name: n.Name, FirstSeen: now, LastSeen: now}
		r.put(e)
		r.mark(cluster, key)
		out[n.Key] = rec
		taken[cand] = true
	}
	return out
}

func (r *Registry) mark(cluster, ident string) {
	if r.dirty[cluster] == nil {
		r.dirty[cluster] = map[string]bool{}
	}
	r.dirty[cluster][ident] = true
}

// Dirty returns the entries that changed since the last call, for the caller to persist.
func (r *Registry) Dirty() []store.Identity {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []store.Identity
	for c, m := range r.dirty {
		for ident := range m {
			if e := r.entries[c][ident]; e != nil {
				out = append(out, *e)
			}
		}
	}
	r.dirty = map[string]map[string]bool{}
	sort.Slice(out, func(i, j int) bool { return out[i].ClusterID+out[i].Ident < out[j].ClusterID+out[j].Ident })
	return out
}

// Requeue marks entries as unsaved again, after a failed write.
func (r *Registry) Requeue(ids []store.Identity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range ids {
		r.mark(e.ClusterID, e.Ident)
	}
}

func prepend(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	out := append([]string{s}, list...)
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}
