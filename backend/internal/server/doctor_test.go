package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The doctor: one test, no cluster, that follows a label from the agent to the Grafana query and fails where a value is accepted
// by one layer and dropped, or cannot be joined, by the next. The vocabulary (fusionapi.Vocabulary) is the one table every
// stage holds the rest to; to add a label, add it there and let the failing stage say which chart value or query to change.
//
//	stage 0  the table against the FUSION chart's values and the dashboards          (doctor_vocab_test.go)
//	stage 1  every endpoint the agent sends is stored or counted as dropped          (doctor_agent_test.go)
//	stage 4  what the API asks the stores, and what the dashboards join on           (doctor_queries_test.go)
func TestDoctor(t *testing.T) {
	t.Run("stage 0: the vocabulary is what the chart promotes and the dashboards select", doctorVocabulary)
	t.Run("stage 1: every kind of endpoint an agent sends is stored or counted as dropped", doctorAgentToServer)
	t.Run("stage 4: the API and the dashboards ask the stores for the same things", doctorQueries)
}

// ---- reading a query ----

// matcher is one label matcher of a selector: label, one of = != =~ !~, and the quoted value.
type matcher struct{ label, op, value string }

var matcherRE = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(=~|!~|!=|=)\s*"((?:[^"\\]|\\.)*)"`)

func matchersIn(s string) []matcher {
	var out []matcher
	for _, m := range matcherRE.FindAllStringSubmatch(s, -1) {
		out = append(out, matcher{m[1], m[2], m[3]})
	}
	return out
}

// matchesEmpty is whether the matcher accepts a series that does not have the label at all.
func (m matcher) matchesEmpty() bool {
	switch m.op {
	case "=":
		return m.value == ""
	case "!=":
		return m.value != ""
	}
	re, err := regexp.Compile("^(?:" + m.value + ")$")
	return err != nil || re.MatchString("") == (m.op == "=~")
}

// canSelect is whether a Loki stream selector keeps one matcher that cannot be satisfied by the empty string: Loki answers a
// selector without one with a 500 ("queries require at least one regexp or equality matcher that does not have an
// empty-compatible value"), which is what an empty application picker used to cause.
func canSelect(ms []matcher) bool {
	return slices.ContainsFunc(ms, func(m matcher) bool { return !m.matchesEmpty() })
}

// byList is every label named in a `by (...)`, `on (...)` or `group_left (...)` list of a PromQL expression.
var byListRE = regexp.MustCompile(`\b(?:by|without|on|ignoring|group_left|group_right)\s*\(([^)]*)\)`)

func listedLabels(expr string) []string {
	var out []string
	for _, m := range byListRE.FindAllStringSubmatch(expr, -1) {
		for _, l := range strings.Split(m[1], ",") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
	}
	return out
}

// ---- the dashboards ----

// dashboardQuery is one query of a provisioned dashboard: its text and the kind of store it goes to.
type dashboardQuery struct{ file, store, expr string }

type dashboardVar struct {
	Name       string
	IncludeAll bool
	AllValue   string
	Definition string // the query that lists its options
}

// dashboards reads the FUSION chart's provisioned dashboards from the source tree: the queries of every panel and the
// variables, each by the store it asks (prometheus, loki, tempo).
func dashboards(t *testing.T) (queries []dashboardQuery, vars map[string][]dashboardVar) {
	t.Helper()
	files, _ := filepath.Glob("../chart/continuum-fusion/files/dashboards/*.json")
	if len(files) == 0 {
		t.Fatal("no dashboards found next to the chart")
	}
	vars = map[string][]dashboardVar{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		name := filepath.Base(f)
		var walk func(v any, store string)
		walk = func(v any, store string) {
			switch v := v.(type) {
			case map[string]any:
				if ds, ok := v["datasource"].(map[string]any); ok {
					store, _ = ds["type"].(string)
				}
				if e, ok := v["expr"].(string); ok {
					queries = append(queries, dashboardQuery{name, store, e})
				} else if e, ok := v["query"].(string); ok && store == "tempo" {
					queries = append(queries, dashboardQuery{name, store, e})
				}
				for _, c := range v {
					walk(c, store)
				}
			case []any:
				for _, c := range v {
					walk(c, store)
				}
			}
		}
		walk(d, "")
		for _, v := range d.(map[string]any)["templating"].(map[string]any)["list"].([]any) {
			m := v.(map[string]any)
			dv := dashboardVar{Name: m["name"].(string)}
			dv.IncludeAll, _ = m["includeAll"].(bool)
			dv.AllValue, _ = m["allValue"].(string)
			switch q := m["query"].(type) {
			case string:
				dv.Definition = q
			case map[string]any:
				dv.Definition, _ = q["query"].(string)
			}
			vars[name] = append(vars[name], dv)
			queries = append(queries, dashboardQuery{name, "prometheus", dv.Definition})
		}
	}
	return queries, vars
}

// grafanaVarRE is a variable reference in a query: $name or ${name} or ${name:format}.
var grafanaVarRE = regexp.MustCompile(`\$\{(\w+)(?::\w+)?\}|\$(\w+)`)

// interpolate replaces the dashboard's variables the way Grafana does when each has the value pick gives it. The built-in
// ones ($__interval and friends) are a duration, whatever they are.
func interpolate(expr string, pick func(dashboardVar) string, vars []dashboardVar) string {
	return grafanaVarRE.ReplaceAllStringFunc(expr, func(ref string) string {
		name := strings.Trim(strings.SplitN(ref, ":", 2)[0], "${}")
		if strings.HasPrefix(name, "__") {
			return "1m"
		}
		for _, v := range vars {
			if v.Name == name {
				return pick(v)
			}
		}
		return ref
	})
}
