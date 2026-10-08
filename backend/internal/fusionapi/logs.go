package fusionapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Loki's own names for what OTLP resource and log-record attributes become: index labels for the common ones
// (service.name, k8s.namespace.name, k8s.pod.name) and structured metadata for the rest, all with dots turned into
// underscores. trace_id and span_id are the log record's own fields, kept as structured metadata - the join keys to
// Tempo.
const (
	lokiService   = "service_name"
	lokiNamespace = "k8s_namespace_name"
	lokiPod       = "k8s_pod_name"
	lokiCluster   = "continuum_cluster_id"
	lokiTraceID   = "trace_id"
	lokiSpanID    = "span_id"
	lokiSeverity  = "severity_text"
	lokiNode      = "k8s_node_name"
)

// LogEntry is one log line with the context it was saved with.
type LogEntry struct {
	Time      time.Time `json:"time"`
	Line      string    `json:"line"`
	Severity  string    `json:"severity,omitempty"`
	TraceID   string    `json:"traceId,omitempty"`
	SpanID    string    `json:"spanId,omitempty"`
	Service   string    `json:"service,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	Pod       string    `json:"pod,omitempty"`
	Cluster   string    `json:"cluster,omitempty"`
	// Category is system, kubernetes or application, by the namespace the line came from (see LogCategory).
	Category string `json:"category,omitempty"`
	// Labels are the stream's index labels; Metadata the record's structured metadata (everything else OTLP carried).
	Labels   map[string]string `json:"labels,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// LogFilter picks log lines. Every field is optional and combines with AND.
type LogFilter struct {
	Service   string `json:"service,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	// Categories keeps lines of these categories (system, kubernetes, application); empty keeps all.
	Categories []string `json:"categories,omitempty"`
	TraceID    string   `json:"traceId,omitempty"`
	SpanID     string   `json:"spanId,omitempty"`
	// Severity matches the record's severity text, ignoring case (error, warn, info, ...).
	Severity string `json:"severity,omitempty"`
	// Contains keeps only lines containing this text.
	Contains string `json:"contains,omitempty"`
	// Node keeps lines written on this Kubernetes node.
	Node string `json:"node,omitempty"`
	// Services keeps lines of any of these services (Service, when set, wins). It is an index label, so a read that knows
	// the services it wants (a trace's) does not have to open every stream.
	Services []string `json:"services,omitempty"`
	// Namespaces keeps lines of any of these namespaces (Namespace, when set, narrows it further).
	Namespaces []string `json:"namespaces,omitempty"`
	// NoTrace keeps only lines that carry no trace id: the context around a trace rather than what it wrote itself.
	NoTrace bool `json:"noTrace,omitempty"`
	// Backward lists newest first (Loki's default); otherwise oldest first.
	Backward bool `json:"backward,omitempty"`
}

var severityText = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

// severityRegex turns "error" or "error,warn" into a case-insensitive match for any of them.
func severityRegex(v string) (string, error) {
	parts := strings.Split(v, ",")
	if len(parts) > 8 {
		return "", badRequest("severity names at most 8 levels")
	}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if !severityText.MatchString(p) {
			return "", badRequest("severity must be words such as error, warn or info, separated by commas")
		}
		parts[i] = p
	}
	if len(parts) == 1 {
		return "(?i)" + parts[0], nil // (Loki anchors a label regex itself)
	}
	return "(?i)(" + strings.Join(parts, "|") + ")", nil
}

// logQL builds the query: a stream selector (always at least one matcher that cannot be empty, which Loki requires),
// then line filters, then label filters on the structured metadata. The Scope's namespace limit goes in the selector
// and its cluster limit in the filters.
func (f LogFilter) logQL(s Scope) (string, error) {
	sel, err := f.selector(s)
	if err != nil {
		return "", err
	}
	return f.pipeline(s, sel)
}

// selector is the stream selector alone: the part Loki's label-value and series calls accept.
func (f LogFilter) selector(s Scope) (string, error) {
	var sel []string
	switch {
	case f.Service != "":
		if err := checkValue("service", f.Service); err != nil {
			return "", err
		}
		sel = append(sel, lokiService+"="+quote(f.Service))
	case len(f.Services) > 0:
		for _, n := range f.Services {
			if err := checkValue("service", n); err != nil {
				return "", err
			}
		}
		sel = append(sel, lokiService+"=~"+quote(regexAny(f.Services)))
	case len(s.FocusServices) == 0:
		sel = append(sel, lokiService+`=~".+"`)
	}
	if len(s.FocusServices) > 0 {
		sel = append(sel, lokiService+"=~"+quote(regexAny(s.FocusServices)))
	}
	eq, err := eqMatchers([]eqFilter{{"namespace", lokiNamespace, f.Namespace}, {"pod", lokiPod, f.Pod}}, func(l, v string) string { return l + "=" + v })
	if err != nil {
		return "", err
	}
	sel = append(sel, eq...)
	if len(f.Namespaces) > 0 {
		for _, n := range f.Namespaces {
			if err := checkValue("namespace", n); err != nil {
				return "", err
			}
		}
		sel = append(sel, lokiNamespace+"=~"+quote(regexAny(f.Namespaces)))
	}
	if ns := s.nsLimit(); len(ns) > 0 {
		sel = append(sel, lokiNamespace+"=~"+quote(regexAny(ns)))
	}
	sel = append(sel, logCategoryMatchers(lokiNamespace, f.Categories)...)
	return "{" + strings.Join(sel, ",") + "}", nil
}

// pipeline adds the line and label filters to a selector.
func (f LogFilter) pipeline(s Scope, q string) (string, error) {
	if f.Contains != "" {
		if err := checkValue("contains", f.Contains); err != nil {
			return "", err
		}
		q += " |= " + quote(f.Contains)
	}
	if f.TraceID != "" {
		id, err := NormalizeTraceID(f.TraceID)
		if err != nil {
			return "", err
		}
		q += " | " + lokiTraceID + "=" + quote(id)
	}
	if f.SpanID != "" {
		id, err := NormalizeSpanID(f.SpanID)
		if err != nil {
			return "", err
		}
		q += " | " + lokiSpanID + "=" + quote(id)
	}
	if f.NoTrace {
		q += " | " + lokiTraceID + `=""`
	}
	if f.Node != "" {
		if err := checkValue("node", f.Node); err != nil {
			return "", err
		}
		q += " | " + lokiNode + "=" + quote(f.Node)
	}
	if f.Severity != "" {
		re, err := severityRegex(f.Severity)
		if err != nil {
			return "", err
		}
		q += " | " + lokiSeverity + "=~" + quote(re)
	}
	if f.Cluster != "" {
		if err := checkValue("cluster", f.Cluster); err != nil {
			return "", err
		}
		q += " | " + lokiCluster + "=" + quote(f.Cluster)
	}
	if cl := s.clLimit(); len(cl) > 0 {
		q += " | " + lokiCluster + "=~" + quote(regexAny(cl))
	}
	return q, nil
}

// lokiStreams is Loki's "streams" result. With the categorize-labels flag a value is [ns, line, {structuredMetadata,
// parsed}]; without it (an older Loki) it is [ns, line] and the structured metadata sits among the stream's labels.
type lokiStreams struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string   `json:"stream"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

var lokiHeaders = map[string]string{"X-Loki-Response-Encoding-Flags": "categorize-labels"}

// Logs returns the lines matching the filter in the range, at most limit of them. truncated says there were more (a read
// that returns exactly limit lines because that is all there are is not truncated).
func (c *Client) Logs(ctx context.Context, s Scope, f LogFilter, tr TimeRange, limit int) (entries []LogEntry, truncated bool, err error) {
	if err := s.needSignal(SignalLogs); err != nil {
		return nil, false, err
	}
	q, err := f.logQL(s)
	if err != nil {
		return nil, false, err
	}
	return c.lokiQuery(ctx, s, q, f.Backward, tr, limit)
}

// RawLogQuery runs a LogQL query exactly as written. Only a Scope with no namespace or cluster limit may do this.
func (c *Client) RawLogQuery(ctx context.Context, s Scope, query string, backward bool, tr TimeRange, limit int) ([]LogEntry, bool, error) {
	if err := s.needSignal(SignalLogs); err != nil {
		return nil, false, err
	}
	if err := s.needUnrestricted("a LogQL query"); err != nil {
		return nil, false, err
	}
	if query == "" || len(query) > 4096 {
		return nil, false, badRequest("query is required and at most 4096 characters")
	}
	return c.lokiQuery(ctx, s, query, backward, tr, limit)
}

func (c *Client) lokiQuery(ctx context.Context, s Scope, query string, backward bool, tr TimeRange, limit int) ([]LogEntry, bool, error) {
	if err := tr.within(storeLoki, MaxLogWindow); err != nil {
		return nil, false, err
	}
	dir := "forward"
	if backward {
		dir = "backward"
	}
	var res lokiStreams
	err := c.get(ctx, storeLoki, c.Loki, "/loki/api/v1/query_range", url.Values{
		"query": {query}, "start": {unixNano(tr.From)}, "end": {unixNano(tr.To)}, "limit": {strconv.Itoa(limit + 1)}, "direction": {dir}, // (one more: see MetricNames)
	}, lokiHeaders, &res)
	if err != nil {
		return nil, false, err
	}
	if res.Status != "success" || (res.Data.ResultType != "streams" && res.Data.ResultType != "") {
		return nil, false, badRequest("that LogQL query does not return log lines (it returned %q)", res.Data.ResultType)
	}
	var out []LogEntry
	for _, st := range res.Data.Result {
		for _, v := range st.Values {
			e, ok := lokiEntry(st.Stream, v)
			if !ok {
				continue
			}
			if !s.NamespaceVisible(e.Namespace) || !s.ClusterVisible(e.Cluster) {
				continue
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if backward {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].Time.Before(out[j].Time)
	})
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

func lokiEntry(stream map[string]string, v []json.RawMessage) (LogEntry, bool) {
	if len(v) < 2 {
		return LogEntry{}, false
	}
	var ns, line string
	if json.Unmarshal(v[0], &ns) != nil || json.Unmarshal(v[1], &line) != nil {
		return LogEntry{}, false
	}
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil {
		return LogEntry{}, false
	}
	meta := map[string]string{}
	if len(v) > 2 {
		var extra struct {
			StructuredMetadata map[string]string `json:"structuredMetadata"`
		}
		if json.Unmarshal(v[2], &extra) == nil {
			meta = extra.StructuredMetadata
		}
	}
	get := func(k string) string {
		if x := meta[k]; x != "" {
			return x
		}
		return stream[k]
	}
	e := LogEntry{
		Time: time.Unix(0, n).UTC(), Line: line,
		Severity: firstNonEmpty(get(lokiSeverity), get("detected_level"), get("level")),
		TraceID:  get(lokiTraceID), SpanID: get(lokiSpanID),
		Service: stream[lokiService], Namespace: stream[lokiNamespace], Pod: stream[lokiPod], Cluster: get(lokiCluster),
		Labels: stream,
	}
	e.Category = LogCategory(e.Namespace)
	if len(meta) > 0 {
		e.Metadata = meta
	}
	return e, true
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// scopedLogSample is how many lines a cluster-limited Scope's service listing reads.
const scopedLogSample = 1000

// logServices lists the service names that have logs in the range (inside the Scope). A Scope limited to clusters reads
// them off a sample of the newest lines; sample says so (it is empty for a complete list).
func (c *Client) logServices(ctx context.Context, s Scope, tr TimeRange) (names []string, sample string, err error) {
	if err := s.needSignal(SignalLogs); err != nil {
		return nil, "", err
	}
	if len(s.clLimit()) > 0 {
		// A cluster is structured metadata, which label-value calls cannot filter on: read the newest in-scope lines
		// and take the services they came from.
		lines, truncated, err := c.Logs(ctx, s, LogFilter{Backward: true}, tr.newest(MaxLogWindow), scopedLogSample)
		if err != nil {
			return nil, "", err
		}
		if truncated {
			sample = fmt.Sprintf("the newest %d log lines", scopedLogSample)
		}
		seen := map[string]bool{}
		var out []string
		for _, l := range lines {
			if l.Service != "" && !seen[l.Service] {
				seen[l.Service] = true
				out = append(out, l.Service)
			}
		}
		return out, sample, nil
	}
	sel, err := LogFilter{}.selector(s)
	if err != nil {
		return nil, "", err
	}
	var res struct {
		Data []string `json:"data"`
	}
	if err := c.get(ctx, storeLoki, c.Loki, "/loki/api/v1/label/"+lokiService+"/values", url.Values{
		"query": {sel}, "start": {unixNano(tr.From)}, "end": {unixNano(tr.To)},
	}, nil, &res); err != nil {
		return nil, "", err
	}
	if res.Data == nil {
		return nil, "", errf(http.StatusBadGateway, "%s answered with something unexpected", storeLoki)
	}
	return res.Data, "", nil
}
