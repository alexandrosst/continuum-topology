package fusionapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestMetricCategoryByName(t *testing.T) {
	for name, want := range map[string]string{
		"k8s_pod_cpu_usage": CategoryKubernetes, "container_memory_usage_bytes": CategoryKubernetes, "kube_pod_info": CategoryKubernetes,
		"system_cpu_time_seconds_total": CategorySystem, "process_cpu_seconds_total": CategorySystem, "kepler_node_platform_joules_total": CategorySystem,
		"DCGM_FI_DEV_GPU_UTIL": CategorySystem, "dcgm_gpu_utilization": CategorySystem, "up": CategorySystem, "scrape_duration_seconds": CategorySystem, "target_info": CategorySystem, "otelcol_exporter_sent_spans": CategorySystem,
		"http_server_duration_seconds_count": CategoryApplication, "fusion_demo_requests": CategoryApplication, "upstream_requests_total": CategoryApplication,
		"systemd_unit_state": CategoryApplication, "k8sish_thing": CategoryApplication,
	} {
		if got := MetricCategory(name); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
}

// Whatever combination is asked for, the matcher keeps exactly the names MetricCategory puts in those categories.
func TestMetricNameMatcherAgreesWithMetricCategory(t *testing.T) {
	names := []string{"k8s_pod_cpu_usage", "container_cpu_usage", "kube_node_info", "system_memory_usage_bytes", "process_cpu_seconds_total", "kepler_container_joules_total",
		"up", "scrape_series_added", "target_info", "otelcol_receiver_accepted_spans", "http_requests_total", "fusion_demo_requests", "upstream_total", "systemd_x"}
	for _, cats := range [][]string{{CategorySystem}, {CategoryKubernetes}, {CategoryApplication}, {CategorySystem, CategoryKubernetes}, {CategorySystem, CategoryApplication}, {CategoryKubernetes, CategoryApplication}} {
		m := metricNameMatcher(cats)
		var neg bool
		var expr string
		switch {
		case strings.HasPrefix(m, lblName+"!~"):
			neg, expr = true, strings.TrimPrefix(m, lblName+"!~")
		case strings.HasPrefix(m, lblName+"=~"):
			expr = strings.TrimPrefix(m, lblName+"=~")
		default:
			t.Fatalf("%v: %q", cats, m)
		}
		re := regexp.MustCompile(`^(?:` + strings.Trim(expr, `"`) + `)$`) // Prometheus anchors a matcher itself
		for _, n := range names {
			kept := re.MatchString(n) != neg
			want := false
			for _, c := range cats {
				want = want || MetricCategory(n) == c
			}
			if kept != want {
				t.Errorf("%v: %s kept=%v, want %v (matcher %s)", cats, n, kept, want, m)
			}
		}
	}
	if m := metricNameMatcher(nil); m != "" {
		t.Errorf("no categories must be no matcher, got %q", m)
	}
}

func TestParseCategories(t *testing.T) {
	if c, err := ParseCategories(""); c != nil || err != nil {
		t.Errorf("empty: %v %v", c, err)
	}
	if c, err := ParseCategories("system, application,system"); err != nil || len(c) != 2 || c[0] != "system" || c[1] != "application" {
		t.Errorf("%v %v", c, err)
	}
	if c, err := ParseCategories("system,kubernetes,application"); c != nil || err != nil {
		t.Errorf("all three is no filter: %v %v", c, err)
	}
	for _, bad := range []string{"infra", "system,", "Application", strings.Repeat("x", 100)} {
		if _, err := ParseCategories(bad); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// What Loki makes of the namespace matchers, simulated: a missing label is the empty string and a matcher is anchored.
func TestLogCategoryMatchersPartitionTheNamespaces(t *testing.T) {
	cases := map[string]string{"": CategorySystem, "kube-system": CategoryKubernetes, "kube-node-lease": CategoryKubernetes, "shop": CategoryApplication, "kube-systemx": CategoryApplication}
	match := func(m string, ns string) bool {
		for _, op := range []string{`!~`, `=~`, `!=`, `=`} {
			if i := strings.Index(m, op); i > 0 {
				arg := strings.Trim(m[i+len(op):], `"`)
				switch op {
				case `=~`, `!~`:
					ok := regexp.MustCompile(`^(?:` + arg + `)$`).MatchString(ns)
					return ok == (op == `=~`)
				default:
					return (ns == arg) == (op == `=`)
				}
			}
		}
		t.Fatalf("unreadable matcher %q", m)
		return false
	}
	for _, cats := range [][]string{{CategorySystem}, {CategoryKubernetes}, {CategoryApplication}, {CategorySystem, CategoryKubernetes}, {CategorySystem, CategoryApplication}, {CategoryKubernetes, CategoryApplication}} {
		ms := logCategoryMatchers("k8s_namespace_name", cats)
		for ns, cat := range cases {
			kept := true
			for _, m := range ms {
				kept = kept && match(m, ns)
			}
			want := false
			for _, c := range cats {
				want = want || c == cat
			}
			if kept != want {
				t.Errorf("%v: namespace %q (%s) kept=%v, want %v (%v)", cats, ns, cat, kept, want, ms)
			}
		}
	}
	if logCategoryMatchers("x", nil) != nil {
		t.Error("no categories must be no matcher")
	}
	if LogCategory("") != CategorySystem || LogCategory("kube-system") != CategoryKubernetes || LogCategory("shop") != CategoryApplication {
		t.Error("LogCategory")
	}
}

func TestTraceCategoryCondition(t *testing.T) {
	if _, err := traceCategoryCond("resource.k8s.namespace.name", []string{CategorySystem}); statusOf(err) != http.StatusBadRequest {
		t.Errorf("system traces: %v", err)
	}
	if _, err := traceCategoryCond("a", []string{CategorySystem, CategoryApplication}); statusOf(err) != http.StatusBadRequest {
		t.Errorf("system+application: %v", err)
	}
	k, _ := traceCategoryCond("a", []string{CategoryKubernetes})
	if !strings.Contains(k, `a = "kube-system"`) || !strings.Contains(k, " || ") {
		t.Errorf("kubernetes: %s", k)
	}
	a, _ := traceCategoryCond("a", []string{CategoryApplication})
	if !strings.Contains(a, `a != "kube-system"`) || !strings.Contains(a, " && ") || strings.Contains(a, "||") {
		t.Errorf("application: %s", a)
	}
	if c, _ := traceCategoryCond("a", nil); c != "" {
		t.Errorf("none: %s", c)
	}
	// And it reaches the query, next to the token's own limits.
	q, err := TraceFilter{Categories: []string{CategoryApplication}}.traceQL(Scope{Signals: Signals, Namespaces: []string{"shop"}})
	if err != nil || !strings.Contains(q, `!= "kube-system"`) || !strings.Contains(q, `= "shop"`) {
		t.Errorf("%q %v", q, err)
	}
}

func TestCategoryReachesLogSelectorAndEntries(t *testing.T) {
	sel, err := LogFilter{Categories: []string{CategoryKubernetes}}.selector(Scope{Signals: Signals})
	if err != nil || !strings.Contains(sel, `k8s_namespace_name=~"kube-system|kube-public|kube-node-lease"`) || !strings.Contains(sel, `service_name=~".+"`) {
		t.Errorf("%q %v", sel, err)
	}
	for ns, want := range map[string]string{"kube-system": CategoryKubernetes, "shop": CategoryApplication, "": CategorySystem} {
		e, ok := lokiEntry(map[string]string{lblNamespace: ns, lblService: "x"}, []json.RawMessage{json.RawMessage(`"1000"`), json.RawMessage(`"line"`)})
		if !ok || e.Category != want {
			t.Errorf("namespace %q: %+v %v", ns, e, ok)
		}
	}
}
