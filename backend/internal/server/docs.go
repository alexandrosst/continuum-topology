package server

import (
	"ikhnos/internal/accounts"
	"ikhnos/internal/agents"
)

// The shapes the shell itself puts on the wire: the server describing itself, and the one page ticket it hands out. Everything else a
// route says is a module's (see wire.go).

// infoDoc answers GET .../info: who is asking and where, and what the server was told about its own install. The fields are in the
// alphabetical order of their JSON names, which is the order the map it replaced was encoded in.
type infoDoc struct {
	AgentAddress    string         `json:"agentAddress"`
	AgentExposure   string         `json:"agentExposure"`
	CAPin           string         `json:"caPin"`
	GeoIP           agents.GeoInfo `json:"geoip"`
	ImplementedTier int            `json:"implementedTier"`
	// Install is what the install wizard tells the operator to bring to the cluster: the chart file this server hands out
	// (empty when the command names a chart elsewhere) and where the image comes from (this organisation's
	// Settings → Installation, else the server's flags; empty registry: the chart's built-in names).
	// ChartVersion is the version a `--version` flag must say - the one the release pipeline published this build's
	// chart under (agentChartVersion), NOT chart.Version(): the browser builds several commands itself from this
	// field, and the bundled Chart.yaml literal can name an old chart whose values schema rejects what this build
	// sends (helm: "Additional property ... is not allowed").
	Install          infoInstallDoc `json:"install"`
	OrgID            string         `json:"orgId"`
	OrgName          string         `json:"orgName"`
	ReleaseName      string         `json:"releaseName"`
	ReleaseNamespace string         `json:"releaseNamespace"`
	Role             accounts.Role  `json:"role"`
	Version          string         `json:"version"`
}

// infoInstallDoc is the part of infoDoc about how the agent is installed.
type infoInstallDoc struct {
	ChartFile        string `json:"chartFile"`
	ChartRef         string `json:"chartRef"`
	ChartVersion     string `json:"chartVersion"`
	ImageDigest      string `json:"imageDigest"`
	ImageRegistry    string `json:"imageRegistry"`
	ImageTag         string `json:"imageTag"`
	ImagesConfigured bool   `json:"imagesConfigured"`
}

// openFusionPageRequest is the body of POST .../fusion/pages: which of FUSION's pages to open ("grafana" or "prometheus").
type openFusionPageRequest struct {
	Page string `json:"page"`
}

// fusionPageDoc answers it: the path to open, with a ticket in it.
type fusionPageDoc struct {
	Path string `json:"path"`
}
