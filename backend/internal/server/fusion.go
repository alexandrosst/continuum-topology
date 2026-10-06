package server

import (
	"fmt"
	"strings"

	"continuum/internal/store"
)

// FUSION is the continuum-fusion chart: Prometheus (metrics), Loki (logs) and Tempo (traces), each its own Service,
// bundled with the server and switched on and off by it (fusion_control.go). Nothing here is stored per operator: the
// Service names, ports and OTLP paths follow from the chart's release name by the same convention the chart's own
// helper uses (fusionName below mirrors its "fusion.name" define, fusion_test.go pins the two together, and the
// server chart's Role grants exactly the objects that convention names - see internal/chart/server_fusion_test.go).

// maxFusionRelease is the longest release name the chart accepts: its stores' Service names add up to 11
// characters ("-prometheus") to the 50 the chart keeps of it, which must stay a 63-character DNS label.
const maxFusionRelease = 50

// fusionName is the prefix the chart gives each of its objects: the release name, with "-fusion" added unless it
// already contains "fusion", cut to 50 characters. Exactly the chart's "fusion.name" helper.
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

// fusionRoute is where one signal type goes inside a FUSION install: the store's in-cluster address (no scheme: the
// stores take plain HTTP/gRPC, they are ClusterIP-only), and the OTLP flavour it speaks there.
type fusionRoute struct {
	Modality store.Modality
	Endpoint string
	Protocol string
}

// fusionRoutes are the three per-signal routes into the stores, in the order metrics, logs, traces: Prometheus'
// native OTLP receiver (the collector appends /v1/metrics to the path), Loki's native OTLP endpoint (/v1/logs
// appended), and Tempo's OTLP gRPC port. The central gateway's own config (the chart's central-config.yaml) sends to
// exactly these.
func fusionRoutes(d store.Destination) []fusionRoute {
	n, ns := fusionName(d.FusionRelease), d.FusionNamespace
	return []fusionRoute{
		{store.ModalityMetrics, fmt.Sprintf("%s-prometheus.%s.svc:9090/api/v1/otlp", n, ns), "http"},
		{store.ModalityLogs, fmt.Sprintf("%s-loki.%s.svc:3100/otlp", n, ns), "http"},
		{store.ModalityTraces, fmt.Sprintf("%s-tempo.%s.svc:4317", n, ns), "grpc"},
	}
}
