package chart

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ikhnos/internal/fusionapi"
)

// selfDashboard is the "Ikhnos self" dashboard as the tests below look at it.
type selfDashboard struct {
	UID     string   `json:"uid"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Refresh string   `json:"refresh"`
	Links   []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"links"`
	Annotations struct {
		List []struct {
			Expr string `json:"expr"`
		} `json:"list"`
	} `json:"annotations"`
	Templating struct {
		List []struct {
			Name       string `json:"name"`
			Definition string `json:"definition"`
			Regex      string `json:"regex"`
			Multi      bool   `json:"multi"`
			IncludeAll bool   `json:"includeAll"`
			AllValue   string `json:"allValue"`
		} `json:"list"`
	} `json:"templating"`
	Panels []selfPanel `json:"panels"`
}

type selfPanel struct {
	ID          int    `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Datasource  struct {
		UID string `json:"uid"`
	} `json:"datasource"`
	Targets []struct {
		Expr   string `json:"expr"`
		Legend string `json:"legendFormat"`
	} `json:"targets"`
	FieldConfig struct {
		Defaults struct {
			Unit       string           `json:"unit"`
			Mappings   []map[string]any `json:"mappings"`
			Thresholds struct {
				Steps []struct {
					Color string   `json:"color"`
					Value *float64 `json:"value"`
				} `json:"steps"`
			} `json:"thresholds"`
		} `json:"defaults"`
	} `json:"fieldConfig"`
	Options struct {
		Legend struct {
			DisplayMode string   `json:"displayMode"`
			Calcs       []string `json:"calcs"`
		} `json:"legend"`
	} `json:"options"`
}

func loadSelfDashboard(t *testing.T, r fusionRendered) selfDashboard {
	t.Helper()
	raw, ok := r.configs["f-fusion-grafana-dashboards"].Data["ikhnos-self.json"]
	if !ok {
		t.Fatalf("no ikhnos-self.json dashboard: %v", mapKeys(r.configs["f-fusion-grafana-dashboards"].Data))
	}
	var d selfDashboard
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("ikhnos-self.json is not JSON: %v", err)
	}
	return d
}

var selfSeriesRE = regexp.MustCompile(`ikhnos_(?:agent_)?self_[a-z0-9_]+`)

// selfNameOfCluster is how every per-cluster panel puts a cluster's name on the agents' series, which carry only the id: the
// info series the server writes has the id as its label "cluster". A cluster without a name falls back to its id (the "or
// ignoring" half), never drops out of the panel; a cluster named twice (a rename still in the lookback) must not fail the join.
const selfNameOfCluster = `label_replace(topk by (cluster) (1, ikhnos_cluster_info), "ikhnos_cluster_id", "$1", "cluster", "(.+)")`

// The dashboard replaces the System health page: it is provisioned like the others, reads only the series the server writes
// (named by the constants the writer uses, so a rename fails here and not as an empty panel), draws every one of them, shows
// each cluster by name where it shows clusters, and is built the way an operator reads it: states as colours and words,
// units everywhere, a legend table with the latest, mean and highest value.
func TestFusionIkhnosSelfDashboardShowsEverythingTheServerWrites(t *testing.T) {
	full := fusionRender(t, "f")
	d := loadSelfDashboard(t, full)
	if d.UID != "ikhnos-self" || d.Title != "Ikhnos self" || d.Refresh != "30s" || !slices.Contains(d.Tags, "ikhnos") {
		t.Errorf("uid %q title %q refresh %q tags %v", d.UID, d.Title, d.Refresh, d.Tags)
	}
	var dropdown, toDelivery bool
	for _, l := range d.Links {
		dropdown = dropdown || l.Type == "dashboards"
		toDelivery = toDelivery || l.URL == "/d/ikhnos-delivery"
	}
	if !dropdown || !toDelivery {
		t.Errorf("links %+v: want the Ikhnos menu and a way to delivery health", d.Links)
	}
	if len(d.Templating.List) != 1 {
		t.Fatalf("variables %+v", d.Templating.List)
	}
	v := d.Templating.List[0]
	if v.Name != "cluster" || !v.Multi || !v.IncludeAll || v.AllValue != ".*" || !strings.Contains(v.Regex, "(?<text>") || !strings.Contains(v.Regex, "(?<value>") ||
		!strings.Contains(v.Definition, "ikhnos_cluster_info") || !strings.Contains(v.Definition, "ikhnos_agent_self_") {
		t.Errorf("the cluster picker must list the clusters whose agent reports, by name, with All matching everything: %+v", v)
	}
	if len(d.Annotations.List) == 0 || !strings.Contains(d.Annotations.List[0].Expr, "ikhnos_self_connected_agents") {
		t.Errorf("annotations %+v", d.Annotations.List)
	}

	exported := map[string]bool{}
	for _, n := range []string{fusionapi.SelfRSSBytes, fusionapi.SelfGoroutines, fusionapi.SelfCPUPercent, fusionapi.SelfConnectedAgents, fusionapi.SelfFlowIngestBps,
		fusionapi.SelfModelCacheHit, fusionapi.SelfGCPauseMs, fusionapi.AgentSelfRSSBytes, fusionapi.AgentSelfGoroutines, fusionapi.AgentSelfCPUPercent,
		fusionapi.AgentSelfBandwidth, fusionapi.AgentSelfWatts, fusionapi.AgentSelfFlowInterval, fusionapi.AgentSelfProbeInterval} {
		exported[n] = true
	}
	provisioned := map[string]bool{}
	for _, ds := range grafanaDatasources(t, full) {
		provisioned[ds.UID] = true
	}
	drawn := map[string]bool{}
	ids := map[int]bool{}
	stats, series, mapped := 0, 0, 0
	for _, p := range d.Panels {
		if ids[p.ID] {
			t.Errorf("panel id %d is used twice (%q)", p.ID, p.Title)
		}
		ids[p.ID] = true
		if p.Type == "row" {
			continue
		}
		if p.Datasource.UID != "fusion-metrics" || !provisioned[p.Datasource.UID] {
			t.Errorf("panel %q reads %q, want the metrics store", p.Title, p.Datasource.UID)
		}
		if p.Description == "" {
			t.Errorf("panel %q has no description", p.Title)
		}
		if len(p.Targets) == 0 {
			t.Errorf("panel %q has no query", p.Title)
		}
		for _, q := range p.Targets {
			for _, s := range selfSeriesRE.FindAllString(q.Expr, -1) {
				if !exported[s] {
					t.Errorf("panel %q reads %s, which the server does not write", p.Title, s)
				}
				drawn[s] = true
			}
			if strings.Contains(q.Expr, "ikhnos_agent_self_") {
				// the agents' series are keyed by cluster id and selected by the picker
				if !strings.Contains(q.Expr, `ikhnos_cluster_id=~"$cluster"`) && p.Type != "table" {
					t.Errorf("panel %q does not follow the cluster picker: %s", p.Title, q.Expr)
				}
				for _, m := range byClauseRE.FindAllStringSubmatch(strings.ReplaceAll(q.Expr, selfNameOfCluster, ""), -1) {
					if strings.TrimSpace(m[1]) != "ikhnos_cluster_id" {
						t.Errorf("panel %q groups by %q: the agents' series carry the cluster id alone", p.Title, m[1])
					}
				}
			} else if strings.Contains(q.Expr, "$cluster") {
				t.Errorf("panel %q is about the server, which is one process, but follows the cluster picker: %s", p.Title, q.Expr)
			}
			if p.Type == "timeseries" && strings.Contains(q.Expr, "ikhnos_agent_self_") {
				if !strings.Contains(q.Expr, selfNameOfCluster) || !strings.Contains(q.Expr, "group_left (cluster_name)") || !strings.Contains(q.Expr, "or ignoring (cluster_name) label_replace(") {
					t.Errorf("panel %q does not put the cluster's name on its series: %s", p.Title, q.Expr)
				}
				if !strings.Contains(q.Legend, "{{cluster_name}}") || strings.Contains(q.Legend, "ikhnos_cluster_id") {
					t.Errorf("panel %q: legend %q should name the cluster, not show its id", p.Title, q.Legend)
				}
			}
		}
		switch p.Type {
		case "stat":
			stats++
			if len(p.FieldConfig.Defaults.Thresholds.Steps) < 1 {
				t.Errorf("stat %q has no thresholds", p.Title)
			}
			if len(p.FieldConfig.Defaults.Mappings) > 0 {
				mapped++
			}
		case "timeseries":
			series++
			if p.FieldConfig.Defaults.Unit == "" {
				t.Errorf("graph %q has no unit", p.Title)
			}
			if p.Options.Legend.DisplayMode != "table" || !slices.Equal(p.Options.Legend.Calcs, []string{"lastNotNull", "mean", "max"}) {
				t.Errorf("graph %q: legend %+v, want a table with the latest, the mean and the highest", p.Title, p.Options.Legend)
			}
		case "table":
			if len(p.Targets) < 2 {
				t.Errorf("table %q is one query", p.Title)
			}
		}
	}
	for n := range exported {
		if !drawn[n] {
			t.Errorf("the server writes %s and no panel draws it", n)
		}
	}
	if stats < 10 || series < 8 || mapped < 4 {
		t.Errorf("%d stats (%d with value mappings) and %d graphs: the dashboard has lost its overview or its history", stats, mapped, series)
	}
}

// The overview says "not arriving" in words and in red when the server's own series are missing, instead of drawing a good-looking
// zero, and the stat that counts agents never turns a missing server into "none connected".
func TestFusionIkhnosSelfDashboardSaysWhenTheServerIsNotWritingItsFigures(t *testing.T) {
	d := loadSelfDashboard(t, fusionRender(t, "f"))
	var found bool
	for _, p := range d.Panels {
		if p.Title != "Server series" {
			continue
		}
		found = true
		if !strings.Contains(p.Targets[0].Expr, "time() - timestamp(ikhnos_self_rss_bytes)") || p.FieldConfig.Defaults.Unit != "s" {
			t.Errorf("not the age of the newest figure: %+v", p.Targets)
		}
		if len(p.FieldConfig.Defaults.Thresholds.Steps) < 3 || p.FieldConfig.Defaults.Thresholds.Steps[0].Color != "red" {
			t.Errorf("nothing at all must be red: %+v", p.FieldConfig.Defaults.Thresholds.Steps)
		}
		if b, _ := json.Marshal(p.FieldConfig.Defaults.Mappings); !strings.Contains(string(b), "not arriving") || !strings.Contains(string(b), "stale") {
			t.Errorf("mappings %s", b)
		}
	}
	if !found {
		t.Error("no Server series panel")
	}
	for _, p := range d.Panels {
		if p.Title == "Agents connected" && strings.Contains(p.Targets[0].Expr, "vector(0)") {
			t.Errorf("a server that is not writing its figures reads as zero agents: %s", p.Targets[0].Expr)
		}
	}
}
