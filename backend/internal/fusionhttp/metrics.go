package fusionhttp

import (
	"net/http"
	"net/url"

	"ikhnos/internal/fusionapi"
	"ikhnos/internal/httpkit"
)

import ()

func metricFilter(q url.Values) fusionapi.MetricFilter {
	cats, _ := fusionapi.ParseCategories(q.Get("category")) // already checked by the route (Op.CheckQuery)
	return fusionapi.MetricFilter{Name: q.Get("name"), NameRegex: q.Get("metric"), Service: q.Get("service"), Namespace: q.Get("namespace"),
		Pod: q.Get("pod"), Node: q.Get("node"), Cluster: q.Get("cluster"), Categories: cats}
}

func (g *Guard) MetricNames(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(r.URL.Query().Get("limit"), 500, 5000)
	if err != nil {
		g.Err(w, err)
		return
	}
	scope, err := g.Scope(r, who)
	if err != nil {
		g.Err(w, err)
		return
	}
	names, truncated, err := c.MetricNames(r.Context(), scope, metricFilter(r.URL.Query()), tr, limit)
	if err != nil {
		g.Err(w, err)
		return
	}
	cat := make(map[string]string, len(names))
	for _, n := range names {
		cat[n] = fusionapi.MetricCategory(n)
	}
	httpkit.WriteJSON(w, 200, metricNamesDoc{Names: orEmpty(names), Categories: cat, Truncated: truncated, Limit: limit, From: tr.From, To: tr.To})
}

func (g *Guard) MetricSeries(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(r.URL.Query().Get("limit"), 200, 2000)
	if err != nil {
		g.Err(w, err)
		return
	}
	scope, err := g.Scope(r, who)
	if err != nil {
		g.Err(w, err)
		return
	}
	series, truncated, err := c.Series(r.Context(), scope, metricFilter(r.URL.Query()), tr, limit)
	if err != nil {
		g.Err(w, err)
		return
	}
	if series == nil {
		series = []map[string]string{}
	}
	httpkit.WriteJSON(w, 200, metricSeriesDoc{Series: series, Truncated: truncated, Limit: limit, From: tr.From, To: tr.To})
}

func (g *Guard) MetricRange(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	q := r.URL.Query()
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	limit, err := fusionapi.Limit(q.Get("limit"), 20, 100)
	if err != nil {
		g.Err(w, err)
		return
	}
	req, err := fusionapi.DurationParam(q.Get("step"))
	if err != nil {
		g.Err(w, err)
		return
	}
	step, err := fusionapi.ChooseStep(tr, req, 120)
	if err != nil {
		g.Err(w, err)
		return
	}
	scope, err := g.Scope(r, who)
	if err != nil {
		g.Err(w, err)
		return
	}
	f := metricFilter(q)
	if err := f.NeedSelective(scope); err != nil {
		g.Err(w, err)
		return
	}
	series, truncated, err := c.MetricRange(r.Context(), scope, f, tr, step, limit)
	if err != nil {
		g.Err(w, err)
		return
	}
	if series == nil {
		series = []fusionapi.MetricSeries{}
	}
	httpkit.WriteJSON(w, 200, metricRangeDoc{Series: series, Truncated: truncated, Limit: limit, StepSeconds: step.Seconds(), From: tr.From, To: tr.To})
}

// MetricRaw is PromQL as written, answered the way Prometheus itself answers.
func (g *Guard) MetricRaw(endpoint string) Handler {
	return func(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
		data, err := c.RawMetricQuery(r.Context(), who.Scope, endpoint, r.URL.Query())
		if err != nil {
			g.Err(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":`))
		_, _ = w.Write(data)
		_, _ = w.Write([]byte(`}`))
	}
}
