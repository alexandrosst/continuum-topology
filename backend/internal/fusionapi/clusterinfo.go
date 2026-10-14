package fusionapi

import (
	"context"
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// The cluster info series: what lets a dashboard show the name of a cluster where the metrics carry only its id. The link
// series (internal/link) are keyed by cluster ids, which do not change, and carry no names; for each cluster Ikhnos knows
// the server writes
//
//	ikhnos_cluster_info{cluster, cluster_name} 1
//
// into FUSION's Prometheus, so a query joins on the id:
//
//	continuum_link_probe_loss_ratio * on (cluster) group_left (cluster_name) ikhnos_cluster_info
//
// It is written again every minute with the application series (see AppInfoMetric), so a renamed cluster shows its new name
// within a minute and one that is gone falls out within five.

// ClusterInfoMetric is the name of the cluster info series.
const ClusterInfoMetric = "ikhnos_cluster_info"

// maxClusterInfoSeries bounds what one push carries.
const maxClusterInfoSeries = 1000

// ClusterName is a cluster's id and the name people know it by.
type ClusterName struct{ ID, Name string }

// ClusterInfoSeriesOf keeps the clusters that have both an id and a name, sorted by id, without repeats, at most
// maxClusterInfoSeries.
func ClusterInfoSeriesOf(in []ClusterName) []ClusterName {
	seen := map[string]bool{}
	var out []ClusterName
	for _, c := range in {
		if c.ID == "" || c.Name == "" || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > maxClusterInfoSeries {
		out = out[:maxClusterInfoSeries]
	}
	return out
}

// EncodeClusterInfo writes the series as an OTLP/HTTP metrics request body, a gauge of value 1 stamped at (see EncodeAppInfo).
func EncodeClusterInfo(cs []ClusterName, at time.Time) []byte {
	nano := uint64(at.UnixNano())
	var points []byte
	for _, c := range cs {
		var dp []byte
		for _, kv := range [][2]string{{"cluster", c.ID}, {"cluster_name", c.Name}} {
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
	return gaugeRequest(ClusterInfoMetric, "The name of each cluster Ikhnos knows, by the cluster id the link series are keyed by.", points)
}

// PushClusterInfo writes the cluster info series and reports how many series it sent. With no named cluster it sends nothing:
// what was said before ages out.
func (c *Client) PushClusterInfo(ctx context.Context, cs []ClusterName) (int, error) {
	series := ClusterInfoSeriesOf(cs)
	if len(series) == 0 {
		return 0, nil
	}
	if err := c.pushOTLP(ctx, EncodeClusterInfo(series, c.now()), "the cluster series"); err != nil {
		return 0, err
	}
	return len(series), nil
}
