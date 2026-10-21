package kernel

import (
	"fmt"
	"sort"
	"strings"

	"continuum/internal/link"
)

// The pairs of clusters whose link an administrator asked to watch are part of the Settings document, so their shape and the
// checks on them live with it. What is done with the pairs (who measures whom, what each end is told) is connectivity's, in
// links.go, which uses these types.

// maxLinks bounds how many cluster pairs one organisation can ask to have watched.
const maxLinks = 100

// LinkVia is the kind of address a pair's active measurements are pinned to: the classes of package link (empty is not one of them,
// it is the absence of a choice).
type LinkVia string

// WireWords is the list of LinkVia's words, as the generated types give it.
func (LinkVia) WireWords() []string {
	return []string{link.ClassMesh, link.ClassPrivate, link.ClassPublic}
}

// LinkSetting is an administrator's request to watch the link between two clusters. A pair is measured in both directions:
// each cluster is told about the other and nothing else, so a cluster only ever learns the addresses of the clusters it was
// paired with. A pair that is not listed is not measured at all.
//
// By default a listed pair gets the passive link metrics (each cluster's own), the one-way probe and the connect timing, and
// the route trace, and no throughput tests (those load the link, so they are asked for pair by pair). Both clusters get the
// same choices. The one-way probe measures both directions from one end and the connect timing is the same round trip from
// either, so only the cluster whose id sorts first runs them (like the throughput tests), and the other one only serves them;
// the route trace runs on both, as the two directions can take different routes.
type LinkSetting struct {
	A string `json:"a"`
	B string `json:"b"`
	// NoProbe turns the one-way probe, the connect timing and the route trace off for the pair; the passive metrics stay.
	NoProbe bool `json:"noProbe,omitempty"`
	// Via pins the active measurements of the pair (one-way probe, connect timing, route trace, throughput tests) to one kind
	// of the peer's addresses. Empty, the default, follows the address the real traffic between the clusters is seen using. The
	// passive metrics always match against all of the peer's addresses.
	Via LinkVia `json:"via,omitempty"`
	// Throughput, when present, asks for the throughput tests (iperf3) on the pair. Only one cluster of the pair runs
	// them, in both directions, and the other serves them: the one whose id sorts first, so the link is not loaded twice.
	Throughput *LinkThroughput `json:"throughput,omitempty"`
}

// LinkThroughput tunes the throughput tests of one pair.
type LinkThroughput struct {
	// IntervalMinutes replaces the cluster's own time between two sets of tests (10 minutes to 7 days; 0 keeps it).
	IntervalMinutes uint32 `json:"intervalMinutes,omitempty"`
	// BudgetMB is the most the tests may carry on this link in a day, in addition to the cluster's own budget (up to 1 TiB;
	// 0 means only the cluster's budget applies).
	BudgetMB uint32 `json:"budgetMB,omitempty"`
}

const (
	minThroughputMinutes  = 10
	maxThroughputMinutes  = 7 * 24 * 60
	maxThroughputBudgetMB = 1 << 20
)

// NormalizeLinks checks the pairs and puts each in a canonical order (A before B), without duplicates, so the same pair
// entered either way round is one pair.
func NormalizeLinks(in []LinkSetting) ([]LinkSetting, error) {
	if len(in) > maxLinks {
		return nil, fmt.Errorf("at most %d linked cluster pairs", maxLinks)
	}
	at := map[[2]string]int{}
	out := make([]LinkSetting, 0, len(in))
	for _, l := range in {
		l.A, l.B = strings.TrimSpace(l.A), strings.TrimSpace(l.B)
		if l.A == "" || l.B == "" {
			return nil, fmt.Errorf("a linked pair needs two clusters")
		}
		if len(l.A) > 64 || len(l.B) > 64 {
			return nil, fmt.Errorf("a cluster id is at most 64 characters")
		}
		if l.A == l.B {
			return nil, fmt.Errorf("a cluster cannot be linked to itself")
		}
		if l.B < l.A {
			l.A, l.B = l.B, l.A
		}
		if !link.ValidVia(string(l.Via)) {
			return nil, fmt.Errorf("the pair of %s and %s: measure through must be mesh, private or public addresses, or empty to follow the traffic", l.A, l.B)
		}
		if t := l.Throughput; t != nil {
			if t.IntervalMinutes != 0 && (t.IntervalMinutes < minThroughputMinutes || t.IntervalMinutes > maxThroughputMinutes) {
				return nil, fmt.Errorf("throughput tests of %s and %s: the interval must be between %d minutes and 7 days", l.A, l.B, minThroughputMinutes)
			}
			if t.BudgetMB > maxThroughputBudgetMB {
				return nil, fmt.Errorf("throughput tests of %s and %s: the daily budget is at most 1 TiB", l.A, l.B)
			}
			cp := *t
			l.Throughput = &cp
		}
		// The same pair entered twice (or the other way round) is one pair; the last entry's choices stand.
		k := [2]string{l.A, l.B}
		if i, ok := at[k]; ok {
			out[i] = l
			continue
		}
		at[k] = len(out)
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, nil
}

func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
