package chart

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ikhnos/internal/link"
)

type linksDashboard struct {
	UID         string   `json:"uid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Refresh     string   `json:"refresh"`
	Editable    bool     `json:"editable"`
	Panels      []struct {
		ID          int    `json:"id"`
		Type        string `json:"type"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Datasource  struct {
			UID string `json:"uid"`
		} `json:"datasource"`
		FieldConfig struct {
			Defaults struct {
				Unit     string `json:"unit"`
				Mappings []struct {
					Options map[string]struct {
						Text  string `json:"text"`
						Color string `json:"color"`
					} `json:"options"`
				} `json:"mappings"`
				Thresholds struct {
					Steps []struct {
						Color string   `json:"color"`
						Value *float64 `json:"value"`
					} `json:"steps"`
				} `json:"thresholds"`
			} `json:"defaults"`
		} `json:"fieldConfig"`
		Targets []struct {
			RefID  string `json:"refId"`
			Expr   string `json:"expr"`
			Legend string `json:"legendFormat"`
		} `json:"targets"`
	} `json:"panels"`
	Templating struct {
		List []struct {
			Name       string `json:"name"`
			Definition string `json:"definition"`
			Regex      string `json:"regex"`
			IncludeAll bool   `json:"includeAll"`
			AllValue   string `json:"allValue"`
			Multi      bool   `json:"multi"`
		} `json:"list"`
	} `json:"templating"`
}

func loadLinksDashboard(t *testing.T, r fusionRendered) linksDashboard {
	t.Helper()
	raw, ok := r.configs["f-fusion-grafana-dashboards"].Data["links.json"]
	if !ok {
		t.Fatalf("no links.json dashboard: %v", mapKeys(r.configs["f-fusion-grafana-dashboards"].Data))
	}
	var d linksDashboard
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("links.json is not JSON: %v", err)
	}
	return d
}

// The cluster-links dashboard is provisioned with the others, is shaped like them (tag, refresh, a cluster and a peer picker
// that depends on it) and is part of what the Grafana pod's checksum covers, so a changed dashboard rolls the pod.
func TestFusionLinksDashboardIsProvisioned(t *testing.T) {
	full := fusionRender(t, "f")
	d := loadLinksDashboard(t, full)
	if d.UID != "ikhnos-links" || d.Title != "Cluster links" || d.Refresh != "30s" || d.Editable {
		t.Errorf("uid %q title %q refresh %q editable %v", d.UID, d.Title, d.Refresh, d.Editable)
	}
	if !slices.Contains(d.Tags, "ikhnos") {
		t.Errorf("not tagged ikhnos, so it is missing from the navigation menu: %v", d.Tags)
	}
	if !strings.Contains(d.Description, "cluster id") {
		t.Errorf("the description does not say that clusters are shown by id: %q", d.Description)
	}
	var names []string
	for _, v := range d.Templating.List {
		names = append(names, v.Name)
		if !v.IncludeAll || !v.Multi || v.AllValue != ".*" {
			t.Errorf("variable %q: All must match every value (and series without the label): %+v", v.Name, v)
		}
		if v.Name == "peer" && !strings.Contains(v.Definition, `cluster=~"$cluster"`) {
			t.Errorf("the peer list does not depend on the chosen cluster: %s", v.Definition)
		}
		if !strings.Contains(v.Definition, "ikhnos_link_peer_info") {
			t.Errorf("variable %q is not read from the peer info series: %s", v.Name, v.Definition)
		}
	}
	if !slices.Equal(names, []string{"cluster", "peer"}) {
		t.Errorf("variables = %v, want cluster then peer", names)
	}

	provisioned := map[string]bool{}
	for _, ds := range grafanaDatasources(t, full) {
		provisioned[ds.UID] = true
	}
	ids := map[int]bool{}
	rows, panels := 0, 0
	for _, p := range d.Panels {
		if ids[p.ID] {
			t.Errorf("panel id %d is used twice (%q)", p.ID, p.Title)
		}
		ids[p.ID] = true
		if p.Type == "row" {
			rows++
			continue
		}
		panels++
		if p.Datasource.UID != "fusion-metrics" || !provisioned[p.Datasource.UID] {
			t.Errorf("panel %q reads %q, want the metrics store", p.Title, p.Datasource.UID)
		}
		if len(p.Targets) == 0 {
			t.Errorf("panel %q has no query", p.Title)
		}
	}
	if rows < 6 || panels < 30 {
		t.Errorf("%d rows and %d panels: the dashboard has lost its overview, loss and delay, round trip, traffic, path or capacity rows", rows, panels)
	}

	// Pod checksum: the dashboards are part of the ConfigMap the checksum is taken over.
	if a := full.sets["f-fusion-grafana"].Spec.Template.Annotations["checksum/config"]; a == "" {
		t.Error("Grafana's pod has no checksum/config, so a changed dashboard would not roll it")
	}
	// A ConfigMap holds 1 MiB; leave room for the next dashboard.
	total := 0
	for _, v := range full.configs["f-fusion-grafana-dashboards"].Data {
		total += len(v)
	}
	if total > 900<<10 {
		t.Errorf("the dashboards ConfigMap holds %d bytes of its 1 MiB limit", total)
	}

	// Link series are metrics: with Prometheus off the dashboard is not rendered; with only Prometheus on nothing is dropped.
	if _, ok := fusionRender(t, "f", "--set", "prometheus.enabled=false").configs["f-fusion-grafana-dashboards"].Data["links.json"]; ok {
		t.Error("links.json rendered with Prometheus off")
	}
	only := loadLinksDashboard(t, fusionRender(t, "f", "--set", "loki.enabled=false", "--set", "tempo.enabled=false"))
	if len(only.Panels) != len(d.Panels) {
		t.Errorf("with the log and trace stores off the dashboard has %d panels, want all %d (it reads metrics only)", len(only.Panels), len(d.Panels))
	}
}

var (
	linkMetricRE = regexp.MustCompile(`ikhnos_link_[a-z0-9_]+`)
	byClauseRE   = regexp.MustCompile(`\bby\s*\(([^)]*)\)`)
	matcherRE    = regexp.MustCompile(`\b([a-zA-Z_]\w*)\s*(?:=~|!=|!~|=)\s*"`)
)

// linkSeriesLabels are the labels the link series carry (see internal/link): a dashboard that groups or selects by anything
// else would not find it, or would join on a label the series do not have.
var linkSeriesLabels = []string{"cluster", "peer", "direction", "method", "stat", "reason", "point", "streams", "dscp", "protocol", "le", "peer_name"}

// The dashboard shows a cluster by its name where the series carry only its id, by joining the info series the server writes
// into FUSION (fusionapi.ClusterInfoMetric: one series per cluster id, with its name). The join is written the same way
// everywhere so that it can be checked: a cluster the server has no name for must fall back to its id (the "or ignoring" half),
// never drop out of the panel, and a cluster named twice (a rename that is still in Prometheus' lookback) must not make the
// query fail (the topk).
const (
	nameOfCluster = `topk by (cluster) (1, ikhnos_cluster_info)`
	nameOfPeer    = `label_replace(label_replace(` + nameOfCluster + `, "peer", "$1", "cluster", "(.+)"), "peer_name", "$1", "cluster_name", "(.+)")`
)

// namedForms are the ways the dashboard names clusters, each as a function of the expression it is applied to.
var namedForms = []func(core string) string{
	// a pair: the cluster and its peer
	func(e string) string {
		return `(` + e + `) * on (cluster) group_left (cluster_name) ` + nameOfCluster + ` * on (peer) group_left (peer_name) ` + nameOfPeer +
			` or ignoring (cluster_name, peer_name) label_replace(label_replace((` + e + `), "cluster_name", "$1", "cluster", "(.+)"), "peer_name", "$1", "peer", "(.+)")`
	},
	// a cluster alone
	func(e string) string {
		return `(` + e + `) * on (cluster) group_left (cluster_name) ` + nameOfCluster +
			` or ignoring (cluster_name) label_replace((` + e + `), "cluster_name", "$1", "cluster", "(.+)")`
	},
	// the list of clusters, and of peers, for a picker
	func(e string) string {
		return `(` + e + `) * on (cluster) group_left (cluster_name) ` + nameOfCluster +
			` or ignoring (cluster_name) label_replace(` + e + `, "cluster_name", "$1", "cluster", "(.+)")`
	},
	func(e string) string {
		return `(` + e + `) * on (peer) group_left (peer_name) ` + nameOfPeer +
			` or ignoring (peer_name) label_replace(` + e + `, "peer_name", "$1", "peer", "(.+)")`
	},
}

// unnamed returns the expression a named form wraps, and true; an expression that does not use the info series comes back
// as it is. An expression that uses it in any other way is reported by ok being false.
func unnamed(expr string) (core string, named, ok bool) {
	if !strings.Contains(expr, "ikhnos_cluster_info") {
		return expr, false, true
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(expr, "query_result("), ")")
	if !strings.HasPrefix(expr, "query_result(") {
		inner = expr
	}
	if !strings.HasPrefix(inner, "(") {
		return "", true, false
	}
	depth := 0
	for i, c := range inner {
		switch c {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				core = inner[1:i]
				for _, form := range namedForms {
					if form(core) == inner {
						return core, true, true
					}
				}
				return "", true, false
			}
		}
	}
	return "", true, false
}

// directional says whether a family has a direction label: grouping it without one would add the two directions together.
func directional(family string) bool {
	return strings.HasPrefix(family, "ikhnos_link_probe_") ||
		slices.Contains([]string{"throughput_bits_per_second", "throughput_retransmits", "throughput_rtt_seconds", "throughput_test_bytes_total"}, strings.TrimPrefix(family, "ikhnos_link_")) ||
		strings.HasPrefix(family, "ikhnos_link_throughput_udp_")
}

// linkFamily maps a series name to its family: a histogram's series carry _bucket, _sum or _count.
func linkFamily(series string, fams []link.MetricFamily) (string, bool) {
	for _, f := range fams {
		if series == f.Name {
			return f.Name, true
		}
		if f.Type == "histogram" {
			for _, suf := range []string{"_bucket", "_sum", "_count"} {
				if series == f.Name+suf {
					return f.Name, true
				}
			}
		}
	}
	return "", false
}

// Every link series the dashboard names is one the link package exports (link.MetricFamilies is held to the meter's own
// output by a test of its own), so a rename in the meter fails here instead of leaving an empty panel; it groups and selects
// only by labels those series carry; and it never adds the two directions of a probe together or a pair's two sides.
func TestFusionLinksDashboardNamesOnlyWhatTheMeterExports(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	fams := link.MetricFamilies()
	used := map[string]bool{}
	check := func(where, expr string) {
		series := linkMetricRE.FindAllString(expr, -1)
		if len(series) == 0 {
			t.Errorf("%s: names no link series: %s", where, expr)
		}
		hasDirection := false
		for _, s := range series {
			fam, ok := linkFamily(s, fams)
			if !ok {
				t.Errorf("%s: %s is not exported by the link package: %s", where, s, expr)
				continue
			}
			used[fam] = true
			hasDirection = hasDirection || directional(fam)
		}
		if strings.Contains(expr, "ikhnos_link_jitter_seconds") {
			t.Errorf("%s: reads the passive jitter series, which is being dropped: %s", where, expr)
		}
		for _, m := range matcherRE.FindAllStringSubmatch(expr, -1) {
			if !slices.Contains(linkSeriesLabels, m[1]) {
				t.Errorf("%s: selects on %q, which the link series do not carry: %s", where, m[1], expr)
			}
		}
		if strings.Contains(expr, "ikhnos_cluster_id") {
			t.Errorf("%s: uses the sending resource's label; the pair is the series' own cluster and peer: %s", where, expr)
		}
		for _, m := range byClauseRE.FindAllStringSubmatch(expr, -1) {
			var labels []string
			for _, l := range strings.Split(m[1], ",") {
				labels = append(labels, strings.TrimSpace(l))
			}
			for _, l := range labels {
				if !slices.Contains(linkSeriesLabels, l) {
					t.Errorf("%s: groups by %q, which the link series do not carry: %s", where, l, expr)
				}
			}
			// The peer picker lists the distinct peers of the chosen clusters, so it groups by peer alone.
			if !slices.Contains(labels, "cluster") && where != "variable peer" {
				t.Errorf("%s: groups without cluster, which adds different clusters' series together: %s", where, expr)
			}
			if hasDirection && !slices.Contains(labels, "direction") && !strings.Contains(where, "Pairs probed") && !strings.Contains(where, "Clusters reporting") && !strings.Contains(where, "Pairs with a result") {
				t.Errorf("%s: groups a per-direction series without direction, which adds forward and reverse together: %s", where, expr)
			}
		}
	}
	// core is what a check is about: the expression without the join that puts a name on the series.
	core := func(where, expr string) (string, bool) {
		c, named, ok := unnamed(expr)
		if !ok {
			t.Errorf("%s: uses ikhnos_cluster_info other than in the dashboard's one way of naming clusters: %s", where, expr)
		}
		return c, named
	}
	for _, p := range d.Panels {
		for _, q := range p.Targets {
			c, named := core("panel "+p.Title, q.Expr)
			check("panel "+p.Title, c)
			if p.Type != "stat" && p.Type != "table" {
				if !named {
					t.Errorf("panel %q: %s does not put cluster names on its series", p.Title, q.Expr)
				}
				if strings.Contains(q.Legend, "{{cluster}}") || strings.Contains(q.Legend, "{{peer}}") || !strings.Contains(q.Legend, "{{cluster_name}}") {
					t.Errorf("panel %q: legend %q should name the cluster (and its peer) by name, not id", p.Title, q.Legend)
				}
			}
		}
	}
	for _, v := range d.Templating.List {
		c, named := core("variable "+v.Name, v.Definition)
		if !named || v.Regex == "" {
			t.Errorf("variable %s lists ids where it could list names: %s", v.Name, v.Definition)
		}
		check("variable "+v.Name, c)
	}
	// The dashboard covers every kind of measurement, not one: a source it forgot would be a gap nobody notices.
	for _, f := range []string{"probe_loss_ratio", "probe_up", "tcp_connect_seconds", "rtt_seconds", "retransmits_total", "path_mtu_bytes", "path_changes_total", "throughput_bits_per_second", "throughput_udp_saturated", "throughput_budget_used_bytes", "throughput_tests_skipped_total"} {
		if !used["ikhnos_link_"+f] {
			t.Errorf("no panel reads ikhnos_link_%s", f)
		}
	}
}

// Absent data must read as no data: a zero is only drawn where it is true. "or vector(0)" is for counting things (zero pairs
// is a fact), never for loss, failures or delay, where it would turn "nothing is measuring" into "healthy".
func TestFusionLinksDashboardNeverDrawsAbsentDataAsHealthy(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	counts := []string{"Pairs probed", "Clusters reporting links"}
	for _, p := range d.Panels {
		for _, q := range p.Targets {
			if strings.Contains(q.Expr, "vector(0)") && !slices.Contains(counts, p.Title) {
				t.Errorf("panel %q turns missing data into zero: %s", p.Title, q.Expr)
			}
		}
	}
	// "Probes failing now" is zero only when probes are reporting.
	for _, p := range d.Panels {
		if p.Title == "Probes failing now" && !strings.Contains(p.Targets[0].Expr, "0 * count(ikhnos_link_probe_up") {
			t.Errorf("failing probes are zero even when nothing reports: %s", p.Targets[0].Expr)
		}
	}
}

// Loss turns amber from 1 % and red from 5 % here as on the Sites screen, and loss is drawn as a fraction.
func TestFusionLinksDashboardLossThresholdsMatchTheSitesScreen(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	seen := 0
	for _, p := range d.Panels {
		if p.Type == "row" || !strings.Contains(strings.ToLower(p.Title), "loss") {
			continue
		}
		if !strings.Contains(p.Targets[0].Expr, "probe_loss_ratio") && !strings.Contains(p.Targets[0].Expr, "probe_packets_lost") {
			continue
		}
		seen++
		var got []float64
		for _, s := range p.FieldConfig.Defaults.Thresholds.Steps {
			if s.Value != nil {
				got = append(got, *s.Value)
			}
		}
		if p.FieldConfig.Defaults.Unit != "percentunit" || !slices.Equal(got, []float64{0.01, 0.05}) {
			t.Errorf("panel %q: unit %q thresholds %v, want percentunit with 0.01 and 0.05", p.Title, p.FieldConfig.Defaults.Unit, got)
		}
	}
	if seen < 3 {
		t.Errorf("found %d probe-loss panels, want the stat and the two graphs", seen)
	}
}

// linksPanel is a panel as the tests below look at it.
type linksPanel struct {
	Type        string
	Description string
	Expr        string
	Colors      map[string]string // value mapping: the state's text to its colour
	Steps       []string          // threshold colours, lowest first
}

// panelNamed returns the panel with the given title; an administrator finds the panels by title, and so do the tests.
func (d linksDashboard) panelNamed(t *testing.T, title string) linksPanel {
	t.Helper()
	for _, q := range d.Panels {
		if q.Title != title {
			continue
		}
		p := linksPanel{Type: q.Type, Description: q.Description, Colors: map[string]string{}}
		if len(q.Targets) > 0 {
			p.Expr = q.Targets[0].Expr
		}
		for _, m := range q.FieldConfig.Defaults.Mappings {
			for _, o := range m.Options {
				p.Colors[o.Text] = o.Color
			}
		}
		for _, s := range q.FieldConfig.Defaults.Thresholds.Steps {
			p.Steps = append(p.Steps, s.Color)
		}
		return p
	}
	t.Fatalf("no panel %q", title)
	return linksPanel{}
}

// A direction whose probe never completed has no last-success series (the meter writes one only after the first success), so
// a "seconds since" panel built on that series alone leaves out exactly the direction that needs looking at: the reverse
// direction that was refused from the first run. The two panels therefore fall back on a series every probed direction has
// and draw it at the top of the scale, one day; the description says what the top means. The expressions themselves were
// run against sample series in promtool, with a direction that never succeeded, one that did and one that did long ago.
func TestFusionLinksDashboardDrawsADirectionThatNeverSucceededAtTheTopOfTheScale(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	for _, c := range []struct{ title, last, present string }{
		{"Seconds since the last completed probe", "ikhnos_link_probe_last_success_timestamp_seconds", "ikhnos_link_probe_up"},
		{"Seconds since the last connection opened", "ikhnos_link_tcp_connect_last_success_timestamp_seconds", "ikhnos_link_tcp_connect_seconds_count"},
	} {
		p := d.panelNamed(t, c.title)
		core, _, ok := unnamed(p.Expr)
		if !ok {
			t.Fatalf("%s: not in the dashboard's way of naming clusters: %s", c.title, p.Expr)
		}
		if !strings.HasPrefix(core, "clamp_max(time() - (max by (") || !strings.HasSuffix(core, "), 86400)") {
			t.Errorf("%s: not clamped to one day: %s", c.title, core)
		}
		if !strings.Contains(core, c.last) || !strings.Contains(core, ") or (0 * max by (") || !strings.Contains(core, c.present) {
			t.Errorf("%s: no fallback on %s for a series that never succeeded: %s", c.title, c.present, core)
		}
		if !strings.Contains(p.Description, "one day") {
			t.Errorf("%s: the description does not say what the top of the scale means: %q", c.title, p.Description)
		}
	}
}

// The probe state has three values: failed, up, and up with every packet lost, which is what a responder that is reachable
// but whose test packets are not counted looks like (the control connection works, nothing arrives). The third is a sum, so
// it must not come out as anything but 2: a loss of 0.995 must not make "up" 1.995, nor a failed run whose last loss figure
// is still in the store make it "up". The expression was run in promtool with those cases.
func TestFusionLinksDashboardProbeStateTellsAnAllLostRunFromAHealthyOne(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	p := d.panelNamed(t, "Probe state")
	core, _, ok := unnamed(p.Expr)
	if !ok {
		t.Fatalf("not in the dashboard's way of naming clusters: %s", p.Expr)
	}
	if !strings.Contains(core, "ikhnos_link_probe_loss_ratio") || !strings.Contains(core, ">= 0.99") || !strings.Contains(core, "== 1) and on (cluster, peer, direction)") {
		t.Errorf("the state does not look at the loss of a run that is up: %s", core)
	}
	if got := []string{p.Colors["failed"], p.Colors["up"], p.Colors["up, all packets lost"]}; !slices.Equal(got, []string{"red", "green", "purple"}) {
		t.Errorf("state colours (failed, up, up with all packets lost) = %v", got)
	}
	for _, want := range []string{"denied", "other", "99 %"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("the description does not explain %q: %q", want, p.Description)
		}
	}
	if r := d.panelNamed(t, "Probe failures by reason"); !strings.Contains(r.Description, "denied") || !strings.Contains(r.Description, "other") {
		t.Errorf("the failure reasons panel does not explain denied and other: %q", r.Description)
	}
	for _, q := range d.Panels {
		if q.Title == "Probe up" || strings.Contains(q.Description, "Probe up") {
			t.Errorf("panel %q still refers to Probe up, which is now Probe state", q.Title)
		}
	}
}

// Amber is a warning on this dashboard. A clock that is not synchronised is a state of the deployment (the delay figure is
// withheld), not a fault, and would otherwise sit amber beside the real warnings; no pair reporting is amber too, because an
// empty dashboard is where something is not set up.
func TestFusionLinksDashboardColoursOnlyWarningsAmber(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	clock := d.panelNamed(t, "Clocks synchronised")
	if c := clock.Colors["not synchronised"]; c == "" || c == "orange" || c == "red" || clock.Steps[0] == "orange" || clock.Steps[0] == "red" {
		t.Errorf("not synchronised is drawn %q (steps %v), a warning colour for something that is not a fault", c, clock.Steps)
	}
	pairs := d.panelNamed(t, "Pairs probed")
	if len(pairs.Steps) < 2 || pairs.Steps[0] != "orange" || pairs.Steps[1] != "green" {
		t.Errorf("zero pairs probed should be orange and any pair green: %v", pairs.Steps)
	}
	if !strings.Contains(pairs.Description, "Sites") || !strings.Contains(pairs.Description, "Setup") {
		t.Errorf("the description does not send the reader to the Setup list on the Sites screen: %q", pairs.Description)
	}
}

// The throughput tests are rare, so a dashboard that cannot say why it is empty reads as broken. The top of the Capacity row
// says whether the tests are on, whether any finished and whether any failed; failures and skips are counted over the whole
// range (they happen hours apart, so a per-interval window draws nothing most of the time); and the throughput figure looks
// back as far as the console does, so a restarted prober does not blank it.
func TestFusionLinksDashboardSaysWhyThroughputIsEmpty(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	by := map[string]int{}
	for i, p := range d.Panels {
		by[p.Title] = i
	}
	capRow := -1
	for i, p := range d.Panels {
		if p.Type == "row" && strings.HasPrefix(p.Title, "Capacity") {
			capRow = i
		}
	}
	if capRow < 0 {
		t.Fatal("no Capacity row")
	}
	for k, title := range []string{"Clusters running throughput tests", "Pairs with a result", "Newest result", "Tests failed in range"} {
		i, ok := by[title]
		if !ok || i != capRow+1+k {
			t.Errorf("%q is not number %d in the Capacity row (at %d, row at %d)", title, k+1, i, capRow)
			continue
		}
		p := d.Panels[i]
		if p.Type != "stat" || len(p.FieldConfig.Defaults.Thresholds.Steps) == 0 {
			t.Errorf("%q: %s", title, p.Type)
		}
	}
	for _, title := range []string{"Throughput tests skipped, by reason", "Throughput tests failed, by reason"} {
		i, ok := by[title]
		if !ok {
			t.Errorf("no panel %q", title)
			continue
		}
		for _, q := range d.Panels[i].Targets {
			if strings.Contains(q.Expr, "$__rate_interval") || !strings.Contains(q.Expr, "[$__range]") {
				t.Errorf("%q counts per interval, so tests hours apart draw nothing: %s", title, q.Expr)
			}
		}
		if d.Panels[i].Type != "table" {
			t.Errorf("%q is a %s, want a table over the range", title, d.Panels[i].Type)
		}
	}
	if i, ok := by["Latest TCP throughput (last 8 days)"]; !ok || !strings.Contains(d.Panels[i].Targets[0].Expr, "last_over_time(ikhnos_link_throughput_bits_per_second") || !strings.Contains(d.Panels[i].Targets[0].Expr, "[8d]") {
		t.Error("the throughput figure does not look back eight days as the console does")
	}
}

// Every family the meter exports is drawn somewhere, or is named here with the reason it is not. A family that is exported and
// never shown is a measurement nobody can see; the list below is short on purpose.
func TestFusionLinksDashboardDrawsEveryFamilyTheMeterExports(t *testing.T) {
	d := loadLinksDashboard(t, fusionRender(t, "f"))
	fams := link.MetricFamilies()
	used := map[string]bool{}
	for _, p := range d.Panels {
		for _, q := range p.Targets {
			for _, s := range linkMetricRE.FindAllString(q.Expr, -1) {
				if f, ok := linkFamily(s, fams); ok {
					used[f] = true
				}
			}
		}
	}
	notDrawn := map[string]string{}
	for _, f := range fams {
		if !used[f.Name] {
			notDrawn[f.Name] = f.Type
		}
	}
	if len(notDrawn) > 0 {
		t.Errorf("exported by the meter and drawn by no panel: %v", notDrawn)
	}
}
