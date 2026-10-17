package install

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTheProcessorCatalog(t *testing.T) {
	var ps []Processor
	if err := json.Unmarshal([]byte(`[
	  {"id":"proc-1","kind":"filter","name":"Drop Debug!","raw":"","config":{"signal":"log","conditions":[{"field":"level","op":"eq","value":"debug"},{"field":"k8s.namespace.name","op":"matches","value":"^k.*","level":"resource"},{"field":"u","op":"neq","value":"O'Brien \"x\""}]}},
	  {"id":"proc-2","kind":"tailSampling","name":"errs","raw":"","config":{"decisionWaitSeconds":10,"policies":[
	    {"name":"a","type":"probabilistic","probabilisticPercent":10,"statusCodes":"","latencyThresholdMs":0},
	    {"name":"b","type":"statusCode","probabilisticPercent":0,"statusCodes":"ERROR, UNSET,,","latencyThresholdMs":0},
	    {"name":"c","type":"latency","probabilisticPercent":0,"statusCodes":"","latencyThresholdMs":500}]}},
	  {"id":"proc-3","kind":"transform","name":"","raw":"","config":{"signal":"trace","statements":[{"action":"set","key":"env","value":"prod"},{"action":"delete","key":"pw","value":""}]}}
	]`), &ps); err != nil {
		t.Fatal(err)
	}
	if k := []string{ps[0].Key(), ps[1].Key(), ps[2].Key()}; !reflect.DeepEqual(k, []string{"filter/drop_debug", "tail_sampling/errs", "transform/proc-3"}) {
		t.Fatalf("keys %v", k)
	}
	if ps[0].Target() != "extraProcessorNames" || ps[1].Target() != "extraTracesProcessorNames" || ps[2].Target() != "extraProcessorNames" {
		t.Fatal("tail sampling alone is traces-only")
	}
	if p := ProcessorProblems(ps[:2]); p != nil {
		t.Fatal(p)
	}
	fresh := Bodies(ps, false)
	want := `{"filter/drop_debug":{"error_mode":"ignore","log_conditions":["log.attributes[\"level\"] == \"debug\"","IsMatch(resource.attributes[\"k8s.namespace.name\"], \"^k.*\")","log.attributes[\"u\"] != \"O'Brien \\\"x\\\"\""]},` +
		`"tail_sampling/errs":{"decision_wait":"10s","policies":[{"name":"a","type":"probabilistic","probabilistic":{"sampling_percentage":10}},{"name":"b","type":"status_code","status_code":{"status_codes":["ERROR","UNSET"]}},{"name":"c","type":"latency","latency":{"threshold_ms":500}}]},` +
		`"transform/proc-3":{"trace_statements":[{"context":"span","statements":["set(attributes[\"env\"], \"prod\")","delete_key(attributes, \"pw\")"]}]}}`
	if fresh != want {
		t.Fatalf("got\n%s\nwant\n%s", fresh, want)
	}
	// An upgrade states the fields the body does not use as empty lists, so that a filter that moved signal drops its old conditions.
	up := Bodies(ps, true)
	for _, w := range []string{`"metric_conditions":[],"trace_conditions":[]}`, `"metric_statements":[],"log_statements":[]}`} {
		if !strings.Contains(up, w) {
			t.Errorf("an upgrade lacks %s:\n%s", w, up)
		}
	}
	if tail := up[strings.Index(up, "tail_sampling"):strings.Index(up, "transform/")]; strings.Contains(tail, "_conditions") || strings.Contains(tail, "_statements") {
		t.Errorf("tail sampling has no fields to clear: %s", tail)
	}
}

func TestARawProcessorBodyIsKeptAsTyped(t *testing.T) {
	p := Processor{ID: "p", Kind: Filter, Name: "raw", Raw: `{"z": 1, "a": {"y": [1, 2.5, "x<y"], "b": null, "y": true}, "z": 2}`}
	want := `{"filter/raw":{"z":2,"a":{"y":true,"b":null}}}`
	for _, upgrade := range []bool{false, true} {
		if got := Bodies([]Processor{p}, upgrade); got != want {
			t.Errorf("upgrade %v: got %s, want %s", upgrade, got, want)
		}
	}
	for raw, problem := range map[string]string{`{`: "not valid JSON", `[1]`: "must be a JSON object", `null`: "must be a JSON object", `"s"`: "must be a JSON object", `{} {}`: "not valid JSON"} {
		got := ProcessorProblems([]Processor{{ID: "p", Kind: Filter, Name: "raw", Raw: raw}})
		if len(got) != 1 || !strings.Contains(got[0], problem) {
			t.Errorf("%s: %q", raw, got)
		}
	}
}

func TestProcessorProblems(t *testing.T) {
	cond := []FilterCondition{{Field: "a", Op: "eq", Value: "b"}}
	for name, c := range map[string]struct {
		ps   []Processor
		want string
	}{
		"no name":              {[]Processor{{ID: "p1", Kind: Filter, Filter: FilterConfig{Conditions: cond}}}, "Every processor needs a name."},
		"no conditions":        {[]Processor{{ID: "p", Kind: Filter, Name: "f"}}, `"f" (filter) has no conditions`},
		"a condition no name":  {[]Processor{{ID: "p", Kind: Filter, Name: "f", Filter: FilterConfig{Conditions: []FilterCondition{{Op: "eq"}}}}}, "has a condition with no attribute name"},
		"a bad regular expr":   {[]Processor{{ID: "p", Kind: Filter, Name: "f", Filter: FilterConfig{Conditions: []FilterCondition{{Field: "a", Op: "matches", Value: "("}}}}}, "regular expression that is not valid"},
		"a regexp not for RE2": {[]Processor{{ID: "p", Kind: Filter, Name: "f", Filter: FilterConfig{Conditions: []FilterCondition{{Field: "a", Op: "matches", Value: "(?=x)"}}}}}, "regular expression that is not valid"},
		"no policies":          {[]Processor{{ID: "p", Kind: TailSampling, Name: "t"}}, "(tail sampling) has no policies"},
		"no statements":        {[]Processor{{ID: "p", Kind: Transform, Name: "t"}}, "(transform) has no statements"},
		"a statement no key":   {[]Processor{{ID: "p", Kind: Transform, Name: "t", Transform: TransformConfig{Statements: []TransformStatement{{Action: "set"}}}}}, "has a statement with no attribute name"},
		"two of one name": {[]Processor{
			{ID: "p", Kind: Filter, Name: "Same", Filter: FilterConfig{Conditions: cond}}, {ID: "q", Kind: Filter, Name: "same!", Filter: FilterConfig{Conditions: cond}}},
			`More than one processor renders to "filter/same"`},
	} {
		got := ProcessorProblems(c.ps)
		if len(got) != 1 || !strings.Contains(got[0], c.want) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := ProcessorProblems([]Processor{{ID: "p", Kind: Filter, Name: "f", Filter: FilterConfig{Conditions: cond}}}); got != nil {
		t.Errorf("a good one has none: %q", got)
	}
}

func TestAProcessorWithNoConfigIsAnEmptyOne(t *testing.T) {
	for _, in := range []string{`{"id":"p","kind":"filter","name":"f","raw":""}`, `{"id":"p","kind":"filter","name":"f","raw":"","config":null}`} {
		var p Processor
		if err := json.Unmarshal([]byte(in), &p); err != nil || p.Kind != Filter || len(p.Filter.Conditions) != 0 {
			t.Errorf("%s: %v %+v", in, err, p)
		}
	}
	var p Processor
	if err := json.Unmarshal([]byte(`{"id":"p","kind":"nope","config":{}}`), &p); err == nil {
		t.Error("an unknown kind is refused")
	}
}

func TestANameUsedThriceIsReportedOnce(t *testing.T) {
	c := []FilterCondition{{Field: "a", Op: "eq"}}
	same := Processor{ID: "p", Kind: Filter, Name: "same", Filter: FilterConfig{Conditions: c}}
	if got := ProcessorProblems([]Processor{same, same, same}); len(got) != 1 {
		t.Fatalf("got %q", got)
	}
}
