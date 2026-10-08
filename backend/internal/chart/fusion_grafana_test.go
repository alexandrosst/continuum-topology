package chart

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"continuum/internal/fusionapi"

	corev1 "k8s.io/api/core/v1"
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
			Type       string `json:"type"`
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

// Basic auth is off, so Grafana's built-in admin cannot be signed in to with a password from any pod that reaches the
// Service, and the admin's password is neither a literal nor the default: it is read from a Secret the chart generates.
func TestFusionGrafanaBasicAuthIsOffAndAdminPasswordIsFromASecret(t *testing.T) {
	r := fusionRender(t, "f")
	g := r.sets["f-fusion-grafana"].Spec.Template.Spec.Containers[0]
	var pw *corev1.EnvVar
	basic := ""
	for i, e := range g.Env {
		switch e.Name {
		case "GF_AUTH_BASIC_ENABLED":
			basic = e.Value
		case "GF_SECURITY_ADMIN_PASSWORD":
			pw = &g.Env[i]
		case "GF_SECURITY_ADMIN_USER":
			t.Errorf("the chart hardcodes the admin user %q; proxy users are namespaced by the server instead", e.Value)
		}
	}
	if basic != "false" {
		t.Errorf("GF_AUTH_BASIC_ENABLED = %q, want false", basic)
	}
	if pw == nil {
		t.Fatal("GF_SECURITY_ADMIN_PASSWORD is not set, so Grafana's admin is admin:admin")
	}
	if pw.Value != "" || pw.ValueFrom == nil || pw.ValueFrom.SecretKeyRef == nil || pw.ValueFrom.SecretKeyRef.Name != "f-fusion-grafana-admin" || pw.ValueFrom.SecretKeyRef.Key != "admin-password" {
		t.Fatalf("admin password = %+v, want a reference to Secret f-fusion-grafana-admin", pw)
	}
	sec, ok := r.secrets["f-fusion-grafana-admin"]
	if !ok {
		t.Fatalf("no Grafana admin Secret (have %v)", mapKeys(r.secrets))
	}
	if got := sec.Data["admin-password"]; len(got) < 32 || string(got) == "admin" {
		t.Errorf("generated admin password = %q, want 32 random characters", got)
	}
	// Proxy auth, the way people do sign in, is untouched.
	env := map[string]string{}
	for _, e := range g.Env {
		env[e.Name] = e.Value
	}
	if env["GF_AUTH_PROXY_ENABLED"] != "true" || env["GF_AUTH_PROXY_HEADER_NAME"] != "X-WEBAUTH-USER" || env["GF_AUTH_PROXY_AUTO_SIGN_UP"] != "true" {
		t.Errorf("proxy auth is not wired: %v", env)
	}
}

// The admin password survives `helm upgrade`: a Secret that already exists is carried forward, not redrawn.
func TestFusionGrafanaAdminPasswordIsPreservedAcrossUpgrades(t *testing.T) {
	k, kc := startFakeKubernetes(t)
	k.set("rel-fusion-grafana-admin", map[string]string{"admin-password": "kept-from-the-first-install"})
	out := helmTemplateInCluster(t, Fusion, kc)
	want := base64.StdEncoding.EncodeToString([]byte("kept-from-the-first-install"))
	if !strings.Contains(out, "admin-password: "+want) {
		t.Errorf("the existing admin password was not carried forward (lookups: %v):\n%s", k.lookups(), out)
	}
}

// The starter dashboard's metric panels may not read target_info: Prometheus writes it only for a resource that has a
// service.name or service.instance.id, which the infrastructure metrics (hostmetrics, kubelet, cluster state, Kepler) do
// not have, so "Clusters reporting" showed 0 next to a store full of data (checked against a real Prometheus).
func TestStarterDashboardDoesNotDependOnTargetInfo(t *testing.T) {
	r := fusionRender(t, "f")
	body := r.configs["f-fusion-grafana-dashboards"].Data["arriving.json"]
	if strings.Contains(body, "target_info") {
		t.Fatalf("the starter dashboard reads target_info:\n%s", body)
	}
	if !strings.Contains(body, "continuum_cluster_id") {
		t.Fatal("the starter dashboard no longer counts clusters")
	}
}

// The Ikhnos dashboards (clusters and nodes, namespaces and workloads with logs, delivery health) are JSON Grafana can load,
// carry the tag the navigation menu is built from, and only point at data sources that are provisioned. With the log and
// trace stores off their panels are dropped rather than left broken.
func TestFusionIkhnosDashboards(t *testing.T) {
	type dashboard struct {
		UID    string   `json:"uid"`
		Title  string   `json:"title"`
		Tags   []string `json:"tags"`
		Panels []struct {
			Type       string `json:"type"`
			Title      string `json:"title"`
			Datasource struct {
				UID string `json:"uid"`
			} `json:"datasource"`
			Targets []struct {
				Expr  string `json:"expr"`
				Query string `json:"query"`
			} `json:"targets"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Name, AllValue string
				IncludeAll     bool
			} `json:"list"`
		} `json:"templating"`
	}
	load := func(r fusionRendered, name string) dashboard {
		t.Helper()
		raw, ok := r.configs["f-fusion-grafana-dashboards"].Data[name+".json"]
		if !ok {
			t.Fatalf("no %s.json dashboard: %v", name, mapKeys(r.configs["f-fusion-grafana-dashboards"].Data))
		}
		var d dashboard
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("%s.json is not JSON: %v", name, err)
		}
		return d
	}
	full := fusionRender(t, "f")
	provisioned := map[string]bool{}
	for _, d := range grafanaDatasources(t, full) {
		provisioned[d.UID] = true
	}
	uids := map[string]bool{}
	for name, want := range map[string]string{"clusters": "ikhnos-clusters", "workloads": "ikhnos-workloads", "delivery": "ikhnos-delivery", "applications": "ikhnos-applications", "categories": "ikhnos-categories"} {
		d := load(full, name)
		if d.UID != want || d.Title == "" || len(d.Panels) < 8 {
			t.Errorf("%s: uid %q title %q with %d panels", name, d.UID, d.Title, len(d.Panels))
		}
		if uids[d.UID] {
			t.Errorf("two dashboards share the uid %q", d.UID)
		}
		uids[d.UID] = true
		if !strings.Contains(strings.Join(d.Tags, ","), "ikhnos") {
			t.Errorf("%s is not tagged ikhnos, so it is missing from the navigation menu: %v", name, d.Tags)
		}
		for _, p := range d.Panels {
			if p.Type == "row" {
				continue
			}
			if !provisioned[p.Datasource.UID] {
				t.Errorf("%s: panel %q uses data source %q, which is not provisioned", name, p.Title, p.Datasource.UID)
			}
			for _, q := range p.Targets {
				if strings.Contains(strings.ReplaceAll(q.Expr+q.Query, fusionapi.SystemMetricNames, ""), "target_info") {
					t.Errorf("%s: panel %q reads target_info, which is only written for some resources", name, p.Title)
				}
			}
		}
		// "All" must match a series that has no such label at all (an install that never set a cluster id), and must be
		// usable in a Loki stream selector, which refuses a matcher that can match the empty string on its own.
		// The one exception is the Applications dashboard's service: All must be exactly the application's own services
		// (Grafana expands it to the list), not every service there is.
		for _, v := range d.Templating.List {
			if v.IncludeAll && v.AllValue == "" && !(name == "applications" && v.Name == "service") {
				t.Errorf("%s: variable %q has no All value", name, v.Name)
			}
		}
	}
	for _, v := range load(full, "workloads").Templating.List {
		if v.Name == "namespace" && v.AllValue != ".+" {
			t.Errorf("the namespace variable's All is %q; a Loki selector needs one that cannot be empty", v.AllValue)
		}
	}
	for _, e := range full.sets["f-fusion-grafana"].Spec.Template.Spec.Containers[0].Env {
		if e.Name == "GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH" && e.Value != "/etc/grafana/dashboards/clusters.json" {
			t.Errorf("home dashboard = %q", e.Value)
		}
	}

	only := fusionRender(t, "f", "--set", "loki.enabled=false", "--set", "tempo.enabled=false")
	for _, name := range []string{"clusters", "workloads", "delivery", "applications", "categories"} {
		for _, p := range load(only, name).Panels {
			if p.Type != "row" && p.Datasource.UID != "fusion-metrics" {
				t.Errorf("%s with only Prometheus on still has panel %q on %q", name, p.Title, p.Datasource.UID)
			}
		}
	}
	// Without Prometheus there is nothing for their variables to read: only the starter dashboard remains, and it is the home.
	noProm := fusionRender(t, "f", "--set", "prometheus.enabled=false")
	for _, name := range []string{"clusters.json", "workloads.json", "delivery.json", "applications.json", "categories.json"} {
		if _, ok := noProm.configs["f-fusion-grafana-dashboards"].Data[name]; ok {
			t.Errorf("%s rendered with Prometheus off", name)
		}
	}
	for _, e := range noProm.sets["f-fusion-grafana"].Spec.Template.Spec.Containers[0].Env {
		if e.Name == "GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH" && e.Value != "/etc/grafana/dashboards/arriving.json" {
			t.Errorf("home dashboard with Prometheus off = %q", e.Value)
		}
	}
}

// The Applications dashboard reads the series the server writes (ikhnos_application_info, see fusionapi/appinfo.go): its
// variables must come from that series and from nothing a sender may have left out, and its panels must select telemetry only
// by labels that series carries, so that choosing an application filters the same way the API's application filter does.
func TestFusionApplicationsDashboardFollowsTheInfoSeries(t *testing.T) {
	r := fusionRender(t, "f")
	raw := r.configs["f-fusion-grafana-dashboards"].Data["applications.json"]
	var d struct {
		Templating struct {
			List []struct {
				Name, Definition string
				Multi            bool
				IncludeAll       bool
				AllValue         string
			} `json:"list"`
		} `json:"templating"`
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr  string `json:"expr"`
				Query string `json:"query"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"application": false, "service": false, "namespace": false, "cluster": false}
	for _, v := range d.Templating.List {
		if _, ok := want[v.Name]; !ok {
			t.Errorf("unexpected variable %q", v.Name)
		}
		want[v.Name] = true
		if !strings.Contains(v.Definition, "ikhnos_application_info") {
			t.Errorf("variable %q is not read from the info series: %s", v.Name, v.Definition)
		}
		if v.Name == "application" && (v.Multi || v.IncludeAll) {
			t.Error("one application at a time: a service in two applications would make the labels ambiguous")
		}
		if v.Name == "service" && v.AllValue != "" {
			t.Errorf("service All = %q: it must expand to the application's own services, or choosing All shows every service", v.AllValue)
		}
		if (v.Name == "namespace" || v.Name == "cluster") && v.AllValue != ".*" {
			t.Errorf("%s All = %q: a member Ikhnos knows no namespace or cluster for is written without that label, and .+ would hide it", v.Name, v.AllValue)
		}
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("no %q variable", n)
		}
	}
	for _, p := range d.Panels {
		for _, q := range p.Targets {
			e := q.Expr + q.Query
			if strings.Contains(e, "application") && !strings.Contains(e, "ikhnos_application_info") {
				t.Errorf("panel %q filters telemetry by an application label, which telemetry does not carry: %s", p.Title, e)
			}
			if strings.Contains(e, `"$application"`) {
				t.Errorf("panel %q puts the application name in a quoted matcher unescaped (a name with a quote breaks the query): use ${application:doublequote}: %s", p.Title, e)
			}
			if strings.Contains(e, ":regex}") {
				t.Errorf("panel %q formats a variable as a regex inside TraceQL, whose strings reject the escapes: use :pipe: %s", p.Title, e)
			}
		}
	}
	if len(d.Panels) < 12 {
		t.Errorf("%d panels", len(d.Panels))
	}
}

// Findings of the dashboard review, each one a query that returned something other than what its title says.
func TestFusionDashboardQueriesMeanWhatTheirTitlesSay(t *testing.T) {
	r := fusionRender(t, "f")
	exprs := map[string][]string{}
	titles := map[string]string{}
	for _, name := range []string{"applications", "clusters", "delivery", "workloads"} {
		var d struct {
			Panels []struct {
				ID      int    `json:"id"`
				Title   string `json:"title"`
				Targets []struct {
					Expr  string `json:"expr"`
					Query string `json:"query"`
				} `json:"targets"`
			} `json:"panels"`
		}
		if err := json.Unmarshal([]byte(r.configs["f-fusion-grafana-dashboards"].Data[name+".json"]), &d); err != nil {
			t.Fatal(err)
		}
		for _, p := range d.Panels {
			for _, q := range p.Targets {
				exprs[name] = append(exprs[name], q.Expr+q.Query)
				titles[q.Expr+q.Query] = p.Title
			}
			if strings.Contains(p.Title, "per minute") {
				for _, q := range p.Targets {
					e := q.Expr + q.Query
					if strings.Contains(e, "count_over_time") && strings.Contains(e, "$__interval") {
						t.Errorf("%s: %q counts lines per step, not per minute, so its numbers change with the zoom: %s", name, p.Title, e)
					}
				}
			}
		}
	}
	for _, e := range exprs["delivery"] {
		// timestamp() of a range function is the evaluation time, so this was always "0 seconds since data".
		if strings.Contains(e, "timestamp(last_over_time") {
			t.Errorf("delivery: %q asks timestamp(last_over_time(...)), which is the time of the query: %s", titles[e], e)
		}
	}
	for _, e := range exprs["clusters"] {
		if strings.Contains(e, "k8s_node_condition_ready") && strings.Contains(e, "sum(") {
			t.Errorf("clusters: %q sums a condition that is -1 when unknown: %s", titles[e], e)
		}
		if strings.Contains(e, "k8s_node_condition_ready") && strings.Contains(e, "== 0") {
			t.Errorf("clusters: %q misses nodes whose Ready is unknown: %s", titles[e], e)
		}
	}
	// A query that goes into a stat panel with "or vector(0)" shows a healthy 0 when there is nothing to measure; the
	// freshness stat must show "No data" instead.
	for _, e := range exprs["delivery"] {
		if titles[e] == "Slowest cluster, seconds since data" && strings.Contains(e, "vector(0)") {
			t.Errorf("a silent cluster set shows as 0 seconds, i.e. perfectly fresh: %s", e)
		}
	}
	// Telemetry of an application's services is narrowed by the cluster choice in Loki and in every pod query.
	for _, e := range exprs["applications"] {
		switch {
		case strings.Contains(e, "k8s_pod_") || strings.Contains(e, "k8s_container_"):
			for _, kind := range []string{"deployment", "statefulset", "daemonset"} {
				if !strings.Contains(e, "k8s_"+kind+"_name") {
					t.Errorf("applications: %q does not look for %ss: %s", titles[e], kind, e)
				}
			}
		case strings.Contains(e, "service_name=~"):
			if !strings.Contains(e, "continuum_cluster_id=~") {
				t.Errorf("applications: %q ignores the cluster choice: %s", titles[e], e)
			}
		}
	}
	// The Pods table joins four queries: on one key that includes the cluster, not on the pod name alone.
	for _, e := range exprs["workloads"] {
		if strings.Contains(titles[e], "Pods") && strings.Contains(e, "sum by (k8s_pod_name)") {
			t.Errorf("workloads: the Pods table joins on the pod name, which two clusters can share: %s", e)
		}
	}
}

// Every dashboard refreshes by itself and offers the same picker, and the "Telemetry by category" dashboard draws its three
// groups with exactly the rule the API's category filter uses, so a plot and an API call never disagree about what is system.
func TestFusionDashboardsRefreshAndCategories(t *testing.T) {
	r := fusionRender(t, "f")
	for _, name := range []string{"clusters", "workloads", "delivery", "applications", "categories", "arriving"} {
		raw := r.configs["f-fusion-grafana-dashboards"].Data[name+".json"]
		var d struct {
			Refresh    string `json:"refresh"`
			Timepicker struct {
				Intervals []string `json:"refresh_intervals"`
			} `json:"timepicker"`
		}
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Refresh != "30s" || !strings.Contains(strings.Join(d.Timepicker.Intervals, ","), "10s") {
			t.Errorf("%s: refresh %q, picker %v", name, d.Refresh, d.Timepicker.Intervals)
		}
	}
	for _, name := range []string{"categories", "applications"} {
		raw := r.configs["f-fusion-grafana-dashboards"].Data[name+".json"]
		if !strings.Contains(raw, fusionapi.SystemMetricNames) || !strings.Contains(raw, fusionapi.KubernetesMetricNames) {
			t.Errorf("%s does not use the API's system/kubernetes metric families (%s, %s)", name, fusionapi.SystemMetricNames, fusionapi.KubernetesMetricNames)
		}
	}
	raw := r.configs["f-fusion-grafana-dashboards"].Data["categories.json"]
	for _, ns := range fusionapi.SystemNamespaces {
		if !strings.Contains(raw, ns) {
			t.Errorf("categories.json does not treat %s as a system namespace", ns)
		}
	}
	for _, row := range []string{"System", "Kubernetes", "Application"} {
		if !strings.Contains(raw, `"title": "`+row) {
			t.Errorf("categories.json has no %s row", row)
		}
	}
}
