package server

import (
	"fmt"
	"regexp"
	"strings"

	"continuum/internal/chart"
	"continuum/internal/store"
)

// FUSION is the continuum-fusion chart: Prometheus (metrics), Loki (logs) and Tempo (traces), each its own
// Service. A regional operator whose destination is of kind fusion saves what it receives there. Nothing about
// the three stores is stored on the Operator beyond the release name and namespace of that install: the
// Service names, ports and OTLP paths follow from them by the same convention the chart's own helper uses
// (fusionName below mirrors its "fusion.name" define, and fusion_test.go pins the two together).

const (
	// defaultFusionRelease and defaultFusionNamespace are what a fusion destination gets when it names neither:
	// the same names the wizard offers and the docs use, so the common case needs no typing.
	defaultFusionRelease   = "fusion"
	defaultFusionNamespace = "continuum-system"

	// maxFusionRelease is the longest release name the chart accepts: its stores' Service names add up to 11
	// characters ("-prometheus") to the 50 the chart keeps of it, which must stay a 63-character DNS label.
	maxFusionRelease = 50
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// fusionName is the prefix the chart gives each of its Services: the release name, with "-fusion" added unless
// it already contains "fusion", cut to 50 characters. Exactly the chart's "fusion.name" helper.
func fusionName(release string) string {
	n := release
	if !strings.Contains(release, "fusion") {
		n = release + "-fusion"
	}
	if len(n) > maxFusionRelease {
		n = n[:maxFusionRelease]
	}
	return strings.TrimSuffix(n, "-")
}

// normalizeFusion fills a fusion destination's defaults and drops every field that belongs to another kind,
// so what is stored (and later read back) is only what a FUSION destination is.
func normalizeFusion(d store.Destination) store.Destination {
	rel := strings.TrimSpace(d.FusionRelease)
	if rel == "" {
		rel = defaultFusionRelease
	}
	ns := strings.TrimSpace(d.FusionNamespace)
	if ns == "" {
		ns = defaultFusionNamespace
	}
	return store.Destination{Kind: store.DestinationFusion, FusionRelease: rel, FusionNamespace: ns}
}

// validateFusion checks a normalized fusion destination names a release and a namespace Kubernetes (and Helm)
// will accept.
func validateFusion(d store.Destination) error {
	if !dnsLabel.MatchString(d.FusionRelease) || len(d.FusionRelease) > maxFusionRelease {
		return errf(KindInvalid, "destination.fusionRelease must be a lowercase DNS label of at most %d characters (letters, digits and '-')", maxFusionRelease)
	}
	if !dnsLabel.MatchString(d.FusionNamespace) || len(d.FusionNamespace) > 63 {
		return errf(KindInvalid, "destination.fusionNamespace must be a lowercase DNS label of at most 63 characters (letters, digits and '-')")
	}
	return nil
}

// fusionRoute is where one signal type goes inside a FUSION install: the store's in-cluster address (no
// scheme: tls.insecure picks plain HTTP), the OTLP flavour it speaks there, and no TLS - the stores are
// ClusterIP-only and do not authenticate, see the chart's own values.yaml.
type fusionRoute struct {
	Modality store.Modality
	Endpoint string
	Protocol string
}

// fusionRoutes are the three per-signal routes of a FUSION destination, in the order metrics, logs, traces:
// Prometheus' native OTLP receiver (the collector appends /v1/metrics to the path), Loki's native OTLP
// endpoint (/v1/logs appended), and Tempo's OTLP gRPC port.
func fusionRoutes(d store.Destination) []fusionRoute {
	n, ns := fusionName(d.FusionRelease), d.FusionNamespace
	return []fusionRoute{
		{store.ModalityMetrics, fmt.Sprintf("%s-prometheus.%s.svc:9090/api/v1/otlp", n, ns), "http"},
		{store.ModalityLogs, fmt.Sprintf("%s-loki.%s.svc:3100/otlp", n, ns), "http"},
		{store.ModalityTraces, fmt.Sprintf("%s-tempo.%s.svc:4317", n, ns), "grpc"},
	}
}

// fusionRouteFlags are the regional-operator chart's own --set flags (export.routes.<signal>.*) that send each
// signal type to its store. Every signal has a route, so the chart needs no default export.otlp.endpoint.
// Joined by sep, so the install command can put each on its own line.
func fusionRouteFlags(d store.Destination, sep string) string {
	var parts []string
	for _, r := range fusionRoutes(d) {
		base := "export.routes." + string(r.Modality)
		parts = append(parts,
			fmt.Sprintf("--set %s.endpoint=%s", base, r.Endpoint),
			fmt.Sprintf("--set %s.protocol=%s", base, r.Protocol),
			fmt.Sprintf("--set %s.tls.insecure=true", base))
	}
	return strings.Join(parts, sep)
}

// fusionChartRef is the chart the FUSION install command names, or "" for the file this server serves. Same
// rules as the operator chart's: an explicit --chart-ref wins, else a configured image registry also holds the
// chart, as an OCI artifact under its own name.
func (a *Admin) fusionChartRef(img ImageConfig) string {
	switch {
	case a.ChartRef == "local":
		return ""
	case a.ChartRef != "":
		return a.ChartRef
	case img.Configured():
		return "oci://" + OCIBase(img.Registry) + "/continuum-fusion"
	}
	return ""
}

// fusionChartVersion is what a --version flag against the registry chart says: the release pipeline packages
// every chart of a run under one version and links it in as OperatorChartVersion, so it is that when known.
func (a *Admin) fusionChartVersion() string {
	if a.OperatorChartVersion != "" {
		return a.OperatorChartVersion
	}
	return chart.Fusion.Version()
}

// fusionInstallCommand is the `helm upgrade --install` that puts the FUSION stores where a fusion destination
// says they are. An upgrade --install, so running it again is harmless.
func (a *Admin) fusionInstallCommand(img ImageConfig, d store.Destination) string {
	ref, version := a.fusionChartRef(img), ""
	if ref == "" {
		ref = "./" + chart.Fusion.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + a.fusionChartVersion()
	}
	return fmt.Sprintf("helm upgrade --install %s %s%s \\\n  --namespace %s --create-namespace", d.FusionRelease, ref, version, d.FusionNamespace)
}
