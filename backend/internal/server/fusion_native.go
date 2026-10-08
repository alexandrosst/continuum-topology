package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"continuum/internal/fusionapi"
)

// The native mirror (internal/fusionapi/mirror.go): Prometheus', Loki's and Tempo's own read endpoints under
// /api/v1/fusion/{prometheus,loki,tempo}/, answered in their own format. Only the routes listed here exist: a write,
// an admin call, a push or a delete is not mirrored, so there is nothing to refuse - it is a 404.

const (
	tagProm  = "Prometheus API"
	tagLoki  = "Loki API"
	tagTempo = "Tempo API"
)

const nativeNote = "\n\nAnswered exactly as %[1]s answers it (its own format and error shape), so a client written for it works unchanged: point it at `/api/v1/fusion/%[2]s` and send the token as a bearer credential. A hand-written query cannot be narrowed to a namespace or cluster, so only an administrator or a token with no such limit may use this. A POST with a form (`application/x-www-form-urlencoded`) is accepted too, for a long query. What the parameters mean is in %[1]s's HTTP API documentation."

// nativeOp is one mirrored route. Path is the backend's own path; the route is served under the store's name.
func (a *Admin) nativeOp(store, tag, name string, path, summary string, pathParams, params []string, post bool, notes map[string]string) fusionOp {
	return fusionOp{Method: "GET", Path: "/" + store + path, Tag: tag, Summary: summary,
		Description: strings.TrimSpace(fmt.Sprintf(nativeNote, name, store)),
		PathParams:  pathParams, Params: params, Notes: notes, Response: "NativeResponse", AlsoPost: post, Handler: a.fusionNative(store, path, pathParams)}
}

// fusionNative forwards one mirrored route to its store.
func (a *Admin) fusionNative(store, template string, pathParams []string) fusionHandler {
	return func(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who fusionCaller) {
		path := template
		for _, n := range pathParams {
			v := r.PathValue(n)
			if err := fusionapi.MirrorSegment(n, v); err != nil {
				a.fusionErr(w, r, err)
				return
			}
			path = strings.Replace(path, "{"+n+"}", url.PathEscape(v), 1)
		}
		resp, err := c.Mirror(r.Context(), who.Scope, store, path, r)
		if err != nil {
			a.fusionErr(w, r, err)
			return
		}
		w.Header().Set("Content-Type", resp.ContentType)
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	}
}

func nativeOps(a *Admin) []fusionOp {
	rng := []string{"start", "end"}
	cat := func(g ...[]string) []string {
		var out []string
		for _, x := range g {
			out = append(out, x...)
		}
		return out
	}
	q := func(s string) map[string]string { return map[string]string{"query": s} }
	lbl := []string{"label"}
	return []fusionOp{
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/query", "Instant query", nil, []string{"query", "time", "timeout"}, true, q("The PromQL expression.")),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/query_range", "Range query", nil, cat([]string{"query"}, rng, []string{"step", "timeout"}), true, q("The PromQL expression.")),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/query_exemplars", "Exemplars of a query", nil, cat([]string{"query"}, rng), true, q("The PromQL selector.")),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/series", "Series that match", nil, cat([]string{"match[]"}, rng, []string{"limit"}), true, nil),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/labels", "Label names", nil, cat([]string{"match[]"}, rng, []string{"limit"}), true, nil),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/label/{label}/values", "Values of a label", lbl, cat([]string{"match[]"}, rng, []string{"limit"}), false, nil),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/metadata", "Metric metadata", nil, []string{"metric", "limit"}, false,
			map[string]string{"metric": "Only this metric (the exact name)."}),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/status/tsdb", "Cardinality statistics", nil, []string{"limit"}, false, nil),
		a.nativeOp("prometheus", tagProm, "Prometheus", "/api/v1/status/buildinfo", "Build information", nil, nil, false, nil),

		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/query", "Instant LogQL query", nil, []string{"query", "time", "limit", "direction"}, true, q("The LogQL expression.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/query_range", "Range LogQL query", nil, cat([]string{"query"}, rng, []string{"since", "limit", "direction", "step", "interval"}), true, q("The LogQL expression.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/labels", "Label names", nil, cat(rng, []string{"since", "query"}), false, q("A stream selector to narrow to (optional).")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/label/{label}/values", "Values of a label", lbl, cat(rng, []string{"since", "query"}), false, q("A stream selector to narrow to (optional).")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/series", "Streams that match", nil, cat([]string{"match[]"}, rng, []string{"since"}), true, nil),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/index/stats", "Size of what matches", nil, cat([]string{"query"}, rng), false, q("A stream selector.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/index/volume", "Volume of what matches", nil, cat([]string{"query"}, rng, []string{"limit"}), false, q("A stream selector.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/index/volume_range", "Volume over time", nil, cat([]string{"query"}, rng, []string{"limit", "step"}), false, q("A stream selector.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/patterns", "Log patterns", nil, cat([]string{"query"}, rng, []string{"step"}), false, q("A stream selector.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/detected_labels", "Labels detected in the logs", nil, cat([]string{"query"}, rng), false, q("A stream selector.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/detected_fields", "Fields detected in the logs", nil, cat([]string{"query"}, rng, []string{"limit"}), false, q("A stream selector.")),
		a.nativeOp("loki", tagLoki, "Loki", "/loki/api/v1/status/buildinfo", "Build information", nil, nil, false, nil),

		a.nativeOp("tempo", tagTempo, "Tempo", "/api/traces/{id}", "A trace by id", []string{"id"}, rng, false, nil),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/v2/traces/{id}", "A trace by id (v2)", []string{"id"}, rng, false, nil),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/search", "Search traces", nil, cat([]string{"q", "tags", "minDuration", "maxDuration", "limit"}, rng, []string{"spss"}), false,
			map[string]string{"q": "A TraceQL query. Without it, `tags`, `minDuration` and `maxDuration` are used."}),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/search/tags", "Tag names", nil, cat([]string{"scope"}, rng), false, nil),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/v2/search/tags", "Tag names by scope (v2)", nil, cat([]string{"scope", "q"}, rng), false, map[string]string{"q": "A TraceQL query to narrow to (optional)."}),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/search/tag/{tag}/values", "Values of a tag", []string{"tag"}, rng, false, nil),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/v2/search/tag/{tag}/values", "Values of a tag (v2)", []string{"tag"}, cat([]string{"q"}, rng), false, map[string]string{"q": "A TraceQL query to narrow to (optional)."}),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/metrics/query", "TraceQL metrics, instant", nil, cat([]string{"q"}, rng), false, map[string]string{"q": "A TraceQL metrics query, such as `{ } | rate() by (resource.service.name)`."}),
		a.nativeOp("tempo", tagTempo, "Tempo", "/api/metrics/query_range", "TraceQL metrics over time", nil, cat([]string{"q"}, rng, []string{"step", "exemplars"}), false, map[string]string{"q": "A TraceQL metrics query, such as `{ } | rate() by (resource.service.name)`."}),
	}
}
