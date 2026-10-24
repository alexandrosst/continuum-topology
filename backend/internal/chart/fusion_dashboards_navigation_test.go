package chart

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ikhnos/internal/fusionapi"
	"ikhnos/internal/link"
)

// anyDashboard is what the tests below ask of every Ikhnos dashboard.
type anyDashboard struct {
	UID   string   `json:"uid"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	Links []struct {
		Type string   `json:"type"`
		URL  string   `json:"url"`
		Tags []string `json:"tags"`
	} `json:"links"`
	Templating struct {
		List []struct {
			Name       string `json:"name"`
			Definition string `json:"definition"`
			Regex      string `json:"regex"`
		} `json:"list"`
	} `json:"templating"`
	Annotations struct {
		List []struct {
			Name string `json:"name"`
			Expr string `json:"expr"`
		} `json:"list"`
	} `json:"annotations"`
	Panels []struct {
		Type       string `json:"type"`
		Title      string `json:"title"`
		Datasource struct {
			UID string `json:"uid"`
		} `json:"datasource"`
		Targets []struct {
			Expr   string `json:"expr"`
			Legend string `json:"legendFormat"`
		} `json:"targets"`
		Transformations []struct {
			ID string `json:"id"`
		} `json:"transformations"`
		Options struct {
			Legend struct {
				DisplayMode string   `json:"displayMode"`
				Calcs       []string `json:"calcs"`
			} `json:"legend"`
		} `json:"options"`
	} `json:"panels"`
}

var allDashboardNames = []string{"clusters", "workloads", "delivery", "applications", "categories", "links", "ikhnos-self", "fleet", "link-matrix"}

func loadAny(t *testing.T, r fusionRendered, name string) anyDashboard {
	t.Helper()
	var d anyDashboard
	if err := json.Unmarshal([]byte(r.configs["f-fusion-grafana-dashboards"].Data[name+".json"]), &d); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return d
}

// The dashboards are one set: each carries the tag the navigation menu is built from and one of its own, each leads to others that
// exist (a link to a uid that is not provisioned is a click that goes nowhere), every cluster picker offers names (the series
// carry only ids, and the value stays the id so every query means what it did), a graph of Prometheus series names the cluster in
// its legend and never shows its id, and every graph's legend is a table with the latest, mean and highest value.
func TestFusionDashboardsAreOneSetThatNamesItsClusters(t *testing.T) {
	r := fusionRender(t, "f")
	uids := map[string]bool{}
	loaded := map[string]anyDashboard{}
	for _, n := range allDashboardNames {
		d := loadAny(t, r, n)
		loaded[n] = d
		uids[d.UID] = true
	}
	for n, d := range loaded {
		if !slices.Contains(d.Tags, "ikhnos") || len(d.Tags) < 2 {
			t.Errorf("%s: tags %v, want ikhnos and a topic", n, d.Tags)
		}
		menu, nav := false, 0
		for _, l := range d.Links {
			switch l.Type {
			case "dashboards":
				menu = menu || slices.Contains(l.Tags, "ikhnos")
			case "link":
				nav++
				uid := strings.TrimPrefix(l.URL, "/d/")
				if !strings.HasPrefix(l.URL, "/d/") || !uids[uid] || uid == d.UID {
					t.Errorf("%s: link %q does not lead to another provisioned dashboard", n, l.URL)
				}
			}
		}
		if !menu || nav < 2 {
			t.Errorf("%s: the Ikhnos menu is %v and it has %d links to related dashboards", n, menu, nav)
		}
		for _, v := range d.Templating.List {
			if v.Name != "cluster" {
				continue
			}
			if !strings.Contains(v.Regex, "(?<text>") || !strings.Contains(v.Regex, "(?<value>") || !strings.Contains(v.Definition, "ikhnos_cluster_info") {
				t.Errorf("%s: the cluster picker lists ids, not names: %s %s", n, v.Definition, v.Regex)
			}
		}
		for _, p := range d.Panels {
			if p.Type != "timeseries" {
				continue
			}
			if p.Options.Legend.DisplayMode != "table" || !slices.Equal(p.Options.Legend.Calcs, []string{"lastNotNull", "mean", "max"}) {
				t.Errorf("%s: %q: legend %+v is not a table of the latest, mean and highest", n, p.Title, p.Options.Legend)
			}
			for _, q := range p.Targets {
				if p.Datasource.UID == "fusion-metrics" && (strings.Contains(q.Legend, "{{ikhnos_cluster_id}}") || strings.Contains(q.Legend, "{{cluster}}") || strings.Contains(q.Legend, "{{peer}}")) {
					t.Errorf("%s: %q: legend %q shows a cluster id", n, p.Title, q.Legend)
				}
				if strings.Contains(q.Legend, "{{cluster_name}}") && !strings.Contains(q.Expr, "ikhnos_cluster_info") {
					t.Errorf("%s: %q: legend %q asks for a name the query does not join", n, p.Title, q.Legend)
				}
			}
		}
	}
	// A single wrapper puts the name on a vector keyed by ikhnos_cluster_id, in the one way everywhere: an unnamed cluster keeps its
	// id and a cluster named twice does not fail the join.
	const join = `group_left (cluster_name) label_replace(topk by (cluster) (1, ikhnos_cluster_info), "ikhnos_cluster_id", "$1", "cluster", "(.+)") or ignoring (cluster_name) label_replace(`
	for _, n := range []string{"clusters", "delivery", "fleet", "ikhnos-self"} {
		found := 0
		for _, p := range loaded[n].Panels {
			for _, q := range p.Targets {
				if strings.Contains(q.Expr, "group_left (cluster_name)") {
					found++
					if !strings.Contains(q.Expr, join) {
						t.Errorf("%s: %q names clusters in a way the others do not: %s", n, p.Title, q.Expr)
					}
				}
			}
		}
		if found < 4 {
			t.Errorf("%s names clusters in %d queries", n, found)
		}
	}
	// Annotations read series that exist: the nodes' readiness, the containers' restarts, the agents the server holds.
	for n, d := range loaded {
		for _, a := range d.Annotations.List {
			if !regexp.MustCompile(`k8s_node_condition_ready|k8s_container_restarts|ikhnos_self_connected_agents`).MatchString(a.Expr) {
				t.Errorf("%s: annotation %q reads %s", n, a.Name, a.Expr)
			}
		}
	}
}

// The series that count clusters as reporting must not count the ones the server writes about its agents: a cluster whose
// telemetry stopped would look fresh because its agent is connected (and, with no resource labels, the agents' seven gauges
// share one label set, which makes a timestamp() over all series fail).
func TestFusionDashboardsThatTimeTheDataLeaveTheServersOwnSeriesOut(t *testing.T) {
	r := fusionRender(t, "f")
	// A selector with no metric name, which reads every series of the clusters, must leave the agents' gauges out by name.
	nameless := regexp.MustCompile(`\{[^{}]*ikhnos_cluster_id[^{}]*\}`)
	for _, n := range []string{"delivery", "fleet"} {
		seen := 0
		for _, p := range loadAny(t, r, n).Panels {
			for _, q := range p.Targets {
				for _, sel := range nameless.FindAllString(q.Expr, -1) {
					if !strings.Contains(sel, `ikhnos_cluster_id!=""`) && !strings.Contains(sel, `ikhnos_cluster_id=~".+"`) {
						continue // a selector of a metric, not of every series
					}
					seen++
					if !strings.Contains(sel, "__name__!~") || !strings.Contains(sel, "ikhnos_agent_self_") || !strings.Contains(sel, "ikhnos_application_info") {
						t.Errorf("%s: %q: %s reads every series of a cluster, the server's own among them", n, p.Title, sel)
					}
				}
			}
		}
		if seen == 0 {
			t.Errorf("%s: no query times the data of every series any more; has this test lost what it guards?", n)
		}
	}
}

// The fleet overview reads what the server writes (the cluster and application info series, the agents) and what the collectors
// send for the cluster's own objects, and answers "which cluster" by name: every table is built from named queries.
func TestFusionFleetOverview(t *testing.T) {
	r := fusionRender(t, "f")
	d := loadAny(t, r, "fleet")
	if d.UID != "ikhnos-fleet" || d.Title != "Fleet overview" {
		t.Errorf("uid %q title %q", d.UID, d.Title)
	}
	used := map[string]bool{}
	for _, p := range d.Panels {
		if p.Type == "row" {
			continue
		}
		if p.Datasource.UID != "fusion-metrics" {
			t.Errorf("%q reads %q", p.Title, p.Datasource.UID)
		}
		for _, q := range p.Targets {
			if strings.Contains(q.Expr, "target_info") {
				t.Errorf("%q reads target_info, which is only written for some resources: %s", p.Title, q.Expr)
			}
			for _, m := range regexp.MustCompile(`\b(?:ikhnos_[a-z_]+|k8s_[a-z_]+)`).FindAllString(q.Expr, -1) {
				used[m] = true
			}
			if p.Type == "table" && p.Title != "Ikhnos applications" && !strings.Contains(q.Expr, "ikhnos_cluster_info") && p.Title == "Node inventory" {
				t.Errorf("%q has a query that does not put the cluster's name on its rows: %s", p.Title, q.Expr)
			}
		}
	}
	for _, want := range []string{fusionapi.ClusterInfoMetric, fusionapi.AppInfoMetric, fusionapi.SelfConnectedAgents, fusionapi.AgentSelfRSSBytes, fusionapi.AgentSelfCPUPercent,
		"k8s_node_condition_ready", "k8s_pod_phase", "k8s_container_restarts", "k8s_node_cpu_usage", "k8s_node_allocatable_cpu"} {
		if !used[want] {
			t.Errorf("the fleet overview does not read %s", want)
		}
	}
}

// The link quality matrix draws the series the meter exports and nothing else, selects and groups only by labels they carry, takes
// each direction by name (a pair's two directions added together would be neither), puts the reverse direction in the cell of
// the other end, and names the clusters in the one way Cluster links does.
func TestFusionLinkQualityMatrix(t *testing.T) {
	r := fusionRender(t, "f")
	d := loadAny(t, r, "link-matrix")
	if d.UID != "ikhnos-link-matrix" || d.Title != "Link quality matrix" {
		t.Errorf("uid %q title %q", d.UID, d.Title)
	}
	fams := link.MetricFamilies()
	matrices := 0
	swapped := regexp.MustCompile(`"swap", "\$1", "cluster", "\(\.\+\)"`)
	for _, p := range d.Panels {
		if p.Type == "row" {
			continue
		}
		isMatrix := len(p.Transformations) == 1 && p.Transformations[0].ID == "groupingToMatrix"
		if p.Datasource.UID != "fusion-metrics" {
			t.Errorf("%q reads %q", p.Title, p.Datasource.UID)
		}
		for _, q := range p.Targets {
			if strings.Contains(q.Expr, "vector(0)") && !strings.HasPrefix(p.Title, "Pairs probed") && !strings.HasPrefix(p.Title, "Clusters running") && !strings.HasPrefix(p.Title, "Pairs with") {
				t.Errorf("%q turns missing data into zero: %s", p.Title, q.Expr)
			}
			core, named, ok := unnamed(q.Expr)
			if !ok {
				t.Errorf("%q names clusters other than in Cluster links' way: %s", p.Title, q.Expr)
				continue
			}
			if p.Type == "table" && !named {
				t.Errorf("%q does not put cluster names on its rows: %s", p.Title, q.Expr)
			}
			series := linkMetricRE.FindAllString(core, -1)
			if len(series) == 0 {
				t.Errorf("%q names no link series: %s", p.Title, q.Expr)
			}
			hasDirection := false
			for _, s := range series {
				fam, ok := linkFamily(s, fams)
				if !ok {
					t.Errorf("%q: %s is not exported by the link package", p.Title, s)
				}
				hasDirection = hasDirection || directional(fam)
			}
			for _, m := range matcherRE.FindAllStringSubmatch(core, -1) {
				if !slices.Contains(linkSeriesLabels, m[1]) {
					t.Errorf("%q selects on %q, which the link series do not carry", p.Title, m[1])
				}
			}
			for _, m := range byClauseRE.FindAllStringSubmatch(core, -1) {
				for _, l := range strings.Split(m[1], ",") {
					if l = strings.TrimSpace(l); !slices.Contains(linkSeriesLabels, l) {
						t.Errorf("%q groups by %q, which the link series do not carry", p.Title, l)
					}
				}
			}
			// The two directions of a probe are never added together (taking the worse of them is fine).
			if hasDirection && (strings.Contains(core, "sum by (cluster, peer)") || strings.Contains(core, "avg by (cluster, peer)")) && !strings.Contains(core, "direction=") {
				t.Errorf("%q adds the two directions of a probe together: %s", p.Title, core)
			}
			// In a matrix the reverse direction is the other end's cell; on the line of a pair it is a column of its own.
			if isMatrix {
				if got, want := len(swapped.FindAllString(core, -1)), strings.Count(core, `direction="reverse"`); (want > 0) != (got > 0) {
					t.Errorf("%q: %d reverse selectors but %d swaps of cluster and peer: the reverse direction must be drawn in the other end's cell", p.Title, want, got)
				}
			} else if p.Type == "table" && len(swapped.FindAllString(core, -1)) > 0 {
				t.Errorf("%q swaps the ends of a pair on a line that is about one pair: %s", p.Title, core)
			}
		}
		if isMatrix {
			matrices++
		}
	}
	if matrices < 6 {
		t.Errorf("%d matrices, want loss, delay, jitter, connect time, throughput and freshness", matrices)
	}
}
