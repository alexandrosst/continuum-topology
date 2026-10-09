package server

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"continuum/internal/fusionapi"
)

// vocabNames is the vocabulary by the label the stores give each attribute.
func vocabNames() (names map[string]fusionapi.Label) {
	names = map[string]fusionapi.Label{}
	for _, l := range fusionapi.Vocabulary {
		names[l.Name()] = l
	}
	return names
}

// inFamily is whether a label starts like the vocabulary's (k8s_, continuum_, ...): one that does and is not in the table is a
// typo or a rename nobody followed.
func inFamily(label string) bool {
	for _, l := range fusionapi.Vocabulary {
		if strings.HasPrefix(label, strings.SplitN(l.Name(), "_", 2)[0]+"_") {
			return true
		}
	}
	return false
}

var (
	madeInQueryRE = regexp.MustCompile(`label_replace\([^,]*,\s*"([^"]+)"|label_format\s+((?:\w+="[^"]*",?\s*)+)`)
	assignedRE    = regexp.MustCompile(`(\w+)=`)
	labelValuesRE = regexp.MustCompile(`label_values\([^,)]*,\s*(\w+)\)`)
	traceAttrRE   = regexp.MustCompile(`\b(?:resource|span)\.([a-z][\w.]*)`)
)

func doctorVocabulary(t *testing.T) {
	names := vocabNames()

	t.Run("the table is well formed", func(t *testing.T) {
		seen := map[string]bool{}
		for _, l := range fusionapi.Vocabulary {
			if seen[l.Name()] {
				t.Errorf("%s is listed twice (or collides with another attribute)", l.Attr)
			}
			seen[l.Name()] = true
			stamped := strings.HasPrefix(l.Attr, "continuum.") || strings.HasPrefix(l.Attr, "ikhnos.")
			if stamped != (l.By != "") {
				t.Errorf("%s: By = %q; exactly the continuum.* and ikhnos.* attributes are provenance somebody stamps", l.Attr, l.By)
			}
			if !slices.Contains([]string{"", "agent", "operator", "gateway"}, l.By) {
				t.Errorf("%s: unknown stamper %q", l.Attr, l.By)
			}
		}
	})

	t.Run("Prometheus promotes exactly the attributes the table says", func(t *testing.T) {
		raw, err := os.ReadFile("../chart/continuum-fusion/values.yaml")
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Prometheus struct{ PromoteResourceAttributes []string }
		}
		if err := yaml.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		for _, l := range fusionapi.Vocabulary {
			if l.Promote != slices.Contains(v.Prometheus.PromoteResourceAttributes, l.Attr) {
				t.Errorf("%s: the table says Promote=%v, FUSION's prometheus.promoteResourceAttributes says otherwise; a label that is not promoted is not on a single series", l.Attr, l.Promote)
			}
		}
		for _, a := range v.Prometheus.PromoteResourceAttributes {
			if !slices.ContainsFunc(fusionapi.Vocabulary, func(l fusionapi.Label) bool { return l.Attr == a }) {
				t.Errorf("prometheus.promoteResourceAttributes has %s, which is not in the vocabulary", a)
			}
		}
	})

	queries, vars := dashboards(t)
	info := infoLabels(t)

	t.Run("a dashboard names only labels the vocabulary or the info series has", func(t *testing.T) {
		for _, q := range queries {
			made := map[string]bool{}
			for _, m := range madeInQueryRE.FindAllStringSubmatch(q.expr, -1) {
				made[m[1]] = true
				for _, a := range assignedRE.FindAllStringSubmatch(m[2], -1) {
					made[a[1]] = true
				}
			}
			var used []string
			for _, m := range matchersIn(q.expr) {
				used = append(used, m.label)
			}
			for _, m := range labelValuesRE.FindAllStringSubmatch(q.expr, -1) {
				used = append(used, m[1])
			}
			if q.store == "prometheus" || q.store == "loki" {
				used = append(used, listedLabels(q.expr)...)
			}
			for _, l := range used {
				if _, ok := names[l]; !ok && !info[l] && !made[l] && inFamily(l) {
					t.Errorf("%s: %q selects on %q, which is neither in the vocabulary nor made by the query: %s", q.file, q.store, l, q.expr)
				}
			}
			if q.store == "tempo" {
				for _, m := range traceAttrRE.FindAllStringSubmatch(q.expr, -1) {
					if !slices.ContainsFunc(fusionapi.Vocabulary, func(l fusionapi.Label) bool { return l.Attr == m[1] }) && inFamily(strings.ReplaceAll(m[1], ".", "_")) {
						t.Errorf("%s: TraceQL names %s, which is not in the vocabulary: %s", q.file, m[1], q.expr)
					}
				}
			}
		}
	})

	t.Run("a LogQL stream selector names only index labels", func(t *testing.T) {
		for _, q := range queries {
			if q.store != "loki" {
				continue
			}
			for _, sel := range streamSelectors(q.expr) {
				for _, m := range matchersIn(sel) {
					if l, ok := names[m.label]; ok && !l.Index {
						t.Errorf("%s: {%s} selects on %s, which Loki keeps as structured metadata, so the selector finds nothing; filter on it after a pipe: %s", q.file, sel, m.label, q.expr)
					}
				}
			}
		}
	})

	t.Run("the picker never falls back on absent()", func(t *testing.T) {
		for _, q := range queries {
			if strings.Contains(q.expr, "absent(") {
				t.Errorf("%s decides with absent() what a picker left on All should show; it is true whenever the info series has a gap, which turns one application into the whole cluster: %s", q.file, q.expr)
			}
		}
	})

	t.Run("a LogQL stream selector still selects when the application picker is on All or has nothing to offer", func(t *testing.T) {
		// The options of the application's own pickers (member, service) come from the info series, which is empty for an
		// application with no members, before the first push and while Prometheus is down; every other variable is on All.
		on := func(emptyApplication bool) func(dashboardVar) string {
			return func(v dashboardVar) string {
				switch {
				case emptyApplication && strings.Contains(v.Definition, fusionapi.AppInfoMetric):
					return ""
				case v.AllValue != "":
					return v.AllValue
				}
				return "a|b"
			}
		}
		for _, q := range queries {
			if q.store != "loki" {
				continue
			}
			for _, empty := range []bool{false, true} {
				for _, sel := range streamSelectors(interpolate(q.expr, on(empty), vars[q.file])) {
					if !canSelect(matchersIn(sel)) {
						t.Errorf("%s: with the application picker empty=%v, {%s} has no matcher that excludes an empty value, and Loki answers it with an error: %s", q.file, empty, sel, q.expr)
					}
				}
			}
		}
	})
}

// streamSelectors is the {...} selectors of a LogQL expression, after the label_format templates ({{.name}}) are taken out.
func streamSelectors(expr string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`\{([^{}]*)\}`).FindAllStringSubmatch(regexp.MustCompile(`\{\{.*?\}\}`).ReplaceAllString(expr, ""), -1) {
		out = append(out, m[1])
	}
	return out
}

// infoLabels are the labels of the info series Ikhnos writes (application, member, service_id, ...), as the encoder writes them.
func infoLabels(t *testing.T) map[string]bool {
	t.Helper()
	s := fusionapi.AppInfoSeries{Application: "a", ApplicationID: "a", Service: "s", Namespace: "n", Cluster: "c", ServiceID: "i"}
	out := map[string]bool{}
	for _, series := range decodeAppInfo(t, fusionapi.EncodeAppInfo([]fusionapi.AppInfoSeries{s}, time.Now())) {
		for l := range series {
			out[l] = true
		}
	}
	return out
}
