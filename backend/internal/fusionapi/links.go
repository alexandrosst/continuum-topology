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
// has one ("forward" is from the cluster to the peer, "reverse" the other way).
type LinkReading struct {
	Cluster, Peer, Direction string
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
}

// How far back each figure is looked for. The probe runs every minute or so; the route trace every ten minutes; throughput
// tests as rarely as once in a week.
const (
	linkProbeLookback = "10m"
	linkMTULookback   = "1h"
	linkTestLookback  = "8d"
	linkRTTWindow     = "5m"
)

// LinkReadings asks the metrics store for the latest link figures of every cluster that reports them. Only a Scope that may
// run PromQL is allowed (see RawMetricQuery).
func (c *Client) LinkReadings(ctx context.Context, s Scope) (LinkReadings, error) {
	var out LinkReadings
	type job struct {
		query string
		into  *[]LinkReading
	}
	jobs := []job{
		{`last_over_time(continuum_link_probe_loss_ratio[` + linkProbeLookback + `])`, &out.Loss},
		{`last_over_time(continuum_link_probe_delay_variation_seconds{stat="median"}[` + linkProbeLookback + `])`, &out.Jitter},
		{`sum by (cluster, peer) (rate(continuum_link_tcp_connect_seconds_sum[` + linkRTTWindow + `])) / sum by (cluster, peer) (rate(continuum_link_tcp_connect_seconds_count[` + linkRTTWindow + `]))`, &out.RTT},
		{`last_over_time(continuum_link_path_mtu_bytes[` + linkMTULookback + `])`, &out.MTU},
		{`max by (cluster, peer, direction) (last_over_time(continuum_link_throughput_bits_per_second{protocol="tcp",dscp="0"}[` + linkTestLookback + `]))`, &out.Throughput},
		{`max by (cluster, peer, direction) (last_over_time(continuum_link_probe_last_success_timestamp_seconds[` + linkMTULookback + `]))`, &out.Probed},
		{`max by (cluster, peer) (last_over_time(continuum_link_throughput_last_success_timestamp_seconds[` + linkTestLookback + `]))`, &out.Tested},
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
		*into = append(*into, LinkReading{Cluster: r.Metric["cluster"], Peer: r.Metric["peer"], Direction: r.Metric["direction"], Value: v})
	}
	return nil
}
