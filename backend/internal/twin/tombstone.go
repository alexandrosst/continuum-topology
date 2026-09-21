package twin

import (
	"sort"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/store"
)

// DefaultRetention is how long a tombstone is kept: long enough to explain a change after a weekend, short
// enough that the estate does not fill with the ghosts of every pod-sized workload that ever ran.
const DefaultRetention = 7 * 24 * time.Hour

// MaxTombstones bounds the tombstones of one organisation. The oldest go first.
const MaxTombstones = 5000

// Kinds of record a tombstone can be.
const (
	KindCluster   = "cluster"
	KindNode      = "node"
	KindNamespace = "namespace"
	KindService   = "service"
)

// Vanished is a fact key that an incoming sync removes from what the server holds.
type Vanished struct {
	Kind   string // node | namespace | service
	Key    string // the agent's key for it
	Reason string
}

// Disappeared lists what a sync takes away from the state: everything not in a full picture, and everything a
// delta deletes. tier is the access tier a human approved: facts above it are not reported at all, which is
// consent narrowing, not a deletion, and the reason says so. It changes nothing.
func Disappeared(st *facts.State, s *continuumv1.Sync, tier int) []Vanished {
	var out []Vanished
	add := func(kind, key, reason string) { out = append(out, Vanished{kind, key, reason}) }
	const gone = "no longer reported by its agent (deleted, or outside what the agent is told to look at)"
	const removed = "deleted in the cluster"
	if s.Full {
		in := map[string]bool{}
		for _, n := range s.Nodes {
			in[n.Key] = true
		}
		for k := range st.Nodes {
			if !in[k] {
				add(KindNode, k, why(tier < 1, "access tier lowered: nodes are no longer reported", gone))
			}
		}
		in = map[string]bool{}
		for _, n := range s.Namespaces {
			in[n.Key] = true
		}
		for k := range st.Namespaces {
			if !in[k] {
				add(KindNamespace, k, why(tier < 2, "access tier lowered: namespaces are no longer reported", gone))
			}
		}
		in = map[string]bool{}
		for _, w := range s.Workloads {
			in[w.Key] = true
		}
		for k := range st.Workloads {
			if !in[k] {
				add(KindService, k, why(tier < 2, "access tier lowered: workloads are no longer reported", gone))
			}
		}
	}
	// A delta deletes only what the server holds, and only what the same message does not bring back.
	nodesIn := map[string]bool{}
	for _, n := range s.Nodes {
		nodesIn[n.Key] = true
	}
	nsIn := map[string]bool{}
	for _, n := range s.Namespaces {
		nsIn[n.Key] = true
	}
	wlIn := map[string]bool{}
	for _, w := range s.Workloads {
		wlIn[w.Key] = true
	}
	seen := map[[2]string]bool{}
	for _, v := range out {
		seen[[2]string{v.Kind, v.Key}] = true
	}
	for _, k := range s.DeletedNodes {
		if _, ok := st.Nodes[k]; ok && !nodesIn[k] && !seen[[2]string{KindNode, k}] {
			add(KindNode, k, removed)
		}
	}
	for _, k := range s.DeletedNamespaces {
		if _, ok := st.Namespaces[k]; ok && !nsIn[k] && !seen[[2]string{KindNamespace, k}] {
			add(KindNamespace, k, removed)
		}
	}
	for _, k := range s.DeletedWorkloads {
		if _, ok := st.Workloads[k]; ok && !wlIn[k] && !seen[[2]string{KindService, k}] {
			add(KindService, k, removed)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func why(consent bool, ifConsent, otherwise string) string {
	if consent {
		return ifConsent
	}
	return otherwise
}

// Tombstones is the in-memory set of one organisation's tombstones. It is safe for concurrent use; the caller
// persists what Dirty returns.
type Tombstones struct {
	mu        sync.Mutex
	items     map[store.TombstoneKey]store.Tombstone
	retention time.Duration
	max       int
	upserts   map[store.TombstoneKey]bool
	deletes   map[store.TombstoneKey]bool
}

// NewTombstones makes an empty set; a retention of zero means DefaultRetention.
func NewTombstones(retention time.Duration) *Tombstones {
	if retention <= 0 {
		retention = DefaultRetention
	}
	return &Tombstones{items: map[store.TombstoneKey]store.Tombstone{}, retention: retention, max: MaxTombstones, upserts: map[store.TombstoneKey]bool{}, deletes: map[store.TombstoneKey]bool{}}
}

// Retention is how long a tombstone is kept.
func (t *Tombstones) Retention() time.Duration { return t.retention }

// Load restores what was persisted, without marking it for saving again.
func (t *Tombstones) Load(list []store.Tombstone) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, x := range list {
		t.items[store.TombstoneKey{Kind: x.Kind, ID: x.ID}] = x
	}
}

// Add records that something is gone.
func (t *Tombstones) Add(list ...store.Tombstone) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, x := range list {
		k := store.TombstoneKey{Kind: x.Kind, ID: x.ID}
		t.items[k] = x
		t.upserts[k] = true
		delete(t.deletes, k)
	}
	t.trim()
}

// Revive removes a tombstone because the record is back. It reports whether there was one.
func (t *Tombstones) Revive(kind, id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := store.TombstoneKey{Kind: kind, ID: id}
	if _, ok := t.items[k]; !ok {
		return false
	}
	delete(t.items, k)
	delete(t.upserts, k)
	t.deletes[k] = true
	return true
}

// Sweep drops tombstones older than the retention window and returns them.
func (t *Tombstones) Sweep(now time.Time) []store.Tombstone {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []store.Tombstone
	for k, x := range t.items {
		if now.Sub(x.GoneAt) > t.retention {
			out = append(out, x)
			delete(t.items, k)
			delete(t.upserts, k)
			t.deletes[k] = true
		}
	}
	return out
}

// trim keeps the newest max tombstones. Caller holds the lock.
func (t *Tombstones) trim() {
	if len(t.items) <= t.max {
		return
	}
	all := make([]store.Tombstone, 0, len(t.items))
	for _, x := range t.items {
		all = append(all, x)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].GoneAt.Before(all[j].GoneAt) })
	for _, x := range all[:len(all)-t.max] {
		k := store.TombstoneKey{Kind: x.Kind, ID: x.ID}
		delete(t.items, k)
		delete(t.upserts, k)
		t.deletes[k] = true
	}
}

// Len is how many tombstones are held (including any past retention that a sweep has not yet dropped).
func (t *Tombstones) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.items)
}

// Has reports whether a tombstone exists for the record.
func (t *Tombstones) Has(kind, id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.items[store.TombstoneKey{Kind: kind, ID: id}]
	return ok
}

// List returns the tombstones within the retention window, newest first.
func (t *Tombstones) List(now time.Time) []store.Tombstone {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]store.Tombstone, 0, len(t.items))
	for _, x := range t.items {
		if now.Sub(x.GoneAt) <= t.retention {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].GoneAt.Equal(out[j].GoneAt) {
			return out[i].GoneAt.After(out[j].GoneAt)
		}
		return out[i].Kind+out[i].ID < out[j].Kind+out[j].ID
	})
	return out
}

// Dirty returns what must be written and deleted to bring the store in step, and resets the marks.
func (t *Tombstones) Dirty() (put []store.Tombstone, del []store.TombstoneKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.upserts {
		if x, ok := t.items[k]; ok {
			put = append(put, x)
		}
	}
	for k := range t.deletes {
		del = append(del, k)
	}
	t.upserts, t.deletes = map[store.TombstoneKey]bool{}, map[store.TombstoneKey]bool{}
	sort.Slice(put, func(i, j int) bool { return put[i].Kind+put[i].ID < put[j].Kind+put[j].ID })
	sort.Slice(del, func(i, j int) bool { return del[i].Kind+del[i].ID < del[j].Kind+del[j].ID })
	return
}

// Ephemeral says whether a workload disappearing is expected and not worth a tombstone: a Job runs to completion
// and is cleaned up by the cluster, so its disappearance is not news.
func Ephemeral(kind string) bool { return strings.EqualFold(kind, "Job") }
