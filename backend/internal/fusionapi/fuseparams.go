package fusionapi

import (
	"net/url"
	"strconv"
	"strings"
)

// Params that only the fused read takes. They are parsed from a query string, and the batch route builds the same
// url.Values from its JSON body, so a parameter means the same thing wherever it is given.
const (
	ParamFused            = "fused"
	ParamInclude          = "include"
	ParamPad              = "pad"
	ParamSpanPad          = "span_pad"
	ParamMetric           = "metric"
	ParamMetricScope      = "metric_scope"
	ParamMaxLogs          = "max_logs"
	ParamMaxContextLogs   = "max_context_logs"
	ParamMaxSystemLogs    = "max_system_logs"
	ParamMaxSeries        = "max_series"
	ParamPoints           = "points"
	ParamSystemNamespaces = "system_namespaces"
	ParamLogSeverity      = "log_severity"
	ParamLogContains      = "log_contains"
	ParamOmit             = "omit"
	ParamSpanService      = "span_service"
	ParamSpanStatus       = "span_status"
	ParamSpanMinDuration  = "span_min_duration"
)

// FuseParamNames lists every parameter ParseFuseParams reads (the API description and its tests use it).
var FuseParamNames = []string{ParamFused, ParamInclude, ParamPad, ParamSpanPad, ParamMetric, ParamMetricScope, ParamMaxLogs,
	ParamMaxContextLogs, ParamMaxSystemLogs, ParamMaxSeries, ParamPoints, ParamSystemNamespaces, ParamLogSeverity, ParamLogContains,
	ParamOmit, ParamSpanService, ParamSpanStatus, ParamSpanMinDuration}

// Names the include list accepts.
const (
	IncludeLogs        = "logs"
	IncludeContextLogs = "context_logs"
	IncludeSystemLogs  = "system_logs"
	IncludeMetrics     = "metrics"
	IncludeAll         = "all"
	IncludeNone        = "none"
)

// splitList splits a comma-separated parameter, dropping blanks and repeats.
func splitList(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// ParseBool reads a true/false parameter ("" is def).
func ParseBool(name, v string, def bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, badRequest("%s must be true or false", name)
}

// ParseFuseParams reads what a fused read is asked for. fused says whether the caller wants one at all: fused=true, or
// an include list. fused=true alone means include=logs,metrics.
func ParseFuseParams(q url.Values) (opts FuseOptions, fused bool, err error) {
	explicit := q.Has(ParamFused)
	fusedFlag, err := ParseBool(ParamFused, q.Get(ParamFused), false)
	if err != nil {
		return opts, false, err
	}
	include := splitList(q.Get(ParamInclude))
	if explicit && !fusedFlag {
		if len(include) > 0 {
			return opts, false, badRequest("include only applies to a fused read; drop fused=false or include")
		}
		return opts, false, nil
	}
	if !fusedFlag && len(include) == 0 {
		return opts, false, nil
	}
	opts, err = ParseFuseOptions(q)
	return opts, err == nil, err
}

// ParseFuseOptions reads the options of a fused read whether or not fused was named (the batch route is always fused).
// No include list means logs and metrics; include=none means neither, only the trace shaped by the other options.
func ParseFuseOptions(q url.Values) (opts FuseOptions, err error) {
	include := splitList(q.Get(ParamInclude))
	if len(include) == 0 {
		include = []string{IncludeLogs, IncludeMetrics}
	}
	for _, in := range include {
		switch in {
		case IncludeNone:
		case IncludeLogs:
			opts.Logs = true
		case IncludeContextLogs, "unlinked_logs":
			opts.ContextLogs = true
		case IncludeSystemLogs:
			opts.SystemLogs = true
		case IncludeMetrics:
			opts.Metrics = true
		case IncludeAll:
			opts.Logs, opts.ContextLogs, opts.SystemLogs, opts.Metrics = true, true, true, true
		default:
			return opts, badRequest("include takes logs, context_logs, system_logs, metrics, all or none")
		}
	}
	if opts.Pad, err = DurationParam(q.Get(ParamPad)); err != nil {
		return opts, err
	}
	if opts.SpanPad, err = DurationParam(q.Get(ParamSpanPad)); err != nil {
		return opts, err
	}
	if opts.Spans.MinDuration, err = DurationParam(q.Get(ParamSpanMinDuration)); err != nil {
		return opts, err
	}
	opts.MetricRegex = q.Get(ParamMetric)
	opts.LogSeverity, opts.LogContains = q.Get(ParamLogSeverity), q.Get(ParamLogContains)
	opts.Spans.Service, opts.Spans.Status = q.Get(ParamSpanService), q.Get(ParamSpanStatus)
	opts.SystemNamespaces = splitList(q.Get(ParamSystemNamespaces))
	for _, p := range []struct {
		key string
		dst *int
		max int
	}{{ParamMaxLogs, &opts.MaxLogs, hardMaxLogs}, {ParamMaxContextLogs, &opts.MaxContextLogs, hardMaxContextLogs},
		{ParamMaxSystemLogs, &opts.MaxSystemLogs, hardMaxSystemLogs}, {ParamMaxSeries, &opts.MaxSeries, hardMaxSeries}, {ParamPoints, &opts.Points, 500}} {
		if v := q.Get(p.key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return opts, badRequest("%s must be a whole number from 1 to %d", p.key, p.max)
			}
			*p.dst = min(n, p.max)
		}
	}
	for _, m := range splitList(q.Get(ParamMetricScope)) {
		switch m {
		case "app":
			opts.MetricViews.App = true
		case "pod":
			opts.MetricViews.Pod = true
		case "node":
			opts.MetricViews.Node = true
		default:
			return opts, badRequest("metric_scope takes app, pod and/or node")
		}
	}
	for _, o := range splitList(q.Get(ParamOmit)) {
		switch o {
		case "attributes":
			opts.OmitAttributes = true
		case "events":
			opts.OmitEvents = true
		default:
			return opts, badRequest("omit takes attributes and/or events")
		}
	}
	return opts, nil
}
