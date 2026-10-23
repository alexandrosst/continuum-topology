package install

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// AgentChoices are the Helm values for what a new agent install is asked to switch on or narrow: the collectors, then the scope, then
// whether uninstalling it also deletes its identity Secret. It is the one place that says which values those choices make, and which
// choices an access tier cannot honour: the command the Connect page prints and the controller that installs from a resource both use
// it, so they cannot disagree. A collector that needs more access than the tier gives, or a scope the agent would refuse, is returned
// as a *ProblemsError for the person to correct.
func AgentChoices(tier int, c Collectors, s Scope, purgeIdentity bool) ([]Value, error) {
	var problems []string
	// Each collector needs the access tier that lets the agent read what it counts or times.
	for _, need := range []struct {
		on   bool
		name string
		tier int
	}{{c.NodeProbe, "the node probe", 1}, {c.FlowObserver, "the traffic observer", 2}, {c.Measurements, "path measurements", 2}} {
		if need.on && tier < need.tier {
			problems = append(problems, fmt.Sprintf("%s needs access tier %d or higher", need.name, need.tier))
		}
	}
	problems = append(problems, s.Problems()...)
	if len(problems) > 0 {
		return nil, &ProblemsError{Problems: problems}
	}
	v := append(c.Values(), s.Values()...)
	if purgeIdentity {
		v = append(v, Set("identity.purgeOnUninstall", "true"))
	}
	return v, nil
}

// HelmValues is what Helm makes of the values when they are given on the command line: a nested document, each dotted key a path into
// it, a plain value read as a boolean, a number or a string the way `--set` reads it, a string value left a string, a list of names a
// list and a JSON value the document it holds. A later value replaces an earlier one at the same key. It is what a program that
// installs the chart itself passes where the command line would have passed the flags, and a test holds it to what Helm itself makes
// of the same flags.
func HelmValues(vs []Value) (map[string]any, error) {
	out := map[string]any{}
	for _, v := range vs {
		if v.Key == "" || strings.HasPrefix(v.Key, "-") || strings.ContainsAny(v.Key, "= \t\r\n") {
			return nil, fmt.Errorf("install: %q is not a Helm value name", v.Key)
		}
		var val any
		switch v.Kind {
		case Plain:
			val = plainValue(v.Value)
		case String:
			val = v.Value
		case JSON:
			if err := json.Unmarshal([]byte(v.Value), &val); err != nil {
				return nil, fmt.Errorf("install: the value of %s is not JSON: %w", v.Key, err)
			}
		case List:
			items := make([]any, len(v.Items))
			for i, it := range v.Items {
				items[i] = it
			}
			val = items
		default:
			return nil, fmt.Errorf("install: the value of %s has a kind this does not know", v.Key)
		}
		if err := setPath(out, strings.Split(v.Key, "."), val); err != nil {
			return nil, fmt.Errorf("install: %s: %w", v.Key, err)
		}
	}
	return out, nil
}

// plainValue reads a `--set` value the way Helm does for the plain ones this package writes: true and false are booleans, null is
// nothing, an integer is a number, anything else is a string. (Helm reads these as int64; a float is not written anywhere.)
func plainValue(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && strconv.FormatInt(n, 10) == s {
		return n
	}
	return s
}

func setPath(m map[string]any, path []string, val any) error {
	for i, p := range path {
		if p == "" {
			return fmt.Errorf("an empty name in the path")
		}
		if i == len(path)-1 {
			m[p] = val
			return nil
		}
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	return nil
}
