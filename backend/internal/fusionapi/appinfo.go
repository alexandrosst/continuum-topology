package fusionapi

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"sort"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// The application info series: what lets the tools that read Prometheus directly (Grafana, PromQL written by hand) filter by
// Ikhnos application, which telemetry itself does not carry. For each service of each application the server writes
//
//	ikhnos_application_info{application, application_id, service_name, k8s_namespace_name, continuum_cluster_id} 1
//
// into FUSION's Prometheus, the same labels the telemetry's own series have for the service, so a query joins on them:
//
//	up * on(service_name, k8s_namespace_name) group_left(application) ikhnos_application_info{application="Shop"}
//
// It is written again every minute from what Ikhnos knows at that moment (an application edited in the UI changes the
// series within a minute, and one deleted stops being written and falls out of Prometheus' five-minute lookback), so a
// query for "now" sees the present membership. The samples themselves are kept for the retention period like any other,
// so a query over a past range sees the membership of that time, as far back as the series was written.

// AppInfoMetric is the name of the info series.
const AppInfoMetric = "ikhnos_application_info"

// maxAppInfoSeries bounds what one push carries.
const maxAppInfoSeries = 5000

// The labels of the info series, named as Prometheus names the telemetry's own.
const (
	infoApplication   = "application"
	infoApplicationID = "application_id"
)

// AppInfoSeries is one series of the info metric: the labels of one service of one application.
type AppInfoSeries struct {
	Application, ApplicationID, Service, Namespace, Cluster string
}

// AppInfoSeriesOf lists the series for the groups: one per service name the telemetry of a member may carry (its name and
// its aliases) in each namespace and cluster it runs in, sorted, without repeats and at most maxAppInfoSeries.
func AppInfoSeriesOf(groups []AppGroup) []AppInfoSeries {
	seen := map[AppInfoSeries]bool{}
	var out []AppInfoSeries
	for _, g := range groups {
		for _, m := range g.Members {
			for _, n := range append([]string{m.Name}, m.Aliases...) {
				if n == "" {
					continue
				}
				s := AppInfoSeries{Application: g.Name, ApplicationID: g.ID, Service: n, Namespace: m.Namespace, Cluster: m.Cluster}
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ApplicationID != b.ApplicationID {
			return a.ApplicationID < b.ApplicationID
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Cluster < b.Cluster
	})
	if len(out) > maxAppInfoSeries {
		out = out[:maxAppInfoSeries]
	}
	return out
}

// EncodeAppInfo writes the series as an OTLP/HTTP metrics request body (protobuf), a gauge of value 1 stamped at. It is
// written with the wire format directly: three nested messages and a handful of fields are not worth a dependency on the
// OpenTelemetry protocol definitions. Prometheus' OTLP receiver turns a data point's attributes into the series' labels
// (dots become underscores); there is no resource, so no job or instance label is added.
func EncodeAppInfo(series []AppInfoSeries, at time.Time) []byte {
	nano := uint64(at.UnixNano())
	var points []byte
	for _, s := range series {
		var dp []byte
		for _, kv := range [][2]string{{infoApplication, s.Application}, {infoApplicationID, s.ApplicationID}, {lblService, s.Service},
			{lblNamespace, s.Namespace}, {lblCluster, s.Cluster}} {
			if kv[1] == "" {
				continue
			}
			dp = protowire.AppendTag(dp, 7, protowire.BytesType) // attributes
			dp = protowire.AppendBytes(dp, keyValue(kv[0], kv[1]))
		}
		dp = protowire.AppendTag(dp, 3, protowire.Fixed64Type) // time_unix_nano
		dp = protowire.AppendFixed64(dp, nano)
		dp = protowire.AppendTag(dp, 4, protowire.Fixed64Type) // as_double
		dp = protowire.AppendFixed64(dp, math.Float64bits(1))
		points = protowire.AppendTag(points, 1, protowire.BytesType) // Gauge.data_points
		points = protowire.AppendBytes(points, dp)
	}
	var metric []byte
	metric = protowire.AppendTag(metric, 1, protowire.BytesType) // name
	metric = protowire.AppendString(metric, AppInfoMetric)
	metric = protowire.AppendTag(metric, 2, protowire.BytesType) // description
	metric = protowire.AppendString(metric, "The services of each Ikhnos application, with the labels their telemetry carries.")
	metric = protowire.AppendTag(metric, 5, protowire.BytesType) // gauge
	metric = protowire.AppendBytes(metric, points)

	var scope []byte
	scope = protowire.AppendTag(scope, 2, protowire.BytesType) // ScopeMetrics.metrics
	scope = protowire.AppendBytes(scope, metric)
	var rm []byte
	rm = protowire.AppendTag(rm, 2, protowire.BytesType) // ResourceMetrics.scope_metrics
	rm = protowire.AppendBytes(rm, scope)
	var req []byte
	req = protowire.AppendTag(req, 1, protowire.BytesType) // resource_metrics
	return protowire.AppendBytes(req, rm)
}

func keyValue(k, v string) []byte {
	var any []byte
	any = protowire.AppendTag(any, 1, protowire.BytesType) // string_value
	any = protowire.AppendString(any, v)
	var kv []byte
	kv = protowire.AppendTag(kv, 1, protowire.BytesType)
	kv = protowire.AppendString(kv, k)
	kv = protowire.AppendTag(kv, 2, protowire.BytesType)
	return protowire.AppendBytes(kv, any)
}

// PushAppInfo writes the info series for the groups into Prometheus' OTLP receiver and reports how many series it sent.
func (c *Client) PushAppInfo(ctx context.Context, groups []AppGroup) (int, error) {
	if c.Prometheus == "" {
		return 0, errf(http.StatusServiceUnavailable, "%s is not configured", storeProm)
	}
	series := AppInfoSeriesOf(groups)
	if len(series) == 0 {
		return 0, nil // nothing to say; what was said before ages out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Prometheus+"/api/v1/otlp/v1/metrics", bytes.NewReader(EncodeAppInfo(series, c.now())))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := c.http().Do(req)
	if err != nil {
		return 0, errf(http.StatusServiceUnavailable, "%s could not be reached: %v", storeProm, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, errf(http.StatusBadGateway, "%s refused the application series (%d): %s", storeProm, resp.StatusCode, upstreamMessage(body))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return len(series), nil
}
