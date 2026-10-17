package install

import (
	"regexp"
	"strconv"
	"strings"
)

// Collectors are the parts of the agent that run on every node. They are separate opt-ins because nothing else in Ikhnos does that.
type Collectors struct {
	// NodeProbe reads facts about each machine (virtual machine or bare metal, model, kernel).
	NodeProbe bool `json:"nodeProbe"`
	// FlowObserver counts which workloads talk to which. It needs the agent's highest access tier.
	FlowObserver bool `json:"flowObserver"`
	// Measurements times TCP connections to addresses the server names (no data is sent).
	Measurements bool `json:"measurements"`
}

// Values are the Helm values that switch the collectors on. A collector that is off is not stated: the chart's default is off.
func (c Collectors) Values() []Value {
	var v []Value
	if c.NodeProbe {
		v = append(v, Set("nodeProbe.enabled", "true"))
	}
	if c.FlowObserver {
		v = append(v, Set("flowObserver.enabled", "true"))
	}
	if c.Measurements {
		v = append(v, Set("measurements.enabled", "true"))
	}
	return v
}

const (
	// DefaultNamespace and DefaultRelease are where the agent chart installs itself unless told otherwise.
	DefaultNamespace = "continuum-system"
	DefaultRelease   = "continuum-agent"
	// DefaultChartFile is the chart file a person is told to use when the install is not known to have come from a registry.
	DefaultChartFile = "continuum-agent.tgz"
)

// plainName is a release or namespace written as the agent reports it: lowercase letters, digits and dashes, nothing a shell could read.
var plainName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// AgentUpgrade is the command a cluster's owner runs to change an agent that is already installed: raise its access ceiling, change
// its telemetry, or both. It starts from the new chart's defaults and puts what the person set on top (--reset-then-reuse-values), so
// everything not named here stays as it was.
type AgentUpgrade struct {
	// ChartRef is a chart in a registry (oci://... or a repository chart), with ChartVersion the version to fetch; or ChartFile is a
	// chart file the person has. With neither, the chart file is DefaultChartFile.
	ChartRef     string `json:"chartRef"`
	ChartVersion string `json:"chartVersion"`
	ChartFile    string `json:"chartFile"`

	// Namespace and Release are where the agent's release lives, when that is known. A name that is not a plain Kubernetes name
	// (it came from the agent, and ends up in a shell line) is dropped for the default.
	Namespace string `json:"namespace"`
	Release   string `json:"release"`

	// Tier is the access ceiling to set. Nil leaves it as it is.
	Tier *int `json:"tier"`
}

func (u AgentUpgrade) helm() Helm {
	h := Helm{Verb: Upgrade, Release: DefaultRelease, Namespace: DefaultNamespace, ResetThenReuse: true, Layout: OneLine}
	if plainName.MatchString(u.Release) {
		h.Release = u.Release
	}
	if plainName.MatchString(u.Namespace) {
		h.Namespace = u.Namespace
	}
	if u.ChartRef != "" {
		h.Chart = u.ChartRef
		// A registry chart is fetched at a version; a chart file is one version already.
		if !strings.HasSuffix(u.ChartRef, ".tgz") {
			h.Version = u.ChartVersion
		}
	} else if u.ChartFile != "" {
		h.Chart = "./" + u.ChartFile
	} else {
		h.Chart = "./" + DefaultChartFile
	}
	if u.Tier != nil {
		h.Values = append(h.Values, Set("access.tier", strconv.Itoa(*u.Tier)))
	}
	return h
}

// Render is the command, or the reason there is none to write.
func (u AgentUpgrade) Render() (string, error) {
	return u.helm().Render()
}
