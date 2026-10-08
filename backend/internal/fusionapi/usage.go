package fusionapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// What the stores say about their own disk use, for the retention control: how much room the data takes and how fast it
// grows, so that a longer retention can be checked against the volume before it is chosen.

// PromStorage is Prometheus' own account of its time series database.
type PromStorage struct {
	// Bytes is the blocks, the write-ahead log and the memory-mapped head chunks: everything its size limit counts.
	Bytes int64
	// Oldest is the timestamp of the oldest sample held; zero while there is none.
	Oldest time.Time
}

var promStorageMetrics = map[string]bool{
	"prometheus_tsdb_storage_blocks_bytes":           true,
	"prometheus_tsdb_wal_storage_size_bytes":         true,
	"prometheus_tsdb_head_chunks_storage_size_bytes": true,
}

// PrometheusStorage reads Prometheus' /metrics page for the size of its database and the age of its oldest sample.
func (c *Client) PrometheusStorage(ctx context.Context) (PromStorage, error) {
	var raw json.RawMessage
	if err := c.get(ctx, storeProm, c.Prometheus, "/metrics", nil, map[string]string{"Accept": "text/plain"}, &raw); err != nil {
		return PromStorage{}, err
	}
	return parsePromStorage(raw), nil
}

func parsePromStorage(text []byte) PromStorage {
	var out PromStorage
	var lowestMs float64
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name := strings.TrimSuffix(f[0], "{}")
		if !promStorageMetrics[name] && name != "prometheus_tsdb_lowest_timestamp" {
			continue
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil || v < 0 {
			continue
		}
		if name == "prometheus_tsdb_lowest_timestamp" {
			lowestMs = v
			continue
		}
		out.Bytes += int64(v)
	}
	// An empty database reports the largest int64 here; anything not after 2001 is not a sample time.
	if lowestMs > 1e12 && lowestMs < 4e12 {
		out.Oldest = time.UnixMilli(int64(lowestMs)).UTC()
	}
	return out
}

// VolumeUse is how full one volume is, as the kubelet reports it.
type VolumeUse struct {
	Namespace string // "" when the series does not say
	Used      int64
}

var claimNameRe = regexp.MustCompile(`^[a-z0-9.-]+$`)

// VolumeUsage asks FUSION's own Prometheus how full the named volume claims are (the kubelet's volume statistics, which
// the agent's host collector sends when it runs). The answer is keyed by claim name; a claim with no series is absent.
// It is only a convenience: the caller treats any error, and any absence, as "not measured".
func (c *Client) VolumeUsage(ctx context.Context, s Scope, claims []string) (map[string]VolumeUse, error) {
	var quoted []string
	for _, n := range claims {
		if !claimNameRe.MatchString(n) {
			continue
		}
		quoted = append(quoted, regexp.QuoteMeta(n))
	}
	if len(quoted) == 0 {
		return nil, nil
	}
	sel := `{k8s_persistentvolumeclaim_name=~` + quote(strings.Join(quoted, "|")) + `}`
	q := `max by (k8s_persistentvolumeclaim_name, k8s_namespace_name) (k8s_volume_capacity_bytes` + sel + ` - k8s_volume_available_bytes` + sel + `)`
	data, err := c.RawMetricQuery(ctx, s, "query", url.Values{"query": {q}})
	if err != nil {
		return nil, err
	}
	var d struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	out := map[string]VolumeUse{}
	for _, r := range d.Result {
		name := r.Metric["k8s_persistentvolumeclaim_name"]
		str, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(str, 64)
		if name == "" || err != nil || v < 0 {
			continue
		}
		if cur, ok := out[name]; !ok || int64(v) > cur.Used {
			out[name] = VolumeUse{Namespace: r.Metric["k8s_namespace_name"], Used: int64(v)}
		}
	}
	return out, nil
}
