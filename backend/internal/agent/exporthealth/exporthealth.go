// Package exporthealth tells whether each telemetry destination is actually receiving data.
//
// The telemetry collectors (the chart's host DaemonSet and cluster Deployment) count what they send, per
// exporter and signal type, and serve those counters on their own pod address (the chart's telemetry.health).
// This package reads them from every collector pod on a timer, keeps the order of what it saw, and turns the
// counters into a state per route: waiting, exporting, silent or failing. It reads counters only. It never
// sees telemetry, and what it reads is not sent anywhere but in the agent's own Diagnostics.
//
// Why here and not on the server: only whoever reads the counters sees every reading in order against one
// clock, which is what "did anything go out recently" needs. A counter on its own says how much went out
// since the pod started, never when.
package exporthealth

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	continuumv1 "continuum/gen/continuumv1"
)

const (
	// DefaultEvery is how often the counters are read.
	DefaultEvery = 30 * time.Second
	// DefaultWindow is how long ago something may have gone out and still count as "recently": a few
	// collection intervals, since the slowest signal here is read every minute.
	DefaultWindow = 3 * time.Minute

	defaultTimeout = 3 * time.Second
	// maxBody caps what is read from one collector: its own self-metrics are a few hundred kilobytes at most.
	maxBody = 16 << 20
	// maxPods caps the pods read from one target, and parallel reads the requests in flight at once, so a
	// large cluster costs a bounded amount of the agent's time and memory.
	maxPods  = 512
	parallel = 8
)

// Config is what a Monitor needs. Only Targets is required.
type Config struct {
	// Targets are host:port names, each resolved to every address it has (the chart gives one headless
	// Service per collector, which resolves to one address per pod) and each address read at /metrics.
	Targets []string
	// Every is the time between readings (default DefaultEvery); Window how recent "recently" is (default
	// DefaultWindow); Timeout the limit on one request (default 3 s).
	Every, Window, Timeout time.Duration
	// Lookup and Get replace name resolution and the HTTP read (tests). Now replaces the clock (tests).
	Lookup func(ctx context.Context, host string) ([]string, error)
	Get    func(ctx context.Context, url string) ([]byte, error)
	Now    func() time.Time
	Log    *slog.Logger
}

// ParseTargets splits a comma-separated list of host:port names, dropping blanks and repeats.
func ParseTargets(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

type route struct{ exporter, signal string }

type counts struct{ sent, failed uint64 }

// routeState is what is known about one route: the latest sum of its counters over the pods read, and when
// each was last seen to grow.
type routeState struct {
	counts
	lastSent, lastFailed time.Time
	// ready is set once a reading had an earlier one of the same pod to compare with. Before that a counter
	// above zero says only that something went out at some time, so no state is claimed from it.
	ready bool
}

// Monitor reads the collectors' counters. Report is safe to call from any goroutine.
type Monitor struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	prev      map[string]map[route]counts // pod address -> its counters at the last reading
	routes    map[route]*routeState
	scrapedAt time.Time
	reached   int
	failed    int
	lastErr   string
}

// New returns a Monitor; call Run to start it.
func New(cfg Config) *Monitor {
	if cfg.Every <= 0 {
		cfg.Every = DefaultEvery
	}
	if cfg.Window <= 0 {
		cfg.Window = DefaultWindow
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	m := &Monitor{cfg: cfg, log: cfg.Log, prev: map[string]map[route]counts{}, routes: map[route]*routeState{}}
	if cfg.Lookup == nil {
		m.cfg.Lookup = func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	if cfg.Get == nil {
		client := &http.Client{Timeout: cfg.Timeout}
		m.cfg.Get = func(ctx context.Context, url string) ([]byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			return io.ReadAll(io.LimitReader(resp.Body, maxBody))
		}
	}
	return m
}

// Run reads the counters at once and then every Config.Every until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	m.Scrape(ctx)
	t := time.NewTicker(m.cfg.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Scrape(ctx)
		}
	}
}

// Scrape does one reading of every target.
func (m *Monitor) Scrape(ctx context.Context) {
	type read struct {
		addr   string
		routes map[route]counts
		err    error
	}
	var (
		reads    []read
		resolved = map[string]bool{}
		failed   int
		lastErr  string
	)
	note := func(err error) {
		failed++
		lastErr = clip(err.Error(), 200)
	}
	var addrs []string
	for _, target := range m.cfg.Targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			note(fmt.Errorf("target %q: %w", target, err))
			continue
		}
		ips := []string{host}
		if net.ParseIP(host) == nil {
			lctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
			ips, err = m.cfg.Lookup(lctx, host)
			cancel()
			if err != nil {
				note(fmt.Errorf("find %s: %w", host, err))
				continue
			}
			if len(ips) == 0 {
				note(fmt.Errorf("find %s: no pods behind it", host))
				continue
			}
		}
		sort.Strings(ips)
		if len(ips) > maxPods {
			ips = ips[:maxPods]
		}
		for _, ip := range ips {
			a := net.JoinHostPort(ip, port)
			resolved[a] = true
			addrs = append(addrs, a)
		}
	}

	var (
		wg  sync.WaitGroup
		rmu sync.Mutex
		sem = make(chan struct{}, parallel)
	)
	for _, a := range addrs {
		wg.Add(1)
		sem <- struct{}{}
		go func(a string) {
			defer wg.Done()
			defer func() { <-sem }()
			rctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
			defer cancel()
			body, err := m.cfg.Get(rctx, "http://"+a+"/metrics")
			var rs map[route]counts
			if err == nil {
				rs, err = parse(strings.NewReader(string(body)))
			}
			rmu.Lock()
			reads = append(reads, read{addr: a, routes: rs, err: err})
			rmu.Unlock()
		}(a)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // shutting down: a half-finished reading would only look like failures
	}
	sort.Slice(reads, func(i, j int) bool { return reads[i].addr < reads[j].addr })

	now := m.cfg.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	reached := 0
	agg := map[route]counts{}
	seenNow := map[string]map[route]counts{}
	for _, r := range reads {
		if r.err != nil {
			note(fmt.Errorf("read %s: %w", r.addr, r.err))
			continue
		}
		reached++
		seenNow[r.addr] = r.routes
		before, known := m.prev[r.addr]
		for rt, c := range r.routes {
			a := agg[rt]
			a.sent += c.sent
			a.failed += c.failed
			agg[rt] = a
			st := m.routes[rt]
			if st == nil {
				st = &routeState{}
				m.routes[rt] = st
			}
			if !known {
				continue // the first reading of a pod is only a baseline to measure growth from
			}
			st.ready = true
			p := before[rt] // a series that is new since the last reading counts from zero
			if grew(p.sent, c.sent) {
				st.lastSent = now
			}
			if grew(p.failed, c.failed) {
				st.lastFailed = now
			}
		}
	}
	for rt, c := range agg {
		m.routes[rt].counts = c
	}
	// A pod that could not be read this time keeps its baseline while it still resolves; one that is gone does not.
	for a, c := range m.prev {
		if _, ok := seenNow[a]; !ok && resolved[a] {
			seenNow[a] = c
		}
	}
	m.prev = seenNow
	m.scrapedAt, m.reached, m.failed, m.lastErr = now, reached, failed, lastErr
}

// grew reports whether a counter went up since the last reading. A counter that went down was reset by a
// restart, and whatever it holds now is growth.
func grew(before, now uint64) bool {
	if now < before {
		return now > 0
	}
	return now > before
}

// Report is what the agent says about export health, or nil before the first reading has finished.
func (m *Monitor) Report() *continuumv1.ExportHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scrapedAt.IsZero() {
		return nil
	}
	now := m.cfg.Now()
	out := &continuumv1.ExportHealth{
		ScrapedAt: timestamppb.New(m.scrapedAt), PodsReached: uint32(m.reached), PodsFailed: uint32(m.failed), LastError: m.lastErr,
	}
	keys := make([]route, 0, len(m.routes))
	for k := range m.routes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].signal != keys[j].signal {
			return keys[i].signal < keys[j].signal
		}
		return keys[i].exporter < keys[j].exporter
	})
	for _, k := range keys {
		st := m.routes[k]
		r := &continuumv1.ExportRouteHealth{Exporter: k.exporter, Signal: k.signal, State: m.state(st, now), Sent: st.sent, Failed: st.failed}
		if !st.lastSent.IsZero() {
			r.LastSentAt = timestamppb.New(st.lastSent)
		}
		if !st.lastFailed.IsZero() {
			r.LastFailedAt = timestamppb.New(st.lastFailed)
		}
		out.Routes = append(out.Routes, r)
	}
	return out
}

func (m *Monitor) state(st *routeState, now time.Time) continuumv1.ExportRouteHealth_State {
	recent := func(t time.Time) bool { return !t.IsZero() && now.Sub(t) <= m.cfg.Window }
	switch {
	case recent(st.lastFailed) && !st.lastFailed.Before(st.lastSent):
		return continuumv1.ExportRouteHealth_FAILING
	case recent(st.lastSent):
		return continuumv1.ExportRouteHealth_EXPORTING
	case !st.ready:
		return continuumv1.ExportRouteHealth_WAITING
	case st.sent > 0 || st.failed > 0:
		return continuumv1.ExportRouteHealth_SILENT
	}
	return continuumv1.ExportRouteHealth_WAITING
}

// parse reads the collector's Prometheus text for the exporter counters. It accepts the counter names with
// or without the "_total" suffix (which collector releases have disagreed on), sums series that differ only in
// labels this package does not use (the address a gRPC exporter talks to, a URL path), and ignores everything
// else, including the "debug" exporter, which writes to the collector's own log and is nobody's destination.
func parse(r io.Reader) (map[route]counts, error) {
	out := map[route]counts{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "otelcol_exporter_") {
			continue
		}
		name, labels, value, ok := splitSample(line)
		if !ok {
			continue
		}
		name = strings.TrimSuffix(strings.TrimPrefix(name, "otelcol_exporter_"), "_total")
		var failedCounter bool
		switch {
		case strings.HasPrefix(name, "send_failed_"):
			failedCounter, name = true, strings.TrimPrefix(name, "send_failed_")
		case strings.HasPrefix(name, "sent_"):
			name = strings.TrimPrefix(name, "sent_")
		default:
			continue
		}
		var signal string
		switch name {
		case "spans":
			signal = "traces"
		case "metric_points":
			signal = "metrics"
		case "log_records":
			signal = "logs"
		default:
			continue
		}
		exp := labels["exporter"]
		if exp == "" || exp == "debug" || strings.HasPrefix(exp, "debug/") {
			continue
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}
		k := route{exp, signal}
		c := out[k]
		if failedCounter {
			c.failed += uint64(value)
		} else {
			c.sent += uint64(value)
		}
		out[k] = c
	}
	return out, sc.Err()
}

// splitSample splits "name{k=\"v\",...} 12 [timestamp]" into its parts. Label values are quoted and may hold
// commas, braces and escaped quotes, so they are read character by character rather than split on punctuation.
func splitSample(line string) (name string, labels map[string]string, value float64, ok bool) {
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		return
	}
	name, rest := line[:i], line[i:]
	labels = map[string]string{}
	if rest[0] == '{' {
		j := 1
		for j < len(rest) && rest[j] != '}' {
			eq := strings.IndexByte(rest[j:], '=')
			if eq < 0 || j+eq+1 >= len(rest) || rest[j+eq+1] != '"' {
				return "", nil, 0, false
			}
			key := strings.TrimSpace(rest[j : j+eq])
			j += eq + 2
			var v strings.Builder
			for j < len(rest) && rest[j] != '"' {
				if rest[j] == '\\' && j+1 < len(rest) {
					j++
					switch rest[j] {
					case 'n':
						v.WriteByte('\n')
					default:
						v.WriteByte(rest[j])
					}
				} else {
					v.WriteByte(rest[j])
				}
				j++
			}
			if j >= len(rest) {
				return "", nil, 0, false
			}
			j++ // the closing quote
			labels[key] = v.String()
			for j < len(rest) && (rest[j] == ',' || rest[j] == ' ') {
				j++
			}
		}
		if j >= len(rest) {
			return "", nil, 0, false
		}
		rest = rest[j+1:]
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return "", nil, 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return "", nil, 0, false
	}
	return name, labels, v, true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
