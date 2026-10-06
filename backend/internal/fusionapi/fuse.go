package fusionapi

import (
	"context"
	"encoding/json"
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
}

const (
	defaultPad       = 2 * time.Minute
	maxPad           = time.Hour
	defaultMaxLogs   = 500
	hardMaxLogs      = 2000
	defaultMaxSeries = 15
	hardMaxSeries    = 100
	fuseConcurrency  = 4
)

func (o *FuseOptions) defaults() error {
	if o.Pad == 0 {
		o.Pad = defaultPad
	}
	if o.Pad < 0 || o.Pad > maxPad {
		return badRequest("pad must be between 0 and %s", maxPad)
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
}

// Fused is a trace with the logs and metrics saved around it: each span carries the log lines written under its id,
// and each resource the metric series of the same service, namespace and pod over the trace's time. A part that could
// not be read is named in Sources and Warnings; the trace itself always comes back or the whole read fails.
type Fused struct {
	*Trace
	Logs     FusedLogs         `json:"logs"`
	Sources  map[string]string `json:"sources"`
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
	tr, err := c.Trace(ctx, s, id)
	if err != nil {
		return nil, err
	}
	f := &Fused{Trace: tr, Sources: map[string]string{SignalTraces: SourceOK, SignalLogs: SourceNotRequested, SignalMetrics: SourceNotRequested}}
	window := TimeRange{From: tr.Start.Add(-opts.Pad), To: tr.End.Add(opts.Pad)}
	if !window.From.Before(window.To) {
		window.To = window.From.Add(time.Second)
	}

	var mu sync.Mutex
	warn := func(signal string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if IsUnavailable(err) {
			f.Sources[signal] = SourceUnavailable
		} else {
			f.Sources[signal] = SourceError
		}
		f.Warnings = append(f.Warnings, fmt.Sprintf("%s: %v", signal, err))
	}
	var wg sync.WaitGroup

	switch {
	case !opts.Logs:
	case !s.Allows(SignalLogs):
		f.Sources[SignalLogs] = SourceNotAllowed
	default:
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines, truncated, err := c.Logs(ctx, s, LogFilter{TraceID: tr.TraceID}, window, opts.MaxLogs)
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
	case !opts.Metrics:
	case !s.Allows(SignalMetrics):
		f.Sources[SignalMetrics] = SourceNotAllowed
	default:
		f.Sources[SignalMetrics] = SourceOK
		step, err := ChooseStep(window, 0, opts.Points)
		if err != nil {
			return nil, err
		}
		sem := make(chan struct{}, fuseConcurrency)
		for _, r := range tr.Resources {
			r := r
			if r.Service == "" && r.Pod == "" {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
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
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f, nil
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
	if r.Service != "" {
		filters = append(filters, MetricFilter{NameRegex: opts.MetricRegex, Service: r.Service, Namespace: r.Namespace})
	}
	if r.Pod != "" {
		filters = append(filters, MetricFilter{NameRegex: opts.MetricRegex, Pod: r.Pod, Namespace: r.Namespace})
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
				if IsUnavailable(err) {
					sources[signal] = SourceUnavailable
				} else {
					sources[signal] = SourceError
				}
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
	if err := ctx.Err(); err != nil {
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
	Name        string            `json:"name"`
	Signals     []string          `json:"signals"`
	Traces      []TraceSummary    `json:"traces"`      // the most recent
	ErrorTraces []TraceSummary    `json:"errorTraces"` // the most recent with an error span
	ErrorLogs   []LogEntry        `json:"errorLogs"`   // the most recent error-level lines
	Metrics     []string          `json:"metrics"`     // metric names it reports
	Sources     map[string]string `json:"sources"`
	Warnings    []string          `json:"warnings,omitempty"`
}

// ApplicationOverview reads the overview of one service.
func (c *Client) ApplicationOverview(ctx context.Context, s Scope, name string, tr TimeRange) (*Overview, error) {
	if err := checkValue("application", name); err != nil {
		return nil, err
	}
	o := &Overview{Name: name, Sources: map[string]string{SignalMetrics: SourceNotAllowed, SignalLogs: SourceNotAllowed, SignalTraces: SourceNotAllowed},
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
				if IsUnavailable(err) {
					o.Sources[signal] = SourceUnavailable
				} else {
					o.Sources[signal] = SourceError
				}
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
	if err := ctx.Err(); err != nil {
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
		return nil, errf(http.StatusNotFound, "no telemetry for %q in this range", name)
	}
	return o, nil
}
