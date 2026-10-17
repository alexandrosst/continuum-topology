package install

import (
	"slices"
	"sort"
	"strings"
)

func sortedUnique(xs []string) []string {
	out := slices.Clone(xs)
	sort.Strings(out)
	return slices.Compact(out)
}

// application is the scopes of the application signals that are on.
func (t Telemetry) application() []ScopeOverride {
	var out []ScopeOverride
	if t.Signals.ApplicationMetrics {
		out = append(out, t.Scopes.ApplicationMetrics)
	}
	if t.Signals.ApplicationLogs {
		out = append(out, t.Scopes.ApplicationLogs)
	}
	if t.Signals.Traces {
		out = append(out, t.Scopes.Traces)
	}
	return out
}

// CombinedScope is the one scope the infrastructure signals follow, worked out from the application signals that are on: a namespace is
// kept if ANY of those signals keeps it (so nothing an application signal collects is cut off from the infrastructure data around it),
// and dropped only if EVERY one of them drops it. A signal with no narrowing of its own collects everything, so one such signal makes the
// combined scope the whole cluster. Workloads are narrowed only when every signal that keeps the namespace narrows it to workloads, then
// to the union of those. Nothing to follow when no application signal is on.
func (t Telemetry) CombinedScope() ScopeOverride {
	none := ScopeOverride{Namespaces: []string{}, Exclude: []string{}, Workloads: []Workload{}}
	scopes := t.application()
	if len(scopes) == 0 {
		return none
	}
	for _, s := range scopes {
		if !s.Narrows() {
			return none
		}
	}
	everywhere := false
	var all []string
	for _, s := range scopes {
		if len(s.Namespaces) == 0 {
			everywhere = true
		}
		all = append(all, s.Namespaces...)
	}
	namespaces := []string{}
	if !everywhere {
		namespaces = sortedUnique(all)
	}
	// Dropped by all: in every signal's exclude list.
	exclude := slices.Clone(scopes[0].Exclude)
	for _, s := range scopes[1:] {
		exclude = slices.DeleteFunc(exclude, func(n string) bool { return !slices.Contains(s.Exclude, n) })
	}
	exclude = sortedUnique(exclude)
	if exclude == nil {
		exclude = []string{}
	}
	var candidates []string
	for _, s := range scopes {
		for _, w := range s.Workloads {
			candidates = append(candidates, w.Namespace)
		}
	}
	out := []Workload{}
	for _, ns := range sortedUnique(candidates) {
		// Every signal that collects this namespace must narrow it, or the namespace stays whole.
		var narrowed [][]string
		collecting, whole := 0, false
		for _, s := range scopes {
			collects := slices.Contains(s.Namespaces, ns)
			if len(s.Namespaces) == 0 {
				collects = !slices.Contains(s.Exclude, ns)
			}
			if !collects {
				continue
			}
			collecting++
			i := slices.IndexFunc(s.Workloads, func(w Workload) bool { return w.Namespace == ns })
			if i < 0 {
				whole = true
				break
			}
			narrowed = append(narrowed, s.Workloads[i].Names)
		}
		if collecting == 0 || whole {
			continue
		}
		var union []string
		for _, n := range narrowed {
			union = append(union, n...)
		}
		out = append(out, Workload{Namespace: ns, Names: names(sortedUnique(union))})
	}
	return ScopeOverride{Namespaces: namespaces, Exclude: exclude, Workloads: out}
}

// ScopeTag is what this telemetry covers, for the continuum.scope tag: the namespaces of the application signals that are narrowed
// ("shop; payments"), a namespace narrowed to workloads as "payments: api+worker", plus what they leave out ("- excluding legacy+tmp").
// No commas: the value travels through `helm --set`. Empty when nothing is narrowed (everything the agent can see).
func (t Telemetry) ScopeTag() string {
	c := t.CombinedScope()
	if len(c.Namespaces) == 0 && len(c.Exclude) == 0 && len(c.Workloads) == 0 {
		return ""
	}
	narrowed := map[string][]string{}
	var order []string
	for _, w := range c.Workloads {
		if _, seen := narrowed[w.Namespace]; !seen {
			order = append(order, w.Namespace)
		}
		narrowed[w.Namespace] = w.Names
	}
	workload := func(ns string) string {
		if len(narrowed[ns]) == 0 {
			return ns + ": nothing"
		}
		return ns + ": " + strings.Join(narrowed[ns], "+")
	}
	not := ""
	if len(c.Exclude) > 0 {
		not = " - excluding " + strings.Join(c.Exclude, "+")
	}
	if len(c.Namespaces) == 0 {
		var only []string
		for _, ns := range order {
			only = append(only, workload(ns)+" only")
		}
		s := "all namespaces"
		if len(only) > 0 {
			s += " (" + strings.Join(only, "; ") + ")"
		}
		return s + not
	}
	var parts []string
	for _, n := range sortedUnique(append(slices.Clone(c.Namespaces), order...)) {
		if _, ok := narrowed[n]; ok {
			parts = append(parts, workload(n))
		} else {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, "; ") + not
}
