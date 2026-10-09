package server

import (
	"slices"
	"strings"
	"testing"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
)

// The chart stages render the real charts with `helm template`, and skip when helm is not installed (the repository has no
// Go chart loader: every chart test shells out). They read a rendered OpenTelemetry Collector the way the stores' queries
// will meet its data.

func asList(v any) []any { l, _ := v.([]any); return l }

// resourceAttrs is what a resource processor of a collector configuration sets: attribute -> its {key, value, action}.
func resourceAttrs(cfg map[string]any, processor string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, a := range asList(asMap(asMap(cfg["processors"])[processor])["attributes"]) {
		out[asMap(a)["key"].(string)] = asMap(a)
	}
	return out
}

// pipelines is every pipeline of a collector configuration, by name, with the processors it runs in order.
func pipelines(cfg map[string]any) map[string][]string {
	out := map[string][]string{}
	for name, p := range asMap(asMap(cfg["service"])["pipelines"]) {
		for _, proc := range asList(asMap(p)["processors"]) {
			out[name] = append(out[name], proc.(string))
		}
	}
	return out
}

// stampedBy is the attributes the vocabulary says one stage of the pipeline stamps.
func stampedBy(by string) (attrs []string) {
	for _, l := range fusionapi.Vocabulary {
		if l.By == by {
			attrs = append(attrs, l.Attr)
		}
	}
	return attrs
}

// setFlags are the chart values a printed command sets, by name.
func setFlags(t *testing.T, cmd string) map[string]string {
	t.Helper()
	out := map[string]string{}
	flags := helmValueFlags(t, cmd)
	for i := 0; i+1 < len(flags); i += 2 {
		k, v, _ := strings.Cut(flags[i+1], "=")
		out[k] = v
	}
	return out
}

// agentValue is the chart value that feeds each attribute an agent stamps; an attribute the vocabulary says an agent stamps
// and this does not know is one nobody has wired into the command yet.
var agentValue = map[string]string{
	"continuum.org.id": "orgId", "continuum.cluster.id": "clusterId", "continuum.intent.id": "intentId", "continuum.scope": "scope",
}

func doctorCommands(t *testing.T) {
	t.Run("the command an agent is given makes the chart stamp what the vocabulary says an agent stamps", func(t *testing.T) {
		a := newIntentRig(t)
		agentID := a.agent(t)
		ag, err := a.st.GetAgent(a.ctx, agentID)
		if err != nil {
			t.Fatal(err)
		}
		doc := a.command(t, map[string]any{"agentId": agentID, "name": "ext", "destination": map[string]any{"kind": "external", "endpoint": "c.example:4317"}})
		fragment := doc["installFragment"].(string)
		r := agentRender(t, staleNothing, "--set telemetry.export.otlp.endpoint=c.example:4317", fragment, "--set telemetry.resource.scope=shop")

		given := setFlags(t, fragment)
		given["telemetry.resource.scope"] = "shop"
		want := map[string]string{}
		for _, attr := range stampedBy("agent") {
			key, ok := agentValue[attr]
			if !ok {
				t.Errorf("the vocabulary says an agent stamps %s, but no chart value is known to feed it", attr)
				continue
			}
			want[attr] = given["telemetry.resource."+key]
		}
		// What the command says is what the cluster is: the id the stores will group by is the one the server gave it.
		if want["continuum.org.id"] != a.core.OrgID || want["continuum.cluster.id"] != ClusterIDFor(a.core.OrgID, ag.Fingerprint) || want["continuum.cluster.id"] != ag.ClusterID {
			t.Errorf("the command stamps org %q and cluster %q; the server knows org %q and cluster %q", want["continuum.org.id"], want["continuum.cluster.id"], a.core.OrgID, ag.ClusterID)
		}
		if ti, err := a.core.GetTelemetryIntent(a.ctx, want["continuum.intent.id"]); err != nil || ti.AgentID != agentID || ti.Status != store.TelemetryIntentActive {
			t.Errorf("the intent the command stamps is not this agent's active one: %v", err)
		}

		cfgs := r.collectors(t)
		if len(cfgs) == 0 {
			t.Fatal("no collector rendered")
		}
		for name, cfg := range cfgs {
			got := resourceAttrs(cfg, "resource/continuum")
			for attr, value := range want {
				if g := got[attr]; g == nil || g["value"] != value || g["action"] != "upsert" {
					t.Errorf("%s: %s is %v, want %q upserted (an insert would keep what a workload set itself)", name, attr, g, value)
				}
			}
			for attr := range got {
				if _, ok := want[attr]; !ok {
					t.Errorf("%s: resource/continuum sets %s, which the vocabulary does not say an agent stamps", name, attr)
				}
			}
			for pipeline, procs := range pipelines(cfg) {
				// Last but for the batch, so that nothing a workload or a user processor sets can override it.
				if n := len(procs); n < 2 || procs[n-2] != "resource/continuum" || procs[n-1] != "batch" {
					t.Errorf("%s: pipeline %s runs %v; resource/continuum must be the last processor before batch", name, pipeline, procs)
				}
			}
		}
	})

	t.Run("the install an operator is given makes the chart stamp what the vocabulary says an operator stamps", func(t *testing.T) {
		a := newIntentRig(t)
		cl := a.approvedCluster(t, "8f3c2a9e-1001-4222-8333-944455556666")
		doc := a.createOperatorDoc(t, a.cookie, extBody("athens", cl))
		op, err := a.st.GetOperator(a.ctx, doc["operator"].(map[string]any)["id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		cfg := onlyCollector(t, operatorRender(t, doc["install"].(string)))
		got := resourceAttrs(cfg, "resource/operator")
		want := map[string]string{"continuum.operator.id": op.ID, "continuum.operator.name": op.Name}
		for _, attr := range stampedBy("operator") {
			if _, ok := want[attr]; !ok {
				t.Errorf("the vocabulary says an operator stamps %s, but no operator value is known to feed it", attr)
			}
		}
		for attr, value := range want {
			if g := got[attr]; g == nil || g["value"] != value || g["action"] != "upsert" {
				t.Errorf("%s is %v, want %q upserted", attr, g, value)
			}
		}
		for attr := range got {
			if _, ok := want[attr]; !ok {
				t.Errorf("resource/operator sets %s, which is neither stamped by the vocabulary's operator nor a label the operator was given", attr)
			}
		}
		for pipeline, procs := range pipelines(cfg) {
			if !slices.Contains(procs, "resource/operator") {
				t.Errorf("pipeline %s never runs resource/operator: %v", pipeline, procs)
			}
		}
	})
}
