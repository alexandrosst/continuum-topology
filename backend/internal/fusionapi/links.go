package fusionapi

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"sync"
)

// The latest figures of the cluster-link series (internal/link), read for the Sites screen. They are fixed queries, not the
// caller's PromQL, so the screen needs no more access than to see its own organisation's pairs.

// LinkReading is one number of one series: what cluster measured it towards which peer, and in which direction when the series
// has one ("forward" is from the cluster to the peer, "reverse" the other way). Reason is set only on the failure readings
// (LinkReadings.ProbeFailure and ConnectFailure), where it is the failure reason label.
type LinkReading struct {
	Cluster, Peer, Direction string
	Reason                   string
	Value                    float64
}

// LinkReadings are the latest figures, by what they measure. Each list holds every series the store has; the caller keeps the
// ones it wants.
type LinkReadings struct {
	// Loss is the fraction of test packets lost in the latest one-way probe run, per direction.
	Loss []LinkReading
	// Jitter is the median delay variation of that run, in seconds, per direction.
	Jitter []LinkReading
	// RTT is the mean time to open a connection to the peer over the last minutes, in seconds.
	RTT []LinkReading
	// MTU is the path MTU the routers reported, in bytes.
	MTU []LinkReading
	// Throughput is the best TCP throughput of the latest tests (any stream count, unmarked packets), in bits per second, per
	// direction.
	Throughput []LinkReading
	// Probed is when a probe run last finished (unix seconds), per direction; Tested when a throughput test last did.
	Probed, Tested []LinkReading
	// Up is 1 where the latest probe run of a direction completed and 0 where it failed; a direction that was never tried has no
	// reading. Loss and Jitter hold only the directions that are up, so a direction that broke does not keep its last good run.
	Up []LinkReading
	// ProbeFailure is, per direction that failed lately, the reason that failed most often and how often (Value) in the last
	// linkFailureWindow. ConnectFailure is the same for the connect timing of a peer (no direction).
	ProbeFailure, ConnectFailure []LinkReading
}

// NeedLinks says whether the Scope may read the link figures at all. They are metrics, so a token without that signal may not.
// They are also measured between clusters and belong to no namespace, and a Scope that limits namespaces sees no telemetry that
// carries none (as for every other metric read), so it may not read them either, rather than be told that there are none.
func (s Scope) NeedLinks() error {
	if err := s.needSignal(SignalMetrics); err != nil {
		return err
	}
	if len(s.Namespaces) > 0 {
		return denied("link figures are measured between clusters and belong to no namespace, so a token limited to namespaces cannot read them; use a token that limits clusters (or nothing) instead")
	}
	return nil
}

// LinkPairVisible is whether the Scope may see the link between two clusters: only when it may see both of them, since the
// figures are as much about one as about the other.
func (s Scope) LinkPairVisible(a, b string) bool { return s.ClusterVisible(a) && s.ClusterVisible(b) }

// How far back each figure is looked for. The probe runs every minute or so; the route trace every ten minutes; throughput
// tests as rarely as once in a week.
const (
	linkProbeLookback = "10m"
	linkMTULookback   = "1h"
	linkTestLookback  = "8d"
	linkRTTWindow     = "5m"
	// A probe that last finished a day ago is still told apart from one that never did.
	linkProbedLookback = "1d"
	// How far back the failure reasons are counted: many runs, so the commonest reason is the current one.
	linkFailureWindow = "15m"
)

// LinkReadings asks the metrics store for the latest link figures of every cluster that reports them. Only a Scope that may
// run PromQL is allowed (see RawMetricQuery).
func (c *Client) LinkReadings(ctx context.Context, s Scope) (LinkReadings, error) {
	var out LinkReadings
	type job struct {
		query string
		into  *[]LinkReading
	}
	// The series of one pair can exist twice for a while (a rolled pod's last samples beside its successor's), and the order of
	// a store's answer is not defined, so every query aggregates by the labels the caller reads: the worse loss and the smaller
	// MTU win, which is what the dashboard shows too.
	up := `(ikhnos_link_probe_up == 1)`
	jobs := []job{
		{`max by (cluster, peer, direction) (last_over_time(ikhnos_link_probe_loss_ratio[` + linkProbeLookback + `]) and on (cluster, peer, direction) ` + up + `)`, &out.Loss},
		{`max by (cluster, peer, direction) (last_over_time(ikhnos_link_probe_delay_variation_seconds{stat="median"}[` + linkProbeLookback + `]) and on (cluster, peer, direction) ` + up + `)`, &out.Jitter},
		{`sum by (cluster, peer) (rate(ikhnos_link_tcp_connect_seconds_sum[` + linkRTTWindow + `])) / sum by (cluster, peer) (rate(ikhnos_link_tcp_connect_seconds_count[` + linkRTTWindow + `]))`, &out.RTT},
		{`min by (cluster, peer) (last_over_time(ikhnos_link_path_mtu_bytes[` + linkMTULookback + `]))`, &out.MTU},
		{`max by (cluster, peer, direction) (last_over_time(ikhnos_link_throughput_bits_per_second{protocol="tcp",dscp="0"}[` + linkTestLookback + `]))`, &out.Throughput},
		{`max by (cluster, peer, direction) (last_over_time(ikhnos_link_probe_last_success_timestamp_seconds[` + linkProbedLookback + `]))`, &out.Probed},
		{`max by (cluster, peer) (last_over_time(ikhnos_link_throughput_last_success_timestamp_seconds[` + linkTestLookback + `]))`, &out.Tested},
		{`max by (cluster, peer, direction) (ikhnos_link_probe_up)`, &out.Up},
		{`topk by (cluster, peer, direction) (1, sum by (cluster, peer, direction, reason) (increase(ikhnos_link_probe_failures_total[` + linkFailureWindow + `])) > 0)`, &out.ProbeFailure},
		{`topk by (cluster, peer) (1, sum by (cluster, peer, reason) (increase(ikhnos_link_tcp_connect_failures_total[` + linkFailureWindow + `])) > 0)`, &out.ConnectFailure},
	}
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.linkQuery(ctx, s, j.query, j.into)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return LinkReadings{}, err
		}
	}
	return out, nil
}

func (c *Client) linkQuery(ctx context.Context, s Scope, q string, into *[]LinkReading) error {
	data, err := c.RawMetricQuery(ctx, s, "query", url.Values{"query": {q}})
	if err != nil {
		return err
	}
	var d struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	for _, r := range d.Result {
		str, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(str, 64)
		// A rate of a series with no new samples is NaN; a series that holds nothing is not a reading.
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || r.Metric["cluster"] == "" || r.Metric["peer"] == "" {
			continue
		}
		*into = append(*into, LinkReading{Cluster: r.Metric["cluster"], Peer: r.Metric["peer"], Direction: r.Metric["direction"], Reason: r.Metric["reason"], Value: v})
	}
	return nil
}
