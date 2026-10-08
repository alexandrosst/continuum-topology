package fusionapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SpanEvent is something that happened during a span (an exception, a log-like annotation).
type SpanEvent struct {
	Time       time.Time      `json:"time"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Span is one unit of work in a trace, with the identity of the thing that did it.
type Span struct {
	SpanID        string         `json:"spanId"`
	ParentSpanID  string         `json:"parentSpanId,omitempty"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind,omitempty"`
	Service       string         `json:"service,omitempty"`
	Namespace     string         `json:"namespace,omitempty"`
	Pod           string         `json:"pod,omitempty"`
	Node          string         `json:"node,omitempty"`
	Cluster       string         `json:"cluster,omitempty"`
	Resource      string         `json:"resource"` // the Resource key it belongs to
	Start         time.Time      `json:"start"`
	End           time.Time      `json:"end"`
	DurationMs    float64        `json:"durationMs"`
	Status        string         `json:"status"` // unset | ok | error
	StatusMessage string         `json:"statusMessage,omitempty"`
	Depth         int            `json:"depth"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Events        []SpanEvent    `json:"events,omitempty"`
	// Logs are the log lines saved with this span's id (filled in by a fused read).
	Logs []LogEntry `json:"logs,omitempty"`
	// Metrics are the points of the series saved for this span's resource that fall inside the span's own time, plus a
	// margin either side (a fused read; see FuseOptions.SpanPad). Associated by service, namespace and pod and by time,
	// not proven: a metric sample carries no trace or span id.
	Metrics []MetricSeries `json:"metrics,omitempty"`
	// Queries are the caller's own PromQL queries (a fused read's promql parameter) cut to this span's time, when
	// promql_spans asked for it. Only queries evaluated per resource can be cut to a span.
	Queries []QueryResult `json:"queries,omitempty"`
}

// Resource is what produced spans: a service in a pod in a namespace of a cluster. Metrics are the series saved for
// the same service, namespace and pod around the trace (filled in by a fused read).
type Resource struct {
	Key              string         `json:"key"`
	Service          string         `json:"service,omitempty"`
	Namespace        string         `json:"namespace,omitempty"`
	Pod              string         `json:"pod,omitempty"`
	Node             string         `json:"node,omitempty"`
	Cluster          string         `json:"cluster,omitempty"`
	Attributes       map[string]any `json:"attributes,omitempty"`
	Metrics          []MetricSeries `json:"metrics,omitempty"`
	MetricsTruncated bool           `json:"metricsTruncated,omitempty"`
	// Logs are lines this resource wrote around the trace that carry no trace id (include=context_logs).
	Logs          []LogEntry `json:"logs,omitempty"`
	LogsTruncated bool       `json:"logsTruncated,omitempty"`
	// Queries are the caller's own PromQL queries, evaluated for this resource over the trace's window (promql).
	Queries []QueryResult `json:"queries,omitempty"`
	// Topology is what Ikhnos knows about the service: what it calls and what calls it (include=topology).
	Topology *ResourceTopology `json:"topology,omitempty"`
}

// Trace is one trace as Tempo has it: its spans in start order, and the resources behind them.
type Trace struct {
	TraceID    string      `json:"traceId"`
	Start      time.Time   `json:"start"`
	End        time.Time   `json:"end"`
	DurationMs float64     `json:"durationMs"`
	Services   []string    `json:"services"`
	SpanCount  int         `json:"spanCount"`
	ErrorCount int         `json:"errorCount"`
	Roots      []string    `json:"roots"`
	Spans      []*Span     `json:"spans"`
	Resources  []*Resource `json:"resources"`
	// Truncated says the trace had more spans than this API returns.
	Truncated bool `json:"truncated,omitempty"`
}

// MaxSpans is the most spans one trace read returns.
const MaxSpans = 5000

// ---- OTLP/JSON, as Tempo returns it ----

type otlpValue struct {
	String *string                       `json:"stringValue"`
	Int    *json.Number                  `json:"intValue"`
	Double json.RawMessage               `json:"doubleValue"` // a number, or the string "NaN", "Infinity" or "-Infinity"
	Bool   *bool                         `json:"boolValue"`
	Bytes  *string                       `json:"bytesValue"`
	Array  *struct{ Values []otlpValue } `json:"arrayValue"`
	KV     *struct{ Values []otlpKV }    `json:"kvlistValue"`
}

type otlpKV struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

func (v otlpValue) any() any {
	switch {
	case v.String != nil:
		return *v.String
	case v.Int != nil:
		if n, err := v.Int.Int64(); err == nil {
			return n
		}
		return v.Int.String()
	case len(v.Double) > 0 && string(v.Double) != "null":
		var f float64
		if json.Unmarshal(v.Double, &f) == nil {
			return f
		}
		var s string // a non-finite value cannot be a JSON number, and must not be one in the answer either
		if json.Unmarshal(v.Double, &s) == nil {
			return s
		}
		return string(v.Double)
	case v.Bool != nil:
		return *v.Bool
	case v.Bytes != nil:
		return *v.Bytes
	case v.Array != nil:
		out := make([]any, 0, len(v.Array.Values))
		for _, e := range v.Array.Values {
			out = append(out, e.any())
		}
		return out
	case v.KV != nil:
		return kvMap(v.KV.Values)
	}
	return nil
}

func kvMap(kvs []otlpKV) map[string]any {
	if len(kvs) == 0 {
		return nil
	}
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value.any()
	}
	return m
}

type otlpSpan struct {
	TraceID      string          `json:"traceId"`
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId"`
	Name         string          `json:"name"`
	Kind         json.RawMessage `json:"kind"`
	Start        json.Number     `json:"startTimeUnixNano"`
	End          json.Number     `json:"endTimeUnixNano"`
	Attributes   []otlpKV        `json:"attributes"`
	Status       struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"status"`
	Events []struct {
		Time       json.Number `json:"timeUnixNano"`
		Name       string      `json:"name"`
		Attributes []otlpKV    `json:"attributes"`
	} `json:"events"`
}

type otlpResourceSpans struct {
	Resource struct {
		Attributes []otlpKV `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []otlpSpan `json:"spans"`
	} `json:"scopeSpans"`
	// Older Tempo and the OTLP draft spelled scopeSpans this way.
	LibrarySpans []struct {
		Spans []otlpSpan `json:"spans"`
	} `json:"instrumentationLibrarySpans"`
}

// traceBody holds the shapes Tempo has answered a trace read with: v2 wraps it in "trace", v1 uses "batches".
type traceBody struct {
	Trace *struct {
		ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
	} `json:"trace"`
	Batches       []otlpResourceSpans `json:"batches"`
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

func (b traceBody) resourceSpans() []otlpResourceSpans {
	switch {
	case b.Trace != nil:
		return b.Trace.ResourceSpans
	case len(b.Batches) > 0:
		return b.Batches
	}
	return b.ResourceSpans
}

// normID turns an id as Tempo wrote it (hex, or base64 of the raw bytes) into lower-case hex of width digits.
func normID(s string, width int) string {
	if s == "" {
		return ""
	}
	if len(s) == width && hexID.MatchString(s) {
		return strings.ToLower(s)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b)*2 == width {
			return fmt.Sprintf("%x", b)
		}
	}
	if hexID.MatchString(s) && len(s) < width {
		return strings.Repeat("0", width-len(s)) + strings.ToLower(s)
	}
	return strings.ToLower(s)
}

func nanos(n json.Number) time.Time {
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || v <= 0 {
		return time.Time{}
	}
	return time.Unix(0, v).UTC()
}

func spanKind(raw json.RawMessage) string {
	s := strings.Trim(string(raw), `"`)
	names := map[string]string{"1": "internal", "2": "server", "3": "client", "4": "producer", "5": "consumer"}
	if n, ok := names[s]; ok {
		return n
	}
	return strings.ToLower(strings.TrimPrefix(s, "SPAN_KIND_"))
}

func spanStatus(raw json.RawMessage) string {
	s := strings.Trim(string(raw), `"`)
	switch s {
	case "1", "STATUS_CODE_OK":
		return "ok"
	case "2", "STATUS_CODE_ERROR":
		return "error"
	}
	return "unset"
}

func strAttr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// buildTrace turns Tempo's resource spans into a Trace, keeping only what the Scope may see.
func buildTrace(id string, rss []otlpResourceSpans, s Scope) *Trace {
	tr := &Trace{TraceID: id, Services: []string{}, Roots: []string{}}
	resIdx := map[string]*Resource{}
	services := map[string]bool{}
	for _, rs := range rss {
		attrs := kvMap(rs.Resource.Attributes)
		ns, cl := strAttr(attrs, attrNamespace), strAttr(attrs, attrCluster)
		if !s.NamespaceVisible(ns) || !s.ClusterVisible(cl) {
			continue
		}
		ident := strings.Join([]string{strAttr(attrs, attrService), ns, strAttr(attrs, attrPod), strAttr(attrs, attrInstance), cl}, "\x00")
		res := resIdx[ident]
		if res == nil {
			res = &Resource{Key: "r" + strconv.Itoa(len(resIdx)+1), Service: strAttr(attrs, attrService), Namespace: ns,
				Pod: strAttr(attrs, attrPod), Node: strAttr(attrs, attrNode), Cluster: cl, Attributes: attrs}
			resIdx[ident] = res
			tr.Resources = append(tr.Resources, res)
		}
		var spans []otlpSpan
		for _, ss := range rs.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
		for _, ss := range rs.LibrarySpans {
			spans = append(spans, ss.Spans...)
		}
		for _, sp := range spans {
			start, end := nanos(sp.Start), nanos(sp.End)
			if end.Before(start) {
				end = start
			}
			out := &Span{
				SpanID: normID(sp.SpanID, 16), ParentSpanID: normID(sp.ParentSpanID, 16), Name: sp.Name, Kind: spanKind(sp.Kind),
				Service: res.Service, Namespace: res.Namespace, Pod: res.Pod, Node: res.Node, Cluster: res.Cluster, Resource: res.Key,
				Start: start, End: end, DurationMs: float64(end.Sub(start)) / float64(time.Millisecond),
				Status: spanStatus(sp.Status.Code), StatusMessage: sp.Status.Message, Attributes: kvMap(sp.Attributes),
			}
			for _, ev := range sp.Events {
				out.Events = append(out.Events, SpanEvent{Time: nanos(ev.Time), Name: ev.Name, Attributes: kvMap(ev.Attributes)})
			}
			tr.Spans = append(tr.Spans, out)
			if res.Service != "" {
				services[res.Service] = true
			}
		}
	}
	sort.SliceStable(tr.Spans, func(i, j int) bool {
		if !tr.Spans[i].Start.Equal(tr.Spans[j].Start) {
			return tr.Spans[i].Start.Before(tr.Spans[j].Start)
		}
		return tr.Spans[i].SpanID < tr.Spans[j].SpanID
	})
	tr.SpanCount = len(tr.Spans)
	if len(tr.Spans) > MaxSpans {
		tr.Spans, tr.Truncated = tr.Spans[:MaxSpans], true
	}
	byID := make(map[string]*Span, len(tr.Spans))
	for _, sp := range tr.Spans {
		byID[sp.SpanID] = sp
	}
	for i, sp := range tr.Spans {
		if i == 0 || sp.Start.Before(tr.Start) {
			tr.Start = sp.Start
		}
		if sp.End.After(tr.End) {
			tr.End = sp.End
		}
		if sp.Status == "error" {
			tr.ErrorCount++
		}
		// A span whose parent is not here (or is hidden from this Scope) is a root of what is visible.
		if p, ok := byID[sp.ParentSpanID]; !ok || sp.ParentSpanID == "" || p == sp {
			tr.Roots = append(tr.Roots, sp.SpanID)
			// A parent this Scope cannot see is not named: its id would confirm a span exists outside the Scope.
			if !s.Unrestricted() {
				sp.ParentSpanID = ""
			}
		}
	}
	for _, sp := range tr.Spans { // depth: walk up, bounded so a cycle in bad data cannot loop
		d := 0
		for p, ok := byID[sp.ParentSpanID]; ok && p != sp && d < 64; p, ok = byID[p.ParentSpanID] {
			d++
		}
		sp.Depth = d
	}
	tr.DurationMs = float64(tr.End.Sub(tr.Start)) / float64(time.Millisecond)
	for svc := range services {
		tr.Services = append(tr.Services, svc)
	}
	sort.Strings(tr.Services)
	return tr
}

// Trace reads one trace by id. A Scope that limits namespaces or clusters sees only the spans of its own; a trace
// with none of them reads as not found, which is also what a trace that does not exist reads as.
func (c *Client) Trace(ctx context.Context, s Scope, id string) (*Trace, error) {
	if err := s.needSignal(SignalTraces); err != nil {
		return nil, err
	}
	id, err := NormalizeTraceID(id)
	if err != nil {
		return nil, err
	}
	var body traceBody
	if err := c.get(ctx, storeTempo, c.Tempo, "/api/v2/traces/"+id, nil, nil, &body); err != nil {
		// A trace Tempo does not have reads exactly as one this Scope may not see, so the two cannot be told apart.
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return nil, errf(http.StatusNotFound, "no trace %s", id)
		}
		return nil, err
	}
	tr := buildTrace(id, body.resourceSpans(), s)
	if tr.SpanCount == 0 {
		return nil, errf(http.StatusNotFound, "no trace %s", id)
	}
	return tr, nil
}

// TraceSummary is one hit of a trace search.
type TraceSummary struct {
	TraceID     string    `json:"traceId"`
	RootService string    `json:"rootService,omitempty"`
	RootName    string    `json:"rootName,omitempty"`
	Start       time.Time `json:"start"`
	DurationMs  float64   `json:"durationMs"`
	// Services are the services of the spans that matched the search; MatchedSpans how many matched.
	Services     []string `json:"services,omitempty"`
	MatchedSpans int      `json:"matchedSpans"`
}

// TraceFilter picks traces. Every field is optional and combines with AND; the conditions are about spans, so a trace
// matches when some span of it does.
type TraceFilter struct {
	Service     string        `json:"service,omitempty"`
	Namespace   string        `json:"namespace,omitempty"`
	Cluster     string        `json:"cluster,omitempty"`
	Categories  []string      `json:"categories,omitempty"` // kubernetes or application, by namespace (see traceCategoryCond)
	Name        string        `json:"name,omitempty"`       // span name
	Status      string        `json:"status,omitempty"`     // error | ok | unset
	MinDuration time.Duration `json:"minDuration,omitempty"`
	MaxDuration time.Duration `json:"maxDuration,omitempty"`
}

func (f TraceFilter) traceQL(s Scope) (string, error) {
	c, err := eqMatchers([]eqFilter{
		{"service", "resource." + attrService, f.Service}, {"namespace", "resource." + attrNamespace, f.Namespace},
		{"cluster", "resource." + attrCluster, f.Cluster}, {"name", "name", f.Name},
	}, func(l, v string) string { return l + " = " + v })
	if err != nil {
		return "", err
	}
	switch f.Status {
	case "":
	case "error", "ok", "unset":
		c = append(c, "status = "+f.Status)
	default:
		return "", badRequest("status must be error, ok or unset")
	}
	if f.MinDuration > 0 {
		c = append(c, "traceDuration >= "+f.MinDuration.String())
	}
	if f.MaxDuration > 0 {
		c = append(c, "traceDuration <= "+f.MaxDuration.String())
	}
	cat, err := traceCategoryCond("resource."+attrNamespace, f.Categories)
	if err != nil {
		return "", err
	}
	if cat != "" {
		c = append(c, cat)
	}
	if ns := s.nsLimit(); len(ns) > 0 {
		c = append(c, anyOf("resource."+attrNamespace, ns))
	}
	if cl := s.clLimit(); len(cl) > 0 {
		c = append(c, anyOf("resource."+attrCluster, cl))
	}
	if len(s.FocusServices) > 0 {
		c = append(c, anyOf("resource."+attrService, s.FocusServices))
	}
	if len(c) == 0 {
		return "{ true }", nil
	}
	return "{ " + strings.Join(c, " && ") + " }", nil
}

func anyOf(attr string, vals []string) string {
	p := make([]string, len(vals))
	for i, v := range vals {
		p[i] = attr + " = " + quote(v)
	}
	return "(" + strings.Join(p, " || ") + ")"
}

type tempoSearch struct {
	Traces []struct {
		TraceID    string         `json:"traceID"`
		RootSvc    string         `json:"rootServiceName"`
		RootName   string         `json:"rootTraceName"`
		Start      json.Number    `json:"startTimeUnixNano"`
		DurationMs float64        `json:"durationMs"`
		SpanSet    *tempoSpanSet  `json:"spanSet"`
		SpanSets   []tempoSpanSet `json:"spanSets"`
	} `json:"traces"`
}

type tempoSpanSet struct {
	Matched int `json:"matched"`
	Spans   []struct {
		Start      json.Number `json:"startTimeUnixNano"`
		Duration   json.Number `json:"durationNanos"`
		Attributes []otlpKV    `json:"attributes"`
	} `json:"spans"`
}

// SearchTraces lists traces with spans matching the filter that started in the range, newest first.
func (c *Client) SearchTraces(ctx context.Context, s Scope, f TraceFilter, tr TimeRange, limit int) ([]TraceSummary, error) {
	if err := s.needSignal(SignalTraces); err != nil {
		return nil, err
	}
	q, err := f.traceQL(s)
	if err != nil {
		return nil, err
	}
	return c.tempoSearch(ctx, s, q, tr, limit)
}

// RawTraceSearch runs a TraceQL query exactly as written. Only a Scope with no namespace or cluster limit may.
func (c *Client) RawTraceSearch(ctx context.Context, s Scope, query string, tr TimeRange, limit int) ([]TraceSummary, error) {
	if err := s.needSignal(SignalTraces); err != nil {
		return nil, err
	}
	if err := s.needUnrestricted("a TraceQL query"); err != nil {
		return nil, err
	}
	if query == "" || len(query) > 4096 {
		return nil, badRequest("q is required and at most 4096 characters")
	}
	return c.tempoSearch(ctx, s, query, tr, limit)
}

func (c *Client) tempoSearch(ctx context.Context, s Scope, q string, tr TimeRange, limit int) ([]TraceSummary, error) {
	var res tempoSearch
	if err := c.get(ctx, storeTempo, c.Tempo, "/api/search", url.Values{
		"q": {q}, "start": {unixSec(tr.From)}, "end": {unixSec(tr.To)}, "limit": {strconv.Itoa(limit)}, "spss": {"20"},
	}, nil, &res); err != nil {
		return nil, err
	}
	restricted := !s.Unrestricted()
	out := make([]TraceSummary, 0, len(res.Traces))
	for _, t := range res.Traces {
		sum := TraceSummary{TraceID: normID(t.TraceID, 32), Start: nanos(t.Start), DurationMs: t.DurationMs}
		sets := t.SpanSets
		if t.SpanSet != nil {
			sets = append(sets, *t.SpanSet)
		}
		seen := map[string]bool{}
		var first, last time.Time
		for _, ss := range sets {
			sum.MatchedSpans += ss.Matched
			for _, sp := range ss.Spans {
				if st := nanos(sp.Start); !st.IsZero() {
					en := st
					if d, err := strconv.ParseInt(sp.Duration.String(), 10, 64); err == nil && d > 0 {
						en = st.Add(time.Duration(d))
					}
					if first.IsZero() || st.Before(first) {
						first = st
					}
					if en.After(last) {
						last = en
					}
				}
				if svc := strAttr(kvMap(sp.Attributes), attrService); svc != "" && !seen[svc] {
					seen[svc] = true
					sum.Services = append(sum.Services, svc)
				}
			}
		}
		sort.Strings(sum.Services)
		// The root of a trace may belong to a namespace this Scope cannot see; only what matched is its to know.
		if !restricted {
			sum.RootService, sum.RootName = t.RootSvc, t.RootName
		} else if !first.IsZero() {
			// Likewise the trace's own start and length cover spans outside the Scope; give the matched spans' extent.
			sum.Start = first
			sum.DurationMs = float64(last.Sub(first)) / float64(time.Millisecond)
		} else {
			sum.Start, sum.DurationMs = time.Time{}, 0
		}
		out = append(out, sum)
	}
	return out, nil
}

// traceServices lists the service names with spans in the range (inside the Scope). A Scope with a limit cannot ask
// Tempo for tag values restricted to it (the tag-values call does not promise to honour the query), so it reads the
// services off a sample of the traces it may see instead: the list is then those of the newest traces, not all.
func (c *Client) traceServices(ctx context.Context, s Scope, tr TimeRange) ([]string, error) {
	if err := s.needSignal(SignalTraces); err != nil {
		return nil, err
	}
	if !s.Unrestricted() || len(s.FocusServices) > 0 {
		hits, err := c.SearchTraces(ctx, s, TraceFilter{}, tr, scopedServiceSample)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var out []string
		for _, h := range hits {
			for _, svc := range h.Services {
				// A trace that touches the focused services also names the ones it passed through; those are not the focus.
				if len(s.FocusServices) > 0 && !slices.Contains(s.FocusServices, svc) {
					continue
				}
				if !seen[svc] {
					seen[svc] = true
					out = append(out, svc)
				}
			}
		}
		sort.Strings(out)
		return out, nil
	}
	q := url.Values{"start": {unixSec(tr.From)}, "end": {unixSec(tr.To)}}
	var res struct {
		TagValues []struct {
			Value string `json:"value"`
		} `json:"tagValues"`
	}
	if err := c.get(ctx, storeTempo, c.Tempo, "/api/v2/search/tag/resource."+attrService+"/values", q, nil, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range res.TagValues {
		if v.Value != "" {
			out = append(out, v.Value)
		}
	}
	return out, nil
}

// scopedServiceSample is how many traces a limited Scope's service listing reads.
const scopedServiceSample = 200
