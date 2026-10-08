package fusionapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Prometheus names a series' labels after the resource attributes the FUSION chart promotes, with dots turned into
// underscores (OTLP's own translation): service.name is service_name, and so on.
const (
	lblName      = "__name__"
	lblService   = "service_name"
	lblNamespace = "k8s_namespace_name"
	lblPod       = "k8s_pod_name"
	lblNode      = "k8s_node_name"
	lblCluster   = "continuum_cluster_id"
)

// The labels that name the workload a pod belongs to. A pod's metrics carry no service name, only these.
var lblWorkloads = []string{"k8s_deployment_name", "k8s_statefulset_name", "k8s_daemonset_name"}

// Point is one sample, [unix seconds, value] in JSON.
type Point [2]float64

// MetricSeries is one series and what it did over the asked range.
type MetricSeries struct {
	Name string `json:"name"`
	// Category is system, kubernetes or application, by the metric's name (see MetricCategory).
	Category string            `json:"category,omitempty"`
	Labels   map[string]string `json:"labels"`
	Points   []Point           `json:"points,omitempty"`
	Min      float64           `json:"min"`
	Max      float64           `json:"max"`
	Avg      float64           `json:"avg"`
	Last     float64           `json:"last"`
}

// MetricFilter picks series. Every field is an exact match except NameRegex, which is a regular expression over the
// metric name; all of them are optional and combine with AND.
type MetricFilter struct {
	Name      string `json:"name,omitempty"`
	NameRegex string `json:"nameRegex,omitempty"`
	Service   string `json:"service,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Node      string `json:"node,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	// Categories keeps metrics of these categories (system, kubernetes, application) by their names; empty keeps all.
	Categories []string `json:"categories,omitempty"`
	// NoPod keeps only series that name no pod: with Node, what was reported about the node itself.
	NoPod bool `json:"noPod,omitempty"`
}

// matchers builds the label matchers of a selector: the filter's own, then the Scope's, each as its own matcher so a
// filter that contradicts the Scope simply matches nothing.
func (f MetricFilter) matchers(s Scope) ([]string, error) {
	var m []string
	switch {
	case f.Name != "":
		if err := checkValue("name", f.Name); err != nil {
			return nil, err
		}
		m = append(m, lblName+"="+quote(f.Name))
	case f.NameRegex != "":
		if err := checkValue("metric", f.NameRegex); err != nil {
			return nil, err
		}
		if _, err := regexp.Compile(f.NameRegex); err != nil {
			return nil, badRequest("metric is not a valid regular expression")
		}
		m = append(m, lblName+"=~"+quote(f.NameRegex))
	default:
		m = append(m, lblName+`=~".+"`)
	}
	if cm := metricNameMatcher(f.Categories); cm != "" {
		m = append(m, cm)
	}
	if f.Name != AppInfoMetric {
		// The series the server writes to say which services are in which application is not telemetry: it would make every
		// service of an application look like it reports a metric of that name.
		m = append(m, lblName+"!="+quote(AppInfoMetric))
	}
	eq, err := eqMatchers([]eqFilter{
		{"service", lblService, f.Service}, {"namespace", lblNamespace, f.Namespace}, {"pod", lblPod, f.Pod},
		{"node", lblNode, f.Node}, {"cluster", lblCluster, f.Cluster},
	}, func(l, v string) string { return l + "=" + v })
	if err != nil {
		return nil, err
	}
	m = append(m, eq...)
	if f.NoPod {
		m = append(m, lblPod+`=""`)
	}
	if ns := s.nsLimit(); len(ns) > 0 {
		m = append(m, lblNamespace+"=~"+quote(regexAny(ns)))
	}
	if cl := s.clLimit(); len(cl) > 0 {
		m = append(m, lblCluster+"=~"+quote(regexAny(cl)))
	}
	if len(s.FocusServices) > 0 {
		m = append(m, lblService+"=~"+quote(regexAny(s.FocusServices)))
	}
	return m, nil
}

// selectors is what a read sends: one selector, or, when the caller chose services (an application) and did not name one
// itself, that selector plus one per workload kind. Pod metrics (k8s_pod_cpu_usage, restarts, ...) carry the name of
// their Deployment, StatefulSet or DaemonSet and no service name, so without the extra selectors choosing an application
// would leave them out. The selectors are disjoint in what they add, so the results are simply united.
func (f MetricFilter) selectors(s Scope) ([]string, error) {
	base, err := f.selector(s)
	if err != nil {
		return nil, err
	}
	if len(s.FocusServices) == 0 || f.Service != "" {
		return []string{base}, nil
	}
	wide := s
	wide.FocusServices = nil
	m, err := f.matchers(wide)
	if err != nil {
		return nil, err
	}
	out := []string{base}
	for _, l := range lblWorkloads {
		out = append(out, "{"+strings.Join(append(slices.Clone(m), l+"=~"+quote(regexAny(s.FocusServices))), ",")+"}")
	}
	return out, nil
}

func (f MetricFilter) selector(s Scope) (string, error) {
	m, err := f.matchers(s)
	if err != nil {
		return "", err
	}
	return "{" + strings.Join(m, ",") + "}", nil
}

// promEnvelope is Prometheus' answer shape.
type promEnvelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

func (c *Client) prom(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	var env promEnvelope
	if err := c.get(ctx, storeProm, c.Prometheus, path, q, nil, &env); err != nil {
		return nil, err
	}
	if env.Status != "success" {
		return nil, errf(http.StatusBadGateway, "%s: %s", storeProm, env.Error)
	}
	return env.Data, nil
}

// HeadMaxTime is the timestamp of the newest sample Prometheus holds, from its TSDB status (GET /api/v1/status/tsdb:
// headStats.maxTime, in milliseconds). ok is false when nothing has been stored yet (an empty head reports the minimum
// int64). It is the cheapest honest "has anything arrived": one number Prometheus already keeps, so it does not depend on which
// series a sender produces and costs the same however much is stored. Only a Scope with no namespace or cluster limit may ask,
// since it says something about every sender.
func (c *Client) HeadMaxTime(ctx context.Context, s Scope) (t time.Time, ok bool, err error) {
	if err := s.needSignal(SignalMetrics); err != nil {
		return time.Time{}, false, err
	}
	if err := s.needUnrestricted("the newest stored sample"); err != nil {
		return time.Time{}, false, err
	}
	data, err := c.prom(ctx, "/api/v1/status/tsdb", nil)
	if err != nil {
		return time.Time{}, false, err
	}
	var d struct {
		HeadStats struct {
			NumSeries int64 `json:"numSeries"`
			MaxTime   int64 `json:"maxTime"`
		} `json:"headStats"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return time.Time{}, false, errf(http.StatusBadGateway, "%s: unreadable TSDB status", storeProm)
	}
	// An empty head has minTime = MaxInt64 and maxTime = MinInt64; anything not after 2001 is not a sample time.
	if d.HeadStats.NumSeries <= 0 || d.HeadStats.MaxTime < 1e12 {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(d.HeadStats.MaxTime).UTC(), true, nil
}

// MetricNames lists the metric names that have a series matching the filter in the range.
func (c *Client) MetricNames(ctx context.Context, s Scope, f MetricFilter, tr TimeRange, limit int) ([]string, error) {
	if err := s.needSignal(SignalMetrics); err != nil {
		return nil, err
	}
	sel, err := f.selectors(s)
	if err != nil {
		return nil, err
	}
	data, err := c.prom(ctx, "/api/v1/label/__name__/values", url.Values{"match[]": sel, "start": {unixFloat(tr.From)}, "end": {unixFloat(tr.To)}, "limit": {strconv.Itoa(limit)}})
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, errf(http.StatusBadGateway, "%s answered with something unexpected", storeProm)
	}
	sort.Strings(names)
	if len(names) > limit {
		names = names[:limit]
	}
	return names, nil
}

// Series lists the label sets of the series matching the filter in the range.
func (c *Client) Series(ctx context.Context, s Scope, f MetricFilter, tr TimeRange, limit int) ([]map[string]string, error) {
	if err := s.needSignal(SignalMetrics); err != nil {
		return nil, err
	}
	sel, err := f.selectors(s)
	if err != nil {
		return nil, err
	}
	data, err := c.prom(ctx, "/api/v1/series", url.Values{"match[]": sel, "start": {unixFloat(tr.From)}, "end": {unixFloat(tr.To)}, "limit": {strconv.Itoa(limit)}})
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errf(http.StatusBadGateway, "%s answered with something unexpected", storeProm)
	}
	out = visibleSeries(s, out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// visibleSeries drops label sets the Scope may not see. The query already carried the Scope's matchers; this is the
// second check, on what actually came back.
func visibleSeries(s Scope, in []map[string]string) []map[string]string {
	if s.Unrestricted() {
		return in
	}
	out := in[:0]
	for _, l := range in {
		if s.NamespaceVisible(l[lblNamespace]) && s.ClusterVisible(l[lblCluster]) {
			out = append(out, l)
		}
	}
	return out
}

// Step limits.
const (
	minStep   = 15 * time.Second
	maxPoints = 5000
)

// ChooseStep picks the resolution of a range query: the requested step if it gives a sane number of points,
// otherwise about target points across the range, never finer than minStep.
func ChooseStep(tr TimeRange, requested time.Duration, target int) (time.Duration, error) {
	span := tr.To.Sub(tr.From)
	if requested > 0 {
		if requested < time.Second {
			return 0, badRequest("step must be at least one second")
		}
		if int(span/requested) > maxPoints {
			return 0, badRequest("step gives more than %d points over this range; use a larger step or a shorter range", maxPoints)
		}
		return requested, nil
	}
	if target <= 0 {
		target = 120
	}
	step := span / time.Duration(target)
	if step < minStep {
		step = minStep
	}
	return step.Round(time.Second), nil
}

// MetricRange returns the values of every series matching the filter over the range, at most maxSeries of them (the
// answer says when it left some out).
func (c *Client) MetricRange(ctx context.Context, s Scope, f MetricFilter, tr TimeRange, step time.Duration, maxSeries int) (series []MetricSeries, truncated bool, err error) {
	if err := s.needSignal(SignalMetrics); err != nil {
		return nil, false, err
	}
	sel, err := f.selectors(s)
	if err != nil {
		return nil, false, err
	}
	data, err := c.prom(ctx, "/api/v1/query_range", url.Values{
		"query": {strings.Join(sel, " or ")}, "start": {unixFloat(tr.From)}, "end": {unixFloat(tr.To)}, "step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)}, "limit": {strconv.Itoa(maxSeries + 1)},
	})
	if err != nil {
		return nil, false, err
	}
	return decodeMatrix(data, s, maxSeries)
}

// decodeMatrix reads the "data" of a Prometheus range query into series, leaving out those the Scope may not see and
// keeping at most maxSeries of them (sorted by name and labels, so the cut is stable).
func decodeMatrix(data json.RawMessage, s Scope, maxSeries int) (series []MetricSeries, truncated bool, err error) {
	var res struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &res); err != nil || res.ResultType != "matrix" {
		return nil, false, errf(http.StatusBadGateway, "%s answered with something unexpected", storeProm)
	}
	for _, r := range res.Result {
		if !s.NamespaceVisible(r.Metric[lblNamespace]) || !s.ClusterVisible(r.Metric[lblCluster]) {
			continue
		}
		ms := MetricSeries{Name: r.Metric[lblName], Category: MetricCategory(r.Metric[lblName]), Labels: map[string]string{}}
		for k, v := range r.Metric {
			if k != lblName {
				ms.Labels[k] = v
			}
		}
		for _, v := range r.Values {
			t, ok1 := v[0].(float64)
			str, ok2 := v[1].(string)
			if !ok1 || !ok2 {
				continue
			}
			val, perr := strconv.ParseFloat(str, 64)
			if perr != nil || math.IsNaN(val) || math.IsInf(val, 0) {
				continue // JSON has no NaN or Inf
			}
			ms.Points = append(ms.Points, Point{t, val})
		}
		ms.summarise()
		series = append(series, ms)
	}
	sort.Slice(series, func(i, j int) bool {
		if series[i].Name != series[j].Name {
			return series[i].Name < series[j].Name
		}
		return labelKey(series[i].Labels) < labelKey(series[j].Labels)
	})
	if len(series) > maxSeries {
		series, truncated = series[:maxSeries], true
	}
	return series, truncated, nil
}

func (m *MetricSeries) summarise() {
	if len(m.Points) == 0 {
		return
	}
	m.Min, m.Max = math.Inf(1), math.Inf(-1)
	var sum float64
	for _, p := range m.Points {
		m.Min, m.Max = math.Min(m.Min, p[1]), math.Max(m.Max, p[1])
		sum += p[1]
	}
	m.Avg = sum / float64(len(m.Points))
	m.Last = m.Points[len(m.Points)-1][1]
}

func labelKey(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s,", k, l[k])
	}
	return b.String()
}

// rawPromParams are the only parameters a raw Prometheus query forwards.
var rawPromParams = map[string][]string{
	"query":       {"query", "time"},
	"query_range": {"query", "start", "end", "step"},
}

// RawMetricQuery runs a PromQL query exactly as written and returns Prometheus' own "data" object. endpoint is
// "query" or "query_range". Only a Scope with no namespace or cluster limit may do this.
func (c *Client) RawMetricQuery(ctx context.Context, s Scope, endpoint string, params url.Values) (json.RawMessage, error) {
	if err := s.needSignal(SignalMetrics); err != nil {
		return nil, err
	}
	if err := s.needUnrestricted("a PromQL query"); err != nil {
		return nil, err
	}
	allowed, ok := rawPromParams[endpoint]
	if !ok {
		return nil, badRequest("unknown metrics endpoint")
	}
	q := url.Values{}
	for _, k := range allowed {
		if v := params.Get(k); v != "" {
			q.Set(k, v)
		}
	}
	if q.Get("query") == "" || len(q.Get("query")) > 4096 {
		return nil, badRequest("query is required and at most 4096 characters")
	}
	if endpoint == "query_range" && (q.Get("start") == "" || q.Get("end") == "" || q.Get("step") == "") {
		return nil, badRequest("query_range needs start, end and step")
	}
	return c.prom(ctx, "/api/v1/"+endpoint, q)
}

// DurationParam reads a duration parameter written as Go ("30s", "2m") or as plain seconds.
// maxDurationSeconds keeps a duration given as plain seconds far below what time.Duration can hold (about 292 years), where
// the conversion would wrap around to a negative one.
const maxDurationSeconds = 366 * 86400

func DurationParam(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if f < 0 || math.IsNaN(f) {
			return 0, badRequest("a duration cannot be negative")
		}
		if f > maxDurationSeconds {
			return 0, badRequest("a duration cannot be longer than %d days", int(maxDurationSeconds/86400))
		}
		return time.Duration(f * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, badRequest("%q is not a duration (30s, 2m, or seconds)", s)
	}
	return d, nil
}
