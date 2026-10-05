package chart

import (
	"strings"
	"testing"
)

const operatorJSON = `{"id":"op-eu1","name":"Athens regional","labels":[{"key":"region","value":"eu-south"},{"key":"env","value":"prod"}]}`

func operatorPipelines(t *testing.T, extra ...string) (map[string]any, map[string]any) {
	t.Helper()
	r := operatorRender(t, extra...)
	cfg := otelConfig(t, r.configmaps["op-regional-operator-config"].Data)
	svc, _ := cfg["service"].(map[string]any)
	pipelines, _ := svc["pipelines"].(map[string]any)
	processors, _ := cfg["processors"].(map[string]any)
	return pipelines, processors
}

// The operator's identity is stamped by one resource processor on every signal pipeline, immediately before
// batch and after anything a user added, with upsert - so nothing a user's own processor sets can win.
func TestRegionalOperatorStampsItsIdentityAndLabelsLastBeforeBatch(t *testing.T) {
	extra := `{"filter/drop_debug":{"error_mode":"ignore"}}`
	pipelines, processors := operatorPipelines(t, "--set-json", "operator="+operatorJSON,
		"--set-json", "processors.extraProcessors="+extra, "--set", "processors.extraProcessorNames[0]=filter/drop_debug",
		"--set", "processors.extraTracesProcessorNames[0]=tail_sampling")
	for _, sig := range []string{"metrics", "logs", "traces"} {
		p, _ := pipelines[sig].(map[string]any)
		got := toStrings(p["processors"])
		n := len(got)
		if n < 2 || got[n-1] != "batch" || got[n-2] != "resource/operator" {
			t.Fatalf("%s processors = %v, want ... resource/operator, batch", sig, got)
		}
	}
	traces, _ := pipelines["traces"].(map[string]any)
	if got := strings.Join(toStrings(traces["processors"]), ","); !strings.Contains(got, "tail_sampling,resource/operator,batch") {
		t.Fatalf("traces processors = %s: the stamp must come after the user's own processors", got)
	}
	ro, _ := processors["resource/operator"].(map[string]any)
	attrs, _ := ro["attributes"].([]any)
	want := map[string]string{"continuum.operator.id": "op-eu1", "continuum.operator.name": "Athens regional", "region": "eu-south", "env": "prod"}
	if len(attrs) != len(want) {
		t.Fatalf("attributes = %+v, want %v", attrs, want)
	}
	for _, a := range attrs {
		m, _ := a.(map[string]any)
		k, _ := m["key"].(string)
		if want[k] == "" || m["value"] != want[k] || m["action"] != "upsert" {
			t.Fatalf("attribute %+v, want value %q with action upsert", m, want[k])
		}
	}
	// The heartbeat pipeline is its own and never carries it.
	if _, ok := pipelines["metrics/heartbeat"]; ok {
		t.Fatal("heartbeat pipeline rendered without heartbeat.enabled")
	}
}

// With no `operator` value there is no resource/operator anywhere: an operator installed before this existed
// renders exactly what it always did.
func TestRegionalOperatorWithoutIdentityRendersNoStampProcessor(t *testing.T) {
	pipelines, processors := operatorPipelines(t)
	if _, ok := processors["resource/operator"]; ok {
		t.Fatalf("resource/operator rendered with no operator value: %+v", processors)
	}
	for sig, v := range pipelines {
		p, _ := v.(map[string]any)
		if strings.Contains(strings.Join(toStrings(p["processors"]), ","), "resource/operator") {
			t.Fatalf("%s pipeline mentions resource/operator", sig)
		}
	}
}

func TestRegionalOperatorSchemaRejectsReservedAndMalformedLabels(t *testing.T) {
	for name, labels := range map[string]string{
		"reserved prefix":  `[{"key":"continuum.region","value":"x"}]`,
		"uppercase prefix": `[{"key":"Continuum.region","value":"x"}]`,
		"bad key":          `[{"key":"has space","value":"x"}]`,
		"empty value":      `[{"key":"region","value":""}]`,
		"too many":         `[{"key":"a","value":"1"},{"key":"b","value":"1"},{"key":"c","value":"1"},{"key":"d","value":"1"},{"key":"e","value":"1"},{"key":"f","value":"1"},{"key":"g","value":"1"},{"key":"h","value":"1"},{"key":"i","value":"1"}]`,
	} {
		if out, err := operatorHelmTemplate(t, "--set-json", `operator={"id":"op-x","name":"x","labels":`+labels+`}`); err == nil {
			t.Fatalf("%s: rendered, want a schema error:\n%s", name, out)
		}
	}
}
