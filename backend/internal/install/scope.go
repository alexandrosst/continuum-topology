package install

import (
	"fmt"
	"regexp"
	"strings"
)

// Scope is which namespaces an agent may report. With none of it set the agent reports every namespace.
type Scope struct {
	// Namespaces are the only ones reported (plus system ones, which are read for detection but never shown). Empty: all.
	Namespaces []string `json:"namespaces"`
	// Exclude are never reported. It wins over everything else.
	Exclude []string `json:"exclude"`
	// Selector adds the namespaces carrying a label, written `key=value` (or just `key`).
	Selector string `json:"selector"`
}

var (
	dnsLabel   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	labelKey   = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`)
	labelValue = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)

	// The agent reads only labels on an allow-list, so a selector on any other key is refused instead of matching nothing.
	selectableKeys     = map[string]bool{"app": true, "k8s-app": true, "istio-injection": true, "istio.io/rev": true, "istio.io/dataplane-mode": true}
	selectablePrefixes = []string{"continuum.io/", "app.kubernetes.io/", "topology.kubernetes.io/"}
)

func selectableKey(k string) bool {
	if selectableKeys[k] {
		return true
	}
	for _, p := range selectablePrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// NamespaceProblems says which of names are not valid Kubernetes namespace names (DNS-1123 labels), worded for a person to act on.
func NamespaceProblems(names []string) []string {
	var out []string
	for _, n := range names {
		if !dnsLabel.MatchString(n) {
			out = append(out, fmt.Sprintf("%q is not a valid namespace name", n))
		}
	}
	return out
}

// Active is whether the scope narrows anything.
func (s Scope) Active() bool {
	return len(s.Namespaces) > 0 || len(s.Exclude) > 0 || strings.TrimSpace(s.Selector) != ""
}

// Problems says what is wrong with the scope, in words a person can act on; none when it is fine.
func (s Scope) Problems() []string {
	out := NamespaceProblems(append(append([]string{}, s.Namespaces...), s.Exclude...))
	if sel := strings.TrimSpace(s.Selector); sel != "" {
		parts := strings.Split(sel, "=")
		k, rest := parts[0], parts[1:]
		switch {
		case !labelKey.MatchString(k):
			out = append(out, fmt.Sprintf("%q is not a valid label key", k))
		case !selectableKey(k):
			out = append(out, fmt.Sprintf("The agent only reads a fixed list of labels, so select on one starting with continuum.io/ (for example continuum.io/scope=yes), not %q", k))
		case len(rest) > 1 || (len(rest) == 1 && !labelValue.MatchString(rest[0])):
			out = append(out, "Write the label as key=value")
		}
	}
	return out
}

// Values are the Helm values that set the scope: none when there is nothing to narrow, or when the scope has a problem, because a
// scope the agent would refuse is not one to print.
func (s Scope) Values() []Value {
	if !s.Active() || len(s.Problems()) > 0 {
		return nil
	}
	var v []Value
	if len(s.Namespaces) > 0 {
		v = append(v, Strings("scope.namespaces", s.Namespaces))
	}
	if len(s.Exclude) > 0 {
		v = append(v, Strings("scope.exclude", s.Exclude))
	}
	if sel := strings.TrimSpace(s.Selector); sel != "" {
		v = append(v, SetString("scope.selector", sel))
	}
	return v
}
