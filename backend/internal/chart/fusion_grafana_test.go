package chart

import (
	"encoding/json"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type grafanaDatasource struct {
	Name     string         `json:"name"`
	UID      string         `json:"uid"`
	Type     string         `json:"type"`
	URL      string         `json:"url"`
	Editable bool           `json:"editable"`
	JSONData map[string]any `json:"jsonData"`
}

func grafanaDatasources(t *testing.T, r fusionRendered) []grafanaDatasource {
	t.Helper()
	cm, ok := r.configs["f-fusion-grafana-provisioning"]
	if !ok {
		t.Fatalf("no Grafana provisioning ConfigMap: %v", mapKeys(r.configs))
	}
	var doc struct {
		Datasources []grafanaDatasource `json:"datasources"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data["datasources.yaml"]), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Datasources
}

// Grafana comes up knowing the stores: one read-only data source per store that is on, pointing at that store's own
// Service, and linked the way an investigation moves.
func TestFusionGrafanaIsProvisionedWithTheStores(t *testing.T) {
	r := fusionRender(t, "f")
	ds := grafanaDatasources(t, r)
	byUID := map[string]grafanaDatasource{}
	for _, d := range ds {
		byUID[d.UID] = d
		if d.Editable {
			t.Errorf("%s is editable; provisioned data sources are read-only", d.UID)
		}
	}
	for uid, want := range map[string]string{
		"fusion-metrics": "http://f-fusion-prometheus.observability.svc:9090",
		"fusion-logs":    "http://f-fusion-loki.observability.svc:3100",
		"fusion-traces":  "http://f-fusion-tempo.observability.svc:3200",
	} {
		if byUID[uid].URL != want {
			t.Errorf("%s url = %q, want %q", uid, byUID[uid].URL, want)
		}
	}
	if raw, _ := json.Marshal(byUID["fusion-logs"].JSONData); !strings.Contains(string(raw), `"datasourceUid":"fusion-traces"`) {
		t.Errorf("a log line is not linked to its trace: %s", raw)
	}
	if raw, _ := json.Marshal(byUID["fusion-traces"].JSONData); !strings.Contains(string(raw), `"datasourceUid":"fusion-logs"`) {
		t.Errorf("a trace is not linked to its logs: %s", raw)
	}
	// The dashboard is JSON Grafana can load, and only points at data sources that exist.
	var dash struct {
		Panels []struct {
			Title      string `json:"title"`
			Datasource struct {
				UID string `json:"uid"`
			} `json:"datasource"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(r.configs["f-fusion-grafana-dashboards"].Data["arriving.json"]), &dash); err != nil || len(dash.Panels) == 0 {
		t.Fatalf("starter dashboard: %v %+v", err, dash)
	}
	for _, p := range dash.Panels {
		if _, ok := byUID[p.Datasource.UID]; !ok {
			t.Errorf("panel %q uses data source %q, which is not provisioned", p.Title, p.Datasource.UID)
		}
	}
}

// A store that is off is not a data source, and no panel points at it; with Grafana off there is none of it.
func TestFusionGrafanaFollowsWhichStoresAreOn(t *testing.T) {
	r := fusionRender(t, "f", "--set", "loki.enabled=false", "--set", "tempo.enabled=false")
	ds := grafanaDatasources(t, r)
	if len(ds) != 1 || ds[0].UID != "fusion-metrics" {
		t.Fatalf("data sources = %+v, want Prometheus alone", ds)
	}
	if raw, _ := json.Marshal(ds[0].JSONData); strings.Contains(string(raw), "fusion-traces") {
		t.Errorf("Prometheus is linked to a Tempo that is off: %s", raw)
	}
	var dash struct {
		Panels []struct{ Datasource struct{ UID string } } `json:"panels"`
	}
	_ = json.Unmarshal([]byte(r.configs["f-fusion-grafana-dashboards"].Data["arriving.json"]), &dash)
	for _, p := range dash.Panels {
		if p.Datasource.UID != "fusion-metrics" {
			t.Errorf("a panel uses %q with only Prometheus on", p.Datasource.UID)
		}
	}
	off := fusionRender(t, "f", "--set", "grafana.enabled=false")
	if _, ok := off.sets["f-fusion-grafana"]; ok {
		t.Error("Grafana rendered with grafana.enabled=false")
	}
	if len(off.policies) != 1 {
		t.Errorf("policies with Grafana off = %v, want the stores' alone", mapKeys(off.policies))
	}
}

// Nobody can reach Grafana without the server's say-so: no login page, no anonymous access, the sign-in trusted from the
// server's header only, and the things that would leave the cluster or widen access are off.
func TestFusionGrafanaTrustsTheServerAndNothingElse(t *testing.T) {
	r := fusionRender(t, "f", "--set", "switch.managed=true", "--set", "switch.initialReplicas=0")
	g, ok := r.sets["f-fusion-grafana"]
	if !ok {
		t.Fatal("no Grafana StatefulSet")
	}
	if *g.Spec.Replicas != 0 {
		t.Errorf("Grafana replicas = %d, want standing by with the rest of FUSION", *g.Spec.Replicas)
	}
	env := map[string]string{}
	for _, e := range g.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	for k, want := range map[string]string{
		"GF_AUTH_PROXY_ENABLED": "true", "GF_AUTH_DISABLE_LOGIN_FORM": "true", "GF_AUTH_ANONYMOUS_ENABLED": "false",
		"GF_SERVER_SERVE_FROM_SUB_PATH": "true", "GF_PUBLIC_DASHBOARDS_ENABLED": "false", "GF_PLUGINS_PLUGIN_ADMIN_ENABLED": "false",
		"GF_SECURITY_ALLOW_EMBEDDING": "false", "GF_ANALYTICS_REPORTING_ENABLED": "false", "GF_USERS_AUTO_ASSIGN_ORG_ROLE": "Editor",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if !strings.HasSuffix(env["GF_SERVER_ROOT_URL"], "/fusion/grafana/") {
		t.Errorf("root url = %q", env["GF_SERVER_ROOT_URL"])
	}
	sc := g.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("Grafana's root filesystem is writable")
	}
	if out, err := fusionTemplate(t, "f", "--set", "grafana.role=Owner"); err == nil {
		t.Errorf("an unknown Grafana role was accepted:\n%s", out)
	}
}

// Prometheus is told where it is served from, so its own page can be opened through the server.
func TestFusionPrometheusKnowsItsPrefix(t *testing.T) {
	args := fusionRender(t, "f").sets["f-fusion-prometheus"].Spec.Template.Spec.Containers[0].Args
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--web.external-url=http://localhost:9090/fusion/prometheus/") || !strings.Contains(joined, "--web.route-prefix=/") {
		t.Errorf("args = %v", args)
	}
	args = fusionRender(t, "f", "--set", "prometheus.webPrefix=").sets["f-fusion-prometheus"].Spec.Template.Spec.Containers[0].Args
	if strings.Contains(strings.Join(args, " "), "web.external-url") {
		t.Errorf("an empty webPrefix still sets an external url: %v", args)
	}
}
