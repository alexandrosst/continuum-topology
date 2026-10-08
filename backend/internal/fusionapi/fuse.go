package fusionapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// What a part of a fused read reports about itself.
const (
	SourceOK           = "ok"
	SourceNotRequested = "not requested"
	SourceNotAllowed   = "not allowed" // the token's scope does not include this signal
	SourceUnavailable  = "unavailable" // the store could not be reached: FUSION off or starting
	SourceError        = "error"

	// The keys of Fused.Sources beyond the three signals, for the log reads that are not tied to a span.
	SourceContextLogs = "contextLogs"
	SourceSystemLogs  = "systemLogs"
)

// FuseOptions say what to join to a trace.
type FuseOptions struct {
	Logs    bool
	Metrics bool
	// Pad widens the window around the trace in which logs and metrics are looked for. Default 2 minutes.
	Pad time.Duration
	// MetricRegex limits which metric names are returned. Default: all.
	MetricRegex string
	// MaxLogs is the most log lines returned (default 500). MaxSeries the most series per resource (default 15).
	MaxLogs   int
	MaxSeries int
	// Points is the target number of samples per series (default 60).
	Points int
	// SpanPad widens each span's own time when its metrics are cut out of its resource's series (default 30 seconds).
	// Samples arrive every 30 to 60 seconds, so a span that lasts milliseconds would otherwise often have none.
	SpanPad time.Duration

	// ContextLogs also reads, for each resource, the lines it wrote around the trace that carry no trace id (the
	// context a failing request sat in). They sit on the resource. MaxContextLogs is the most per resource (default 50).
	ContextLogs    bool
	MaxContextLogs int
	// SystemLogs also reads the lines of the system namespaces (SystemNamespaces, default kube-system) on the nodes the
	// trace ran on, over the same window. They sit on the fused object. MaxSystemLogs is the most in all (default 100).
	SystemLogs       bool
	SystemNamespaces []string
	MaxSystemLogs    int
	// LogSeverity ("error" or "error,warn") and LogContains narrow every log read of this fused read.
	LogSeverity string
	LogContains string
	// MetricViews says which series are read for a resource: the service's own, its pod's, its node's.
	MetricViews MetricViews
	// OmitAttributes and OmitEvents leave the span and resource attributes, or the span events, out of the answer.
	OmitAttributes bool
	OmitEvents     bool
	// Spans keeps only the spans that match (the trace's own totals stay those of the whole trace).
	Spans SpanFilter

	// PromQL are the caller's own queries (see promql.go). PromQLSpans also cuts the per-resource ones to each span's time.
	// PromQLStep and PromQLPad override the step and the window (default: those of the metrics); PromQLServices limits
	// the per-resource queries to resources of these services.
	PromQL         []PromQuery
	PromQLSpans    bool
	PromQLStep     time.Duration
	PromQLPad      time.Duration
	PromQLServices []string

	// Topology adds what each service calls and is called by; Changes the events around the trace (ChangesBefore before
	// it, Pad after it, at most MaxChanges). Both come from Extras, which the server sets; without it they are unavailable.
	Topology      bool
	Changes       bool
	ChangesBefore time.Duration
	MaxChanges    int
	Extras        Extras
}

// MetricViews picks which metric series a resource carries. None set means the default, App and Pod.
type MetricViews struct{ App, Pod, Node bool }

func (v MetricViews) orDefault() MetricViews {
	if !v.App && !v.Pod && !v.Node {
		return MetricViews{App: true, Pod: true}
	}
	return v
}

// SpanFilter narrows the spans a fused read returns. Every field is optional and combines with AND.
type SpanFilter struct {
	Service     string
	Status      string // error | ok | unset
	MinDuration time.Duration
}

func (f SpanFilter) active() bool { return f.Service != "" || f.Status != "" || f.MinDuration > 0 }

func (f SpanFilter) keeps(sp *Span) bool {
	return (f.Service == "" || sp.Service == f.Service) && (f.Status == "" || sp.Status == f.Status) &&
		sp.DurationMs >= float64(f.MinDuration)/float64(time.Millisecond)
}

const (
	defaultPad     = 2 * time.Minute
	maxPad         = time.Hour
	defaultMaxLogs = 500
	hardMaxLogs    = 2000
	defaultSpanPad = 30 * time.Second
	// maxSpanPoints is the most metric points all the spans of one fused read carry between them. Spans of one resource
	// that overlap in time repeat the same samples, so a trace with thousands of spans would otherwise multiply the answer;
	// past it a span keeps each series' min, max, average and last value but not its points.
	maxSpanPoints         = 20000
	defaultMaxContextLogs = 50
	hardMaxContextLogs    = 500
	defaultMaxSystemLogs  = 100
	hardMaxSystemLogs     = 1000
	maxSystemNodes        = 5
	defaultMaxSeries      = 15
	hardMaxSeries         = 100
	fuseConcurrency       = 4
	// maxFusedResources is the most resources one fused read looks up metrics for; a trace with more says so.
	maxFusedResources = 20
)

func (o *FuseOptions) defaults() error {
	if o.Pad == 0 {
		o.Pad = defaultPad
	}
	if o.Pad < 0 || o.Pad > maxPad {
		return badRequest("pad must be between 0 and %s", maxPad)
	}
	if o.SpanPad == 0 {
		o.SpanPad = defaultSpanPad
	}
	if o.SpanPad < 0 || o.SpanPad > maxPad {
		return badRequest("span_pad must be between 0 and %s", maxPad)
	}
	if o.MaxLogs <= 0 {
		o.MaxLogs = defaultMaxLogs
	}
	if o.MaxLogs > hardMaxLogs {
		o.MaxLogs = hardMaxLogs
	}
	if o.MaxSeries <= 0 {
		o.MaxSeries = defaultMaxSeries
	}
	if o.MaxSeries > hardMaxSeries {
		o.MaxSeries = hardMaxSeries
	}
	if o.Points <= 0 {
		o.Points = 60
	}
	if o.MaxContextLogs <= 0 {
		o.MaxContextLogs = defaultMaxContextLogs
	}
	if o.MaxContextLogs > hardMaxContextLogs {
		o.MaxContextLogs = hardMaxContextLogs
	}
	if o.MaxSystemLogs <= 0 {
		o.MaxSystemLogs = defaultMaxSystemLogs
	}
	if o.MaxSystemLogs > hardMaxSystemLogs {
		o.MaxSystemLogs = hardMaxSystemLogs
	}
	if len(o.SystemNamespaces) == 0 {
		o.SystemNamespaces = []string{"kube-system"}
	}
	if o.PromQLPad < 0 || o.PromQLPad > maxPad {
		return badRequest("promql_pad must be between 0 and %s", maxPad)
	}
	if o.PromQLStep < 0 || (o.PromQLStep > 0 && o.PromQLStep < time.Second) {
		return badRequest("promql_step must be at least one second")
	}
	if o.ChangesBefore == 0 {
		o.ChangesBefore = defaultChangesBefore
	}
	if o.ChangesBefore < 0 || o.ChangesBefore > maxChangesBefore {
		return badRequest("changes_before must be between 0 and %s", maxChangesBefore)
	}
	if o.MaxChanges <= 0 {
		o.MaxChanges = defaultMaxChanges
	}
	if o.MaxChanges > hardMaxChanges {
		o.MaxChanges = hardMaxChanges
	}
	switch o.Spans.Status {
	case "", "error", "ok", "unset":
	default:
		return badRequest("span_status must be error, ok or unset")
	}
	return nil
}

// sourceState is what a part of a read reports about itself when err ended it.
func sourceState(err error) string {
	if IsUnavailable(err) {
		return SourceUnavailable
	}
	return SourceError
}

// callerGone is the error of a context whose caller went away. A request that merely ran out of its time is not that: it
// still returns what the stores answered, with the parts that did not make it named in the sources and warnings.
func callerGone(ctx context.Context) error {
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// FusedLogs says what became of the trace's log lines. The lines whose span is in the trace sit on that span; these
// are the rest.
type FusedLogs struct {
	Total   int `json:"total"`
	Matched int `json:"matched"` // lines attached to a span
	// Unmatched carries a trace id but no span id, or the id of a span this read does not have.
	Unmatched []LogEntry `json:"unmatched,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
	// OnFilteredSpans counts lines that sit on spans span_* filters left out of the answer (they are in Total and Matched).
	OnFilteredSpans int `json:"onFilteredSpans,omitempty"`
}

// SystemLogs are the lines of the system namespaces on the nodes a trace ran on.
type SystemLogs struct {
	Namespaces []string   `json:"namespaces"`
	Nodes      []string   `json:"nodes"`
	Entries    []LogEntry `json:"entries"`
	Truncated  bool       `json:"truncated,omitempty"`
}

// Fused is a trace with the logs and metrics saved around it: each span carries the log lines written under its id,
// and each resource the metric series of the same service, namespace and pod over the trace's time. A part that could
// not be read is named in Sources and Warnings; the trace itself always comes back or the whole read fails.
type Fused struct {
	*Trace
	Logs FusedLogs `json:"logs"`
	// SystemLogs is set when include=system_logs was asked for and the logs could be read.
	SystemLogs *SystemLogs `json:"systemLogs,omitempty"`
	// Queries are the caller's own queries that are not about one resource (promql); the others sit on the resources.
	Queries []QueryResult `json:"queries,omitempty"`
	// Changes are events Ikhnos recorded about the trace's services, nodes and clusters around the trace (include=changes).
	Changes []ChangeEvent `json:"changes,omitempty"`
	// SpansOmitted counts spans the span_* filters left out; the trace's spanCount is that of the whole trace.
	SpansOmitted int               `json:"spansOmitted,omitempty"`
	Sources      map[string]string `json:"sources"`
	// Joins says in words how each requested signal was tied to the trace, because the two are not alike: a log line
	// carries the trace and span id, a metric sample carries neither.
	Joins    map[string]string `json:"joins,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

// FuseTrace reads one trace and joins logs and metrics to it as asked.
func (c *Client) FuseTrace(ctx context.Context, s Scope, id string, opts FuseOptions) (*Fused, error) {
	if err := opts.defaults(); err != nil {
		return nil, err
	}
	if opts.MetricRegex != "" {
		if _, err := (MetricFilter{NameRegex: opts.MetricRegex}).matchers(s); err != nil {
			return nil, err
		}
	}
	// A bad log filter is the caller's mistake, whatever the stores are doing: say so before reading anything.
	if opts.Logs || opts.ContextLogs || opts.SystemLogs {
		if _, err := (LogFilter{Severity: opts.LogSeverity, Contains: opts.LogContains}).logQL(s); err != nil {
			return nil, err
		}
	}
	if opts.SystemLogs {
		for _, ns := range opts.SystemNamespaces {
			if err := checkValue("system_namespaces", ns); err != nil {
				return nil, err
			}
		}
	}
	tr, err := c.Trace(ctx, s, id)
	if err != nil {
		return nil, err
	}
	f := &Fused{Trace: tr, Sources: map[string]string{SignalTraces: SourceOK, SignalLogs: SourceNotRequested, SignalMetrics: SourceNotRequested}}
	f.Joins = map[string]string{}
	if opts.Logs {
		f.Joins[SignalLogs] = "exact: log records that carry this trace's id, each placed on the span whose id it carries"
	}
	if opts.ContextLogs {
		f.Sources[SourceContextLogs] = SourceNotRequested
		f.Joins[SourceContextLogs] = "associated, not proven: lines with no trace id from the same service, namespace and pod, in the trace's window (padded by pad); on the resource, not on a span"
	}
	if opts.SystemLogs {
		f.Sources[SourceSystemLogs] = SourceNotRequested
		f.Joins[SourceSystemLogs] = "associated, not proven: lines of " + strings.Join(opts.SystemNamespaces, ", ") + " on the nodes the trace's pods ran on, in the trace's window (padded by pad)"
	}
	if opts.Metrics {
		f.Joins[SignalMetrics] = "associated, not proven: series saved for the same service, namespace and pod; each resource carries them over the trace's time and each span the points inside its own time (plus span_pad either side); a metric sample carries no trace id"
	}
	if len(opts.PromQL) > 0 {
		f.Sources[SourcePromQL] = SourceNotRequested
		f.Joins[SourcePromQL] = "associated, not proven: your own queries, each evaluated for a resource with its service, namespace and pod filled in (or once for the trace), over the trace's time; a metric sample carries no trace id"
	}
	opts.ikhnosJoins(f)
	window := TimeRange{From: tr.Start.Add(-opts.Pad), To: tr.End.Add(opts.Pad)}
	if !window.From.Before(window.To) {
		window.To = window.From.Add(time.Second)
	}
	if window.To.Sub(window.From) > MaxWindow { // a trace that spans weeks must not turn into an unbounded store read
		window.From = window.To.Add(-MaxWindow)
		f.Warnings = append(f.Warnings, fmt.Sprintf("the trace is longer than %d days; logs and metrics cover its last %d days", int(MaxWindow.Hours()/24), int(MaxWindow.Hours()/24)))
	}

	var mu sync.Mutex
	setSource := func(signal, state string) {
		mu.Lock()
		defer mu.Unlock()
		// A source that already failed stays failed: later goroutines of the same signal only ever add to it.
		if cur := f.Sources[signal]; cur == SourceError || cur == SourceUnavailable {
			return
		}
		f.Sources[signal] = state
	}
	warn := func(signal string, err error) {
		mu.Lock()
		defer mu.Unlock()
		f.Sources[signal] = sourceState(err)
		f.Warnings = append(f.Warnings, fmt.Sprintf("%s: %v", signal, err))
	}
	note := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		f.Warnings = append(f.Warnings, fmt.Sprintf(format, a...))
	}
	var wg sync.WaitGroup

	// The resources worth a lookup of their own: those with a service or a pod, at most maxFusedResources.
	var looked []*Resource
	for _, r := range tr.Resources {
		if r.Service == "" && r.Pod == "" {
			continue
		}
		if len(looked) == maxFusedResources {
			note("the trace has more than %d resources; the rest have no metrics or context logs here", maxFusedResources)
			break
		}
		looked = append(looked, r)
	}

	switch {
	case !opts.Logs:
	case !s.Allows(SignalLogs):
		setSource(SignalLogs, SourceNotAllowed)
	default:
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, truncated, err := c.Logs(ctx, s, LogFilter{TraceID: tr.TraceID, Severity: opts.LogSeverity, Contains: opts.LogContains}, window, opts.MaxLogs)
			if err != nil {
				warn(SignalLogs, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			f.Sources[SignalLogs] = SourceOK
			attachLogs(f, lines, truncated)
		}()
	}

	switch {
	case !opts.ContextLogs:
	case !s.Allows(SignalLogs):
		setSource(SourceContextLogs, SourceNotAllowed)
	default:
		setSource(SourceContextLogs, SourceOK)
		sem := make(chan struct{}, fuseConcurrency) // a trace of many resources must not take every slot the stores share
		for _, r := range looked {
			r := r
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := acquire(ctx, sem)
				if err != nil {
					warn(SourceContextLogs, err)
					return
				}
				defer release()
				lines, truncated, err := c.Logs(ctx, s, LogFilter{Service: r.Service, Namespace: r.Namespace, Pod: r.Pod, Cluster: r.Cluster, NoTrace: true,
					Severity: opts.LogSeverity, Contains: opts.LogContains, Backward: true}, window, opts.MaxContextLogs)
				if err != nil {
					warn(SourceContextLogs, err)
					return
				}
				reverseLogs(lines) // newest first out of the store; a resource's lines read oldest first
				mu.Lock()
				r.Logs, r.LogsTruncated = lines, truncated
				mu.Unlock()
			}()
		}
	}

	switch {
	case !opts.SystemLogs:
	case !s.Allows(SignalLogs):
		setSource(SourceSystemLogs, SourceNotAllowed)
	default:
		wg.Add(1)
		go func() {
			defer wg.Done()
			sl, warns, err := c.systemLogs(ctx, s, tr, opts, window)
			if err != nil {
				warn(SourceSystemLogs, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			f.Sources[SourceSystemLogs] = SourceOK
			f.SystemLogs = sl
			for _, w := range warns {
				f.Warnings = append(f.Warnings, w)
			}
		}()
	}

	switch {
	case !opts.Metrics:
	case !s.Allows(SignalMetrics):
		setSource(SignalMetrics, SourceNotAllowed)
	default:
		setSource(SignalMetrics, SourceOK)
		step, err := ChooseStep(window, 0, opts.Points)
		if err != nil {
			wg.Wait() // the log reads, if started, must not outlive this call
			return nil, err
		}
		sem := make(chan struct{}, fuseConcurrency)
		for _, r := range looked {
			r := r
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := acquire(ctx, sem)
				if err != nil {
					warn(SignalMetrics, err)
					return
				}
				defer release()
				series, truncated, err := c.resourceMetrics(ctx, s, r, opts, window, step)
				if err != nil {
					warn(SignalMetrics, err)
					return
				}
				mu.Lock()
				r.Metrics, r.MetricsTruncated = series, truncated
				mu.Unlock()
			}()
		}
	}

	switch {
	case len(opts.PromQL) == 0:
	case !s.Allows(SignalMetrics):
		setSource(SourcePromQL, SourceNotAllowed)
	case !s.Unrestricted():
		setSource(SourcePromQL, SourceNotAllowed)
		note("promql: your own queries can only be run by a caller whose access is not limited to certain namespaces or clusters")
	default:
		qwin := window
		if opts.PromQLPad > 0 {
			qwin = TimeRange{From: tr.Start.Add(-opts.PromQLPad), To: tr.End.Add(opts.PromQLPad)}
			if !qwin.From.Before(qwin.To) {
				qwin.To = qwin.From.Add(time.Second)
			}
			if qwin.To.Sub(qwin.From) > MaxWindow {
				qwin.From = qwin.To.Add(-MaxWindow)
			}
		}
		qstep := opts.PromQLStep
		if qstep == 0 {
			var serr error
			if qstep, serr = ChooseStep(qwin, 0, opts.Points); serr != nil {
				wg.Wait()
				return nil, serr
			}
		} else if _, serr := ChooseStep(qwin, qstep, opts.Points); serr != nil {
			wg.Wait()
			return nil, serr
		}
		setSource(SourcePromQL, SourceOK)
		wg.Add(1)
		go func() {
			defer wg.Done()
			global, warns, err := c.fusePromQL(ctx, s, tr, looked, opts, qwin, qstep)
			mu.Lock()
			f.Queries = global
			f.Warnings = append(f.Warnings, warns...)
			mu.Unlock()
			if err != nil {
				warn(SourcePromQL, err)
			}
		}()
	}

	if opts.Topology || opts.Changes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.fuseIkhnos(ctx, s, f, looked, opts, warn, setSource, note)
		}()
	}
	wg.Wait()
	if err := callerGone(ctx); err != nil {
		return nil, err
	}
	if len(opts.PromQL) > 0 && opts.PromQLSpans && f.Sources[SourcePromQL] != SourceNotAllowed {
		if cut := attachSpanQueries(f, opts.SpanPad); cut {
			f.Warnings = append(f.Warnings, fmt.Sprintf("promql: spans carry more than %d points between them; later spans keep each series' summary but not its points", maxSpanPoints))
		}
	}
	if opts.Metrics && f.Sources[SignalMetrics] == SourceOK {
		if cut := attachSpanMetrics(f, opts.SpanPad); cut {
			f.Warnings = append(f.Warnings, fmt.Sprintf("metrics: spans carry more than %d points between them; later spans keep each series' summary but not its points", maxSpanPoints))
		}
	}
	shape(f, opts)
	return f, nil
}

// shape applies the options that only narrow the answer: the span filter and the attributes and events left out.
func shape(f *Fused, opts FuseOptions) {
	if opts.Spans.active() {
		kept := f.Spans[:0:0]
		for _, sp := range f.Spans {
			if opts.Spans.keeps(sp) {
				kept = append(kept, sp)
				continue
			}
			f.SpansOmitted++
			f.Logs.OnFilteredSpans += len(sp.Logs)
		}
		f.Spans = kept
	}
	for _, sp := range f.Spans {
		if opts.OmitAttributes {
			sp.Attributes = nil
		}
		if opts.OmitEvents {
			sp.Events = nil
		}
	}
	if opts.OmitAttributes {
		for _, r := range f.Resources {
			r.Attributes = nil
		}
	}
}

func reverseLogs(l []LogEntry) {
	for i, j := 0, len(l)-1; i < j; i, j = i+1, j-1 {
		l[i], l[j] = l[j], l[i]
	}
}

// systemLogs reads the system namespaces' lines on the nodes the trace ran on (one read per cluster and node, at most
// maxSystemNodes), newest MaxSystemLogs of them in all, oldest first.
func (c *Client) systemLogs(ctx context.Context, s Scope, tr *Trace, opts FuseOptions, window TimeRange) (*SystemLogs, []string, error) {
	type loc struct{ cluster, node string }
	var locs []loc
	seen := map[loc]bool{}
	for _, r := range tr.Resources {
		l := loc{r.Cluster, r.Node}
		if r.Node != "" && !seen[l] {
			seen[l] = true
			locs = append(locs, l)
		}
	}
	sl := &SystemLogs{Namespaces: opts.SystemNamespaces, Nodes: []string{}, Entries: []LogEntry{}}
	var warns []string
	visible := false
	for _, ns := range opts.SystemNamespaces {
		visible = visible || s.NamespaceVisible(ns)
	}
	if !visible {
		return sl, []string{"system_logs: this token cannot read the system namespaces (" + strings.Join(opts.SystemNamespaces, ", ") + ")"}, nil
	}
	if len(locs) == 0 {
		return sl, []string{"system_logs: the trace does not say which node its pods ran on"}, nil
	}
	if len(locs) > maxSystemNodes {
		warns = append(warns, fmt.Sprintf("system_logs: the trace ran on %d nodes; the first %d are read", len(locs), maxSystemNodes))
		locs = locs[:maxSystemNodes]
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for _, l := range locs {
		l := l
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, trunc, err := c.Logs(ctx, s, LogFilter{Namespaces: opts.SystemNamespaces, Node: l.node, Cluster: l.cluster,
				Severity: opts.LogSeverity, Contains: opts.LogContains, Backward: true}, window, opts.MaxSystemLogs)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			sl.Nodes = append(sl.Nodes, l.node)
			sl.Entries = append(sl.Entries, lines...)
			sl.Truncated = sl.Truncated || trunc
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, nil, firstErr
	}
	sort.Strings(sl.Nodes)
	sort.SliceStable(sl.Entries, func(i, j int) bool { return sl.Entries[i].Time.After(sl.Entries[j].Time) })
	if len(sl.Entries) > opts.MaxSystemLogs {
		sl.Entries, sl.Truncated = sl.Entries[:opts.MaxSystemLogs], true
	}
	reverseLogs(sl.Entries)
	return sl, warns, nil
}

// attachSpanMetrics gives each span the part of its resource's series that falls inside the span's own time widened by
// pad either side. It reads nothing more from the store: the resource's series, already fetched over the whole trace,
// are cut. A series with no point in a span's window is left off that span. It reports whether the point budget ran out.
func attachSpanMetrics(f *Fused, pad time.Duration) (budgetHit bool) {
	byKey := make(map[string]*Resource, len(f.Resources))
	for _, r := range f.Resources {
		byKey[r.Key] = r
	}
	budget := maxSpanPoints
	for _, sp := range f.Spans {
		r := byKey[sp.Resource]
		if r == nil {
			continue
		}
		from, to := float64(sp.Start.Add(-pad).UnixNano())/1e9, float64(sp.End.Add(pad).UnixNano())/1e9
		for _, m := range r.Metrics {
			var in []Point
			for _, p := range m.Points {
				if p[0] >= from && p[0] <= to {
					in = append(in, p)
				}
			}
			if len(in) == 0 {
				continue
			}
			cut := MetricSeries{Name: m.Name, Labels: m.Labels, Points: in}
			cut.summarise()
			if len(in) > budget {
				cut.Points, budgetHit = nil, true
			} else {
				budget -= len(in)
			}
			sp.Metrics = append(sp.Metrics, cut)
		}
	}
	return budgetHit
}

// attachLogs puts each line on the span it names and the rest in f.Logs.Unmatched.
func attachLogs(f *Fused, lines []LogEntry, truncated bool) {
	byID := make(map[string]*Span, len(f.Spans))
	for _, sp := range f.Spans {
		byID[sp.SpanID] = sp
	}
	f.Logs.Total, f.Logs.Truncated = len(lines), truncated
	for _, l := range lines {
		if sp := byID[normID(l.SpanID, 16)]; sp != nil && l.SpanID != "" {
			sp.Logs = append(sp.Logs, l)
			f.Logs.Matched++
			continue
		}
		f.Logs.Unmatched = append(f.Logs.Unmatched, l)
	}
}

// resourceMetrics reads the metric series saved for one resource: what the service itself reported under its name
// and namespace, and what was reported about its pod (the infrastructure view, which usually carries no service name).
func (c *Client) resourceMetrics(ctx context.Context, s Scope, r *Resource, opts FuseOptions, window TimeRange, step time.Duration) ([]MetricSeries, bool, error) {
	var filters []MetricFilter
	views := opts.MetricViews.orDefault()
	if views.App && r.Service != "" {
		filters = append(filters, MetricFilter{NameRegex: opts.MetricRegex, Service: r.Service, Namespace: r.Namespace, Cluster: r.Cluster})
	}
	if views.Pod && r.Pod != "" {
		filters = append(filters, MetricFilter{NameRegex: opts.MetricRegex, Pod: r.Pod, Namespace: r.Namespace, Cluster: r.Cluster})
	}
	if views.Node && r.Node != "" {
		filters = append(filters, MetricFilter{NameRegex: opts.MetricRegex, Node: r.Node, Cluster: r.Cluster, NoPod: true})
	}
	var out []MetricSeries
	seen := map[string]bool{}
	truncated := false
	for _, f := range filters {
		series, trunc, err := c.MetricRange(ctx, s, f, window, step, opts.MaxSeries)
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || trunc
		for _, m := range series {
			k := m.Name + "{" + labelKey(m.Labels) + "}"
			if !seen[k] {
				seen[k] = true
				out = append(out, m)
			}
		}
	}
	if len(out) > opts.MaxSeries {
		out, truncated = out[:opts.MaxSeries], true
	}
	return out, truncated, nil
}

// Application is a service FUSION has data for, and which signal types.
type Application struct {
	Name    string   `json:"name"`
	Signals []string `json:"signals"`
}

// Applications lists the services that have telemetry in the range, each with the signal types it has. Signals the
// Scope does not include are not looked at; a store that cannot be reached is reported in the returned sources.
func (c *Client) Applications(ctx context.Context, s Scope, tr TimeRange) ([]Application, map[string]string, error) {
	sources := map[string]string{SignalMetrics: SourceNotAllowed, SignalLogs: SourceNotAllowed, SignalTraces: SourceNotAllowed}
	found := map[string]map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	run := func(signal string, list func() ([]string, error)) {
		if !s.Allows(signal) {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			names, err := list()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				sources[signal] = sourceState(err)
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			sources[signal] = SourceOK
			for _, n := range names {
				if found[n] == nil {
					found[n] = map[string]bool{}
				}
				found[n][signal] = true
			}
		}()
	}
	run(SignalMetrics, func() ([]string, error) { return c.metricServices(ctx, s, tr) })
	run(SignalLogs, func() ([]string, error) { return c.logServices(ctx, s, tr) })
	run(SignalTraces, func() ([]string, error) { return c.traceServices(ctx, s, tr) })
	wg.Wait()
	if err := callerGone(ctx); err != nil {
		return nil, nil, err
	}
	ok := false
	for _, v := range sources {
		ok = ok || v == SourceOK
	}
	if !ok && firstErr != nil {
		return nil, nil, firstErr // nothing could be read at all
	}
	var apps []Application
	for name, sigs := range found {
		a := Application{Name: name}
		for _, sig := range Signals {
			if sigs[sig] {
				a.Signals = append(a.Signals, sig)
			}
		}
		apps = append(apps, a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, sources, nil
}

// metricServices lists the service names that have series in the range (inside the Scope).
func (c *Client) metricServices(ctx context.Context, s Scope, tr TimeRange) ([]string, error) {
	m, err := MetricFilter{}.matchers(s)
	if err != nil {
		return nil, err
	}
	m = append(m, lblService+`=~".+"`)
	sel := "{" + strings.Join(m, ",") + "}"
	data, err := c.prom(ctx, "/api/v1/label/"+lblService+"/values", url.Values{"match[]": {sel}, "start": {unixFloat(tr.From)}, "end": {unixFloat(tr.To)}})
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, errf(http.StatusBadGateway, "%s answered with something unexpected", storeProm)
	}
	return names, nil
}

// Overview is one application at a glance: which signals it has and a few of the most recent and most interesting
// things in each, ready to follow into a fused trace.
type Overview struct {
	// ID and Services are set for an Ikhnos application: its id and the service names its telemetry may carry.
	ID          string            `json:"id,omitempty"`
	Services    []string          `json:"services,omitempty"`
	Name        string            `json:"name"`
	Signals     []string          `json:"signals"`
	Traces      []TraceSummary    `json:"traces"`      // the most recent
	ErrorTraces []TraceSummary    `json:"errorTraces"` // the most recent with an error span
	ErrorLogs   []LogEntry        `json:"errorLogs"`   // the most recent error-level lines
	Metrics     []string          `json:"metrics"`     // metric names it reports
	Sources     map[string]string `json:"sources"`
	Warnings    []string          `json:"warnings,omitempty"`
}

// ServiceOverview reads the overview of one service.
func (c *Client) ServiceOverview(ctx context.Context, s Scope, name string, tr TimeRange) (*Overview, error) {
	if err := checkValue("service", name); err != nil {
		return nil, err
	}
	return c.overview(ctx, s, name, name, tr)
}

// ApplicationOverview reads the overview of one Ikhnos application: the same things as for a service, over all of its
// services (the focus the Scope gets from the application).
func (c *Client) ApplicationOverview(ctx context.Context, s Scope, g *AppGroup, tr TimeRange) (*Overview, error) {
	fs, err := s.FocusOn(g)
	if err != nil {
		return nil, err
	}
	o, err := c.overview(ctx, fs, "", g.Name, tr)
	if err != nil {
		return nil, err
	}
	o.ID, o.Services = g.ID, fs.FocusServices
	return o, nil
}

// overview reads what a service (or, with no service named, whatever the Scope's focus selects) has. label is what the
// answer is called.
func (c *Client) overview(ctx context.Context, s Scope, name, label string, tr TimeRange) (*Overview, error) {
	o := &Overview{Name: label, Sources: map[string]string{SignalMetrics: SourceNotAllowed, SignalLogs: SourceNotAllowed, SignalTraces: SourceNotAllowed},
		Traces: []TraceSummary{}, ErrorTraces: []TraceSummary{}, ErrorLogs: []LogEntry{}, Metrics: []string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	has := map[string]bool{}
	run := func(signal string, do func() (bool, error)) {
		if !s.Allows(signal) {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			present, err := do()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				o.Sources[signal] = sourceState(err)
				o.Warnings = append(o.Warnings, fmt.Sprintf("%s: %v", signal, err))
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			o.Sources[signal] = SourceOK
			has[signal] = present
		}()
	}
	run(SignalTraces, func() (bool, error) {
		recent, err := c.SearchTraces(ctx, s, TraceFilter{Service: name}, tr, 10)
		if err != nil {
			return false, err
		}
		failed, err := c.SearchTraces(ctx, s, TraceFilter{Service: name, Status: "error"}, tr, 5)
		if err != nil {
			return false, err
		}
		mu.Lock()
		o.Traces, o.ErrorTraces = recent, failed
		mu.Unlock()
		return len(recent) > 0, nil
	})
	run(SignalLogs, func() (bool, error) {
		lines, _, err := c.Logs(ctx, s, LogFilter{Service: name, Severity: "error", Backward: true}, tr, 10)
		if err != nil {
			return false, err
		}
		one, _, err := c.Logs(ctx, s, LogFilter{Service: name, Backward: true}, tr, 1)
		if err != nil {
			return false, err
		}
		mu.Lock()
		o.ErrorLogs = lines
		mu.Unlock()
		return len(one) > 0, nil
	})
	run(SignalMetrics, func() (bool, error) {
		names, err := c.MetricNames(ctx, s, MetricFilter{Service: name}, tr, 100)
		if err != nil {
			return false, err
		}
		mu.Lock()
		o.Metrics = names
		mu.Unlock()
		return len(names) > 0, nil
	})
	wg.Wait()
	if err := callerGone(ctx); err != nil {
		return nil, err
	}
	allFailed := firstErr != nil
	for _, v := range o.Sources {
		if v == SourceOK {
			allFailed = false
		}
	}
	if allFailed {
		return nil, firstErr
	}
	for _, sig := range Signals {
		if has[sig] {
			o.Signals = append(o.Signals, sig)
		}
	}
	if len(o.Signals) == 0 && firstErr == nil {
		return nil, errf(http.StatusNotFound, "no telemetry for %q in this range", label)
	}
	return o, nil
}
