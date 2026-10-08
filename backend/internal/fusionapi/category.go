package fusionapi

import (
	"regexp"
	"slices"
	"strings"
)

// Categories say what kind of thing a piece of telemetry is about, so a reader can ask for the application's own signals
// without the cluster's and the machine's, or the other way round. One rule decides it, here, and every route and the
// Grafana dashboards use it, so "application" means the same thing everywhere.
//
//   - system: the machine and the telemetry pipeline themselves: CPU, memory and disk of a host, energy, scrape health, the
//     collectors' own metrics. Logs with no Kubernetes namespace (a node's own journal, kubelet) are system logs.
//   - kubernetes: the cluster's objects: the k8s_* and container_* metrics (pods, nodes, deployments, containers), and the
//     logs and spans of the cluster's own namespaces (kube-system and the like).
//   - application: everything else: what an application reports about itself (its own metrics, its logs and spans in
//     any other namespace).
//
// A category is what the signal is about, not who owns it: a pod's CPU is kubernetes whichever application the pod is in.
// The application filter answers "whose".
const (
	CategorySystem      = "system"
	CategoryKubernetes  = "kubernetes"
	CategoryApplication = "application"
)

// Categories lists them, in the order a screen shows them.
var Categories = []string{CategorySystem, CategoryKubernetes, CategoryApplication}

// SystemNamespaces are the namespaces whose logs and spans are the cluster's own (category kubernetes).
var SystemNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// The metric-name families of the two categories that are named by prefix. Everything else is an application metric.
// They are Prometheus (RE2) regular expressions over the whole name, which Prometheus anchors itself.
const (
	SystemMetricNames     = `(system_.*|process_.*|node_.*|kepler_.*|dcgm_.*|otelcol_.*|scrape_.*|up|target_info)`
	KubernetesMetricNames = `(k8s_.*|container_.*|kube_.*)`
)

var (
	systemMetricRE     = regexp.MustCompile(`^` + SystemMetricNames + `$`)
	kubernetesMetricRE = regexp.MustCompile(`^` + KubernetesMetricNames + `$`)
)

// MetricCategory is the category of a metric by its name.
func MetricCategory(name string) string {
	switch {
	case systemMetricRE.MatchString(name):
		return CategorySystem
	case kubernetesMetricRE.MatchString(name):
		return CategoryKubernetes
	}
	return CategoryApplication
}

// LogCategory is the category of a log line by the namespace of the pod that wrote it ("" is a host's own log).
func LogCategory(namespace string) string {
	switch {
	case namespace == "":
		return CategorySystem
	case slices.Contains(SystemNamespaces, namespace):
		return CategoryKubernetes
	}
	return CategoryApplication
}

// ParseCategories reads a comma-separated list. An empty one means every category (no filter); asking for all three is the
// same, and is returned as none.
func ParseCategories(v string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if !slices.Contains(Categories, p) {
			return nil, badRequest("category must be system, kubernetes or application (a comma-separated list of them); got %q", printable(p))
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if len(out) == len(Categories) {
		return nil, nil
	}
	return out, nil
}

// printable shortens a value for an error message.
func printable(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// metricNameMatcher is the matcher on __name__ that keeps the wanted categories. The families partition the names, so
// when the application category is wanted the unwanted named families are excluded, and otherwise the wanted ones are listed.
func metricNameMatcher(cats []string) string {
	if len(cats) == 0 {
		return ""
	}
	has := func(c string) bool { return slices.Contains(cats, c) }
	if has(CategoryApplication) {
		var no []string
		if !has(CategorySystem) {
			no = append(no, SystemMetricNames)
		}
		if !has(CategoryKubernetes) {
			no = append(no, KubernetesMetricNames)
		}
		return lblName + "!~" + quote(strings.Join(no, "|"))
	}
	var yes []string
	if has(CategorySystem) {
		yes = append(yes, SystemMetricNames)
	}
	if has(CategoryKubernetes) {
		yes = append(yes, KubernetesMetricNames)
	}
	return lblName + "=~" + quote(strings.Join(yes, "|"))
}

// logCategoryMatchers are the matchers on a Loki namespace label that keep the wanted categories (none when there is no
// filter). A log without a namespace has no such label at all, which Loki reads as the empty string. The three categories
// partition the values: "" (system), the cluster's own namespaces (kubernetes) and the rest (application).
func logCategoryMatchers(label string, cats []string) []string {
	if len(cats) == 0 {
		return nil
	}
	has := func(c string) bool { return slices.Contains(cats, c) }
	sys := regexAny(SystemNamespaces)
	switch {
	case has(CategorySystem) && has(CategoryKubernetes):
		return []string{label + "=~" + quote("|"+sys)}
	case has(CategorySystem) && has(CategoryApplication):
		return []string{label + "!~" + quote(sys)}
	case has(CategoryKubernetes) && has(CategoryApplication):
		return []string{label + `!=""`}
	case has(CategorySystem):
		return []string{label + `=""`}
	case has(CategoryKubernetes):
		return []string{label + "=~" + quote(sys)}
	}
	return []string{label + "!~" + quote(sys), label + `!=""`}
}

// traceCategoryCond is the TraceQL condition that keeps the wanted categories of spans, or "" for none. Spans have no
// system category (a span is work a program did, not a machine's reading), so asking for one is refused; and TraceQL
// cannot match a missing attribute, so a span with no namespace is in neither category.
func traceCategoryCond(attr string, cats []string) (string, error) {
	if slices.Contains(cats, CategorySystem) {
		return "", badRequest("traces have no system category: use kubernetes (the cluster's own namespaces) or application")
	}
	has := func(c string) bool { return slices.Contains(cats, c) }
	switch {
	case len(cats) == 0, has(CategoryKubernetes) && has(CategoryApplication):
		return "", nil
	case has(CategoryKubernetes):
		return anyOf(attr, SystemNamespaces), nil
	}
	p := make([]string, len(SystemNamespaces))
	for i, n := range SystemNamespaces {
		p[i] = attr + " != " + quote(n)
	}
	return "(" + strings.Join(p, " && ") + ")", nil
}
