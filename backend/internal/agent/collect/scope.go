package collect

import (
	"fmt"
	"sort"
	"strings"

	continuumv1 "continuum/gen/continuumv1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// OptOutLabel on a namespace keeps it out of view whatever the agent's own scope says: the people who own
// a namespace can decline to be discovered without asking whoever installed the agent.
const OptOutLabel = "continuum.io/observe"

// Scope says which namespaces the agent reports on. The zero value (or nil) means every namespace, which is
// how the agent has always behaved. A namespace outside the scope is dropped here, in the agent: its
// workloads, services and traffic never leave the cluster, and the server learns only how many there are.
//
// A namespace is in scope when it is selected (named in Include, or matching Selector; with neither set,
// every namespace is selected) and is not named in Exclude and does not carry continuum.io/observe=false.
// The cluster's own system namespaces are always read, because that is where the CNI and ingress
// controller are recognised; the server keeps them out of the topology anyway.
type Scope struct {
	Include  []string
	Exclude  []string
	Selector string
	sel      labels.Selector
}

// ParseScope builds a Scope from the flag values (comma-separated lists and a label selector). The selector
// is matched against the namespace labels the agent keeps, which are the allow-listed ones, so a selector
// on a label the agent would never read is refused at start-up instead of quietly matching nothing.
func ParseScope(include, exclude, selector string) (*Scope, error) {
	s := &Scope{Include: splitList(include), Exclude: splitList(exclude), Selector: strings.TrimSpace(selector)}
	if s.Selector != "" {
		sel, err := labels.Parse(s.Selector)
		if err != nil {
			return nil, fmt.Errorf("scope label selector %q: %w", s.Selector, err)
		}
		reqs, _ := sel.Requirements()
		for _, r := range reqs {
			if !allowed(r.Key(), labelExact, labelPrefix) {
				return nil, fmt.Errorf("scope label selector: the agent does not read the namespace label %q (it keeps only labels on its allow-list, such as continuum.io/*)", r.Key())
			}
		}
		s.sel = sel
	}
	return s, nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Active reports whether the scope restricts anything.
func (s *Scope) Active() bool {
	return s != nil && (len(s.Include) > 0 || len(s.Exclude) > 0 || s.sel != nil)
}

// Describe is the rule in words, for the Agents page.
func (s *Scope) Describe() string {
	if !s.Active() {
		return "all namespaces"
	}
	var parts []string
	if len(s.Include) > 0 || s.sel != nil {
		var sel []string
		if len(s.Include) > 0 {
			sel = append(sel, "namespaces "+strings.Join(s.Include, ", "))
		}
		if s.sel != nil {
			sel = append(sel, "namespaces labelled "+s.Selector)
		}
		parts = append(parts, "only "+strings.Join(sel, " and "))
	}
	if len(s.Exclude) > 0 {
		parts = append(parts, "except "+strings.Join(s.Exclude, ", "))
	}
	return strings.Join(parts, ", ")
}

// Public is the rule as the server is told it. The names of namespaces that are left out are exactly what the
// scope exists to keep in the cluster, so an exclusion is reported as a count; the names that are kept are already sent.
func (s *Scope) Public() string {
	if !s.Active() {
		return "all namespaces"
	}
	c := *s
	c.Exclude = nil
	var parts []string
	if d := c.Describe(); d != "all namespaces" && d != "" {
		parts = append(parts, d)
	}
	switch n := len(s.Exclude); {
	case n == 1:
		parts = append(parts, "1 namespace left out by name")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d namespaces left out by name", n))
	}
	return strings.Join(parts, ", ")
}

func isSystem(ns string) bool {
	return ns == "kube-system" || ns == "kube-public" || ns == "kube-node-lease"
}

// Match reports whether a namespace is in scope. nsLabels are the namespace's allow-listed labels.
func (s *Scope) Match(name string, nsLabels map[string]string) bool {
	if isSystem(name) {
		return true
	}
	if strings.EqualFold(nsLabels[OptOutLabel], "false") {
		return false
	}
	if !s.Active() {
		return true
	}
	for _, x := range s.Exclude {
		if x == name {
			return false
		}
	}
	if len(s.Include) == 0 && s.sel == nil {
		return true
	}
	for _, x := range s.Include {
		if x == name {
			return true
		}
	}
	return s.sel != nil && s.sel.Matches(labels.Set(nsLabels))
}

// SetScope restricts what the collector reports. Call it before Start.
func (c *Collector) SetScope(s *Scope) { c.scope = s }

// SetNamespacedRBAC tells the collector that tier 2's RBAC is a Role per namespace (the chart's rbac.mode=
// namespaced) rather than one cluster-wide ClusterRole. Call it, with SetScope's Include already the namespaces
// that Role covers, before Start; Start then requires a non-empty Include and watches each of those namespaces
// individually, never reading Namespaces or PersistentVolumes at all (no Role, in any namespace, can grant either).
func (c *Collector) SetNamespacedRBAC(v bool) { c.namespaced = v }

// Namespaced reports whether SetNamespacedRBAC(true) was called.
func (c *Collector) Namespaced() bool { return c.namespaced }

// visible returns a function that says whether a namespace's contents may be reported, worked out once from the
// namespaces as they are now. Below tier 2 namespaces are not read and nothing is filtered.
func (c *Collector) visible() func(string) bool {
	if c.ns == nil {
		return func(string) bool { return true }
	}
	in := map[string]bool{}
	sc := c.scopeNow()
	each(c.ns.GetStore().List(), func(n *corev1.Namespace) { in[n.Name] = sc.Match(n.Name, n.Labels) })
	scoped := sc.Active()
	return func(ns string) bool {
		if v, ok := in[ns]; ok {
			return v
		}
		return !scoped // a namespace not seen yet: shown only when nothing restricts the view
	}
}

// scopeFacts counts the namespaces the agent was told to leave out, without naming them.
func (c *Collector) scopeFacts(visible func(string) bool) *continuumv1.ScopeFacts {
	sc := c.scopeNow()
	if c.ns == nil {
		// Below tier 2, or in namespaced mode: Namespace objects were never read, so there is nothing to count
		// against. The rule itself is still worth reporting when it restricts anything - in namespaced mode it
		// always does (scope.namespaces is required), the same as ScopeSummary already reports it.
		if !sc.Active() {
			return nil
		}
		return &continuumv1.ScopeFacts{Description: sc.Public()}
	}
	if !sc.Active() && !c.anyOptOut() {
		return nil
	}
	f := &continuumv1.ScopeFacts{Description: sc.Public()}
	each(c.ns.GetStore().List(), func(n *corev1.Namespace) {
		if isSystem(n.Name) {
			return
		}
		f.NamespacesTotal++
		if visible(n.Name) {
			f.NamespacesInScope++
		}
	})
	if !sc.Active() {
		f.Description = "all namespaces except those labelled " + OptOutLabel + "=false"
	}
	return f
}

func (c *Collector) anyOptOut() bool {
	found := false
	each(c.ns.GetStore().List(), func(n *corev1.Namespace) {
		if strings.EqualFold(n.Labels[OptOutLabel], "false") {
			found = true
		}
	})
	return found
}
