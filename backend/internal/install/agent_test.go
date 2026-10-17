package install

import (
	"strings"
	"testing"
)

func TestCollectorsStateOnlyWhatIsOn(t *testing.T) {
	if v := (Collectors{}).Values(); v != nil {
		t.Errorf("collectors that are off are not stated, got %v", v)
	}
	got := Flags(Collectors{NodeProbe: true, Measurements: true}.Values())
	if want := "--set nodeProbe.enabled=true --set measurements.enabled=true"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func intp(n int) *int { return &n }

func TestAgentUpgrade(t *testing.T) {
	for _, c := range []struct {
		name string
		up   AgentUpgrade
		want string
	}{
		{"nothing known: the documented defaults", AgentUpgrade{},
			"helm upgrade continuum-agent ./continuum-agent.tgz --namespace continuum-system --reset-then-reuse-values"},
		{"a tier", AgentUpgrade{Tier: intp(0)},
			"helm upgrade continuum-agent ./continuum-agent.tgz --namespace continuum-system --reset-then-reuse-values --set access.tier=0"},
		{"a registry chart is fetched at a version", AgentUpgrade{ChartRef: "oci://r.example/continuum-agent", ChartVersion: "0.4.0", Tier: intp(2)},
			"helm upgrade continuum-agent oci://r.example/continuum-agent --version 0.4.0 --namespace continuum-system --reset-then-reuse-values --set access.tier=2"},
		{"a chart file given by reference has no version", AgentUpgrade{ChartRef: "./agent-0.4.0.tgz", ChartVersion: "0.4.0"},
			"helm upgrade continuum-agent ./agent-0.4.0.tgz --namespace continuum-system --reset-then-reuse-values"},
		{"a registry chart with no known version", AgentUpgrade{ChartRef: "oci://r.example/continuum-agent"},
			"helm upgrade continuum-agent oci://r.example/continuum-agent --namespace continuum-system --reset-then-reuse-values"},
		{"a chart file", AgentUpgrade{ChartFile: "continuum-agent-0.4.0.tgz"},
			"helm upgrade continuum-agent ./continuum-agent-0.4.0.tgz --namespace continuum-system --reset-then-reuse-values"},
		{"where the agent lives", AgentUpgrade{Namespace: "ikhnos", Release: "edge-agent"},
			"helm upgrade edge-agent ./continuum-agent.tgz --namespace ikhnos --reset-then-reuse-values"},
		// The names come from what the agent reported; one that is not a plain Kubernetes name is not pasted.
		{"a reported name that is not plain is dropped", AgentUpgrade{Namespace: "x; rm -rf /", Release: "Bad_Name"},
			"helm upgrade continuum-agent ./continuum-agent.tgz --namespace continuum-system --reset-then-reuse-values"},
		{"a chart reference is quoted", AgentUpgrade{ChartRef: "oci://r.example/it's"},
			"helm upgrade continuum-agent 'oci://r.example/it'\\''s' --namespace continuum-system --reset-then-reuse-values"},
	} {
		got, err := c.up.Render()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestAgentUpgradeRefusesAChartHelmWouldReadAsAnOption(t *testing.T) {
	_, err := AgentUpgrade{ChartRef: "--post-renderer=/tmp/x"}.Render()
	if err == nil || !strings.Contains(err.Error(), "dash") {
		t.Fatalf("a chart beginning with a dash must be refused, got %v", err)
	}
	if _, err := (AgentUpgrade{ChartRef: "oci://x", ChartVersion: "--x"}).Render(); err == nil {
		t.Fatal("a version beginning with a dash must be refused")
	}
}
