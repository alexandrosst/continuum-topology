package fusionapi

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The caller's own PromQL in a fused read.
//
// A query is given as promql=<name>=<expression> (repeat the parameter for several). The expression may name the
// resource it is read for with ${service}, ${namespace}, ${pod}, ${node} and ${cluster}, so one query serves every
// service of a trace: such a query is evaluated once per resource (and once per distinct expression, so two pods of one
// service that the query tells apart only by ${service} cost one read) and its series sit on that resource. A query
// that names none of them is evaluated once for the whole trace and sits on the fused object. ${trace_id} and ${range}
// (the length of the window, in seconds, as a PromQL duration) are available to both.
//
// Substitution never lets a value change the shape of the query: a value that holds a quote, a backslash, a brace or
// a control character is not substituted (the resource is skipped and the answer says so), and ${service:re} gives the
// value escaped for use inside a regular expression.

// Names of the sources a query adds to Fused.Sources.
const SourcePromQL = "promql"

const (
	maxPromQueries   = 5
	maxPromQueryLen  = 2048
	maxPromRuns      = 60 // distinct expressions evaluated by one fused read
	maxPromValueLen  = 253
	promQueryNameMax = 32
)

var promNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// The variables an expression may use.
var (
	promResourceVars = []string{"service", "namespace", "pod", "node", "cluster"}
	promOtherVars    = []string{"trace_id", "range"}
)

// PromQuery is one query the caller asked to have in the fused object.
type PromQuery struct {
	Name string
	Expr string

	tpl         []tplPart
	perResource bool
}

type tplPart struct {
	lit string // literal text, when v is empty
	v   string // a variable name
	re  bool   // ${v:re}
}

// QueryResult is what one of the caller's queries returned: on a resource for the resource's own expansion of it, on the
// fused object for a query that is not about one resource, on a span cut to the span's time.
type QueryResult struct {
	Name string `json:"name"`
	// Query is the expression as it was evaluated, with the variables replaced (not repeated on spans).
	Query     string         `json:"query,omitempty"`
	Series    []MetricSeries `json:"series"`
	Truncated bool           `json:"truncated,omitempty"`
	// Error says why this query has no series; the others are not affected.
	Error string `json:"error,omitempty"`
}

// ParsePromQueries reads the values of the promql parameter.
func ParsePromQueries(vals []string) ([]PromQuery, error) {
	var out []PromQuery
	seen := map[string]bool{}
	for _, v := range vals {
		if strings.TrimSpace(v) == "" {
			continue
		}
		name, expr, ok := strings.Cut(v, "=")
		name, expr = strings.TrimSpace(name), strings.TrimSpace(expr)
		if !ok || !promNameRe.MatchString(name) || len(name) > promQueryNameMax || expr == "" || strings.HasPrefix(expr, "=") {
			return nil, badRequest("promql takes <name>=<expression>, with a name of letters, digits and underscores (for example promql=errors=sum(rate(http_errors_total{service_name=\"${service}\"}[1m])))")
		}
		if seen[name] {
			return nil, badRequest("promql: the name %q is used twice", name)
		}
		if len(expr) > maxPromQueryLen {
			return nil, badRequest("promql %s: at most %d characters", name, maxPromQueryLen)
		}
		tpl, err := parseTemplate(expr)
		if err != nil {
			return nil, badRequest("promql %s: %v", name, err)
		}
		q := PromQuery{Name: name, Expr: expr, tpl: tpl}
		for _, p := range tpl {
			for _, rv := range promResourceVars {
				if p.v == rv {
					q.perResource = true
				}
			}
		}
		seen[name] = true
		out = append(out, q)
		if len(out) > maxPromQueries {
			return nil, badRequest("promql: at most %d queries per read", maxPromQueries)
		}
	}
	return out, nil
}

func parseTemplate(expr string) ([]tplPart, error) {
	var parts []tplPart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, tplPart{lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(expr); {
		switch {
		case strings.HasPrefix(expr[i:], "$${"): // an escaped ${
			lit.WriteString("${")
			i += 3
		case strings.HasPrefix(expr[i:], "${"):
			end := strings.IndexByte(expr[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("a ${ is not closed (write $${ for a literal ${)")
			}
			name, mod, _ := strings.Cut(expr[i+2:i+end], ":")
			p := tplPart{v: strings.TrimSpace(name)}
			switch strings.TrimSpace(mod) {
			case "":
			case "re":
				p.re = true
			default:
				return nil, fmt.Errorf("${%s}: the only modifier is :re", name)
			}
			if !isPromVar(p.v) {
				return nil, fmt.Errorf("${%s} is not a variable; use %s", name, strings.Join(append(append([]string{}, promResourceVars...), promOtherVars...), ", "))
			}
			flush()
			parts = append(parts, p)
			i += end + 1
		default:
			lit.WriteByte(expr[i])
			i++
		}
	}
	flush()
	return parts, nil
}

func isPromVar(n string) bool {
	for _, v := range promResourceVars {
		if v == n {
			return true
		}
	}
	for _, v := range promOtherVars {
		if v == n {
			return true
		}
	}
	return false
}

// safePromValue says whether a value can stand inside a PromQL string without changing the query's shape.
func safePromValue(v string) bool {
	if v == "" || len(v) > maxPromValueLen {
		return false
	}
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		if strings.ContainsRune("_.:/@+-=, ", r) {
			continue
		}
		return false
	}
	return true
}

// expand fills the variables in. skip says why this expansion cannot be made (a variable has no value, or an unsafe one).
func (q *PromQuery) expand(vars map[string]string) (expr, skip string) {
	var b strings.Builder
	for _, p := range q.tpl {
		if p.v == "" {
			b.WriteString(p.lit)
			continue
		}
		val := vars[p.v]
		if val == "" {
			return "", "no " + strings.ReplaceAll(p.v, "_", " ")
		}
		if !safePromValue(val) {
			return "", "its " + strings.ReplaceAll(p.v, "_", " ") + " cannot be put into a query safely"
		}
		if p.re { // a regular expression inside a double-quoted PromQL string: each backslash is written twice
			val = strings.ReplaceAll(regexp.QuoteMeta(val), `\`, `\\`)
		}
		b.WriteString(val)
	}
	return b.String(), ""
}

func resourceVars(r *Resource) map[string]string {
	return map[string]string{"service": r.Service, "namespace": r.Namespace, "pod": r.Pod, "node": r.Node, "cluster": r.Cluster}
}

// promRange evaluates one expression as a range query. Only a Scope with no namespace or cluster limit may do this.
func (c *Client) promRange(ctx context.Context, s Scope, expr string, w TimeRange, step time.Duration, maxSeries int) ([]MetricSeries, bool, error) {
	if err := s.needSignal(SignalMetrics); err != nil {
		return nil, false, err
	}
	if err := s.needUnrestricted("a PromQL query"); err != nil {
		return nil, false, err
	}
	data, err := c.prom(ctx, "/api/v1/query_range", url.Values{
		"query": {expr}, "start": {unixFloat(w.From)}, "end": {unixFloat(w.To)}, "step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)}, "limit": {strconv.Itoa(maxSeries + 1)},
	})
	if err != nil {
		return nil, false, err
	}
	return decodeMatrix(data, s, maxSeries)
}

type promRun struct {
	series    []MetricSeries
	truncated bool
	err       error
}

// fusePromQL evaluates the caller's queries for a trace. Queries about a resource are put on it (looked is the resources
// worth a lookup); the others are returned. The warnings say what was skipped; firstErr is the first store failure.
func (c *Client) fusePromQL(ctx context.Context, s Scope, tr *Trace, looked []*Resource, opts FuseOptions, w TimeRange, step time.Duration) (global []QueryResult, warns []string, firstErr error) {
	rangeVar := strconv.Itoa(int(math.Ceil(w.To.Sub(w.From).Seconds()))) + "s"
	common := map[string]string{"trace_id": tr.TraceID, "range": rangeVar}

	type slot struct {
		q    *PromQuery
		r    *Resource // nil: not about a resource
		expr string
	}
	var slots []slot
	runs := map[string]*promRun{}
	var order []string
	skipped := map[string][]string{} // query name -> why, for the warning
	over := false

	add := func(q *PromQuery, r *Resource, vars map[string]string) {
		expr, skip := q.expand(vars)
		if skip != "" {
			label := "the trace"
			if r != nil {
				label = r.Key
			}
			skipped[q.Name] = append(skipped[q.Name], label+": "+skip)
			return
		}
		if _, ok := runs[expr]; !ok {
			if len(runs) >= maxPromRuns {
				over = true
				return
			}
			runs[expr] = &promRun{}
			order = append(order, expr)
		}
		slots = append(slots, slot{q, r, expr})
	}
	services := map[string]bool{}
	for _, n := range opts.PromQLServices {
		services[n] = true
	}
	for i := range opts.PromQL {
		q := &opts.PromQL[i]
		if !q.perResource {
			add(q, nil, common)
			continue
		}
		for _, r := range looked {
			if len(services) > 0 && !services[r.Service] {
				continue
			}
			vars := resourceVars(r)
			for k, v := range common {
				vars[k] = v
			}
			add(q, r, vars)
		}
	}
	if over {
		warns = append(warns, fmt.Sprintf("promql: this read evaluates at most %d distinct queries; the rest were left out", maxPromRuns))
	}
	names := make([]string, 0, len(skipped))
	for n := range skipped {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		list := skipped[n]
		if len(list) > 3 {
			list = append(list[:3:3], fmt.Sprintf("and %d more", len(skipped[n])-3))
		}
		warns = append(warns, fmt.Sprintf("promql %s: not evaluated for %s", n, strings.Join(list, "; ")))
	}

	sem := make(chan struct{}, fuseConcurrency)
	var wg sync.WaitGroup
	for _, expr := range order {
		run := runs[expr]
		expr := expr
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				run.err = ctx.Err()
				return
			}
			run.series, run.truncated, run.err = c.promRange(ctx, s, expr, w, step, opts.MaxSeries)
		}()
	}
	wg.Wait()

	for _, sl := range slots {
		run := runs[sl.expr]
		res := QueryResult{Name: sl.q.Name, Query: sl.expr, Series: run.series, Truncated: run.truncated}
		if run.err != nil {
			res.Error = run.err.Error()
			if firstErr == nil {
				firstErr = run.err
			}
		}
		if res.Series == nil {
			res.Series = []MetricSeries{}
		}
		if sl.r == nil {
			global = append(global, res)
		} else {
			sl.r.Queries = append(sl.r.Queries, res)
		}
	}
	return global, warns, firstErr
}

// attachSpanQueries gives each span the points of its resource's query results that fall inside the span's own time
// widened by pad either side (the same cut metrics get). It reports whether the point budget ran out.
func attachSpanQueries(f *Fused, pad time.Duration) (budgetHit bool) {
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
		for _, qr := range r.Queries {
			cut := QueryResult{Name: qr.Name, Error: qr.Error, Series: []MetricSeries{}}
			for _, m := range qr.Series {
				var in []Point
				for _, p := range m.Points {
					if p[0] >= from && p[0] <= to {
						in = append(in, p)
					}
				}
				if len(in) == 0 {
					continue
				}
				ms := MetricSeries{Name: m.Name, Labels: m.Labels, Points: in}
				ms.summarise()
				if len(in) > budget {
					ms.Points, budgetHit = nil, true
				} else {
					budget -= len(in)
				}
				cut.Series = append(cut.Series, ms)
			}
			if len(cut.Series) > 0 || cut.Error != "" {
				sp.Queries = append(sp.Queries, cut)
			}
		}
	}
	return budgetHit
}
