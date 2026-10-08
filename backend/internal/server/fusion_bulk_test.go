package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"continuum/internal/fusionapi"
)

// post calls a data-API route that takes a body (it is not under an organisation's path).
func (d *dataRig) post(path string, body any, opts ...opt) resp {
	var rd *bytes.Reader
	switch b := body.(type) {
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req := httptest.NewRequest("POST", path, rd)
	req.RemoteAddr = "10.1.1.1:5555"
	req.Header.Set("X-Requested-With", "test")
	req.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	return resp{rec}
}

func (d *dataRig) lines(t *testing.T, r resp) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("a line that is not JSON: %q", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func TestFusedTrueReturnsTheFusedObjectAndIncludeNoneShapesTheTraceAlone(t *testing.T) {
	d := newDataRig(t)
	r := d.get("/api/v1/fusion/traces/"+testTraceID+"?fused=true", withCookie(d.admin))
	j := r.json(t)
	if r.Code != 200 || j["sources"] == nil || j["sources"].(map[string]any)["logs"] != "ok" || j["sources"].(map[string]any)["metrics"] != "ok" {
		t.Fatalf("fused=true: %d %s", r.Code, r.Body.String())
	}
	// Without it, the plain trace; fused=false with an include is a mistake worth saying.
	if j := d.get("/api/v1/fusion/traces/"+testTraceID, withCookie(d.admin)).json(t); j["sources"] != nil {
		t.Fatalf("the plain trace carries the fused parts: %v", j)
	}
	if r := d.get("/api/v1/fusion/traces/"+testTraceID+"?fused=false&include=logs", withCookie(d.admin)); r.Code != 400 {
		t.Fatalf("fused=false&include=logs: %d", r.Code)
	}
	// include=none: no log or metric read, but the shaping applies.
	before := d.stores.count()
	j = d.get("/api/v1/fusion/traces/"+testTraceID+"?include=none&span_service=cart&omit=attributes", withCookie(d.admin)).json(t)
	if n := len(j["spans"].([]any)); n != 1 || j["spansOmitted"].(float64) != 1 {
		t.Fatalf("span filter: %v", j)
	}
	if d.stores.count()-before != 1 {
		t.Fatalf("include=none read more than the trace: %d store calls", d.stores.count()-before)
	}
	// The new log sources ask Loki for what they say they do.
	d.get("/api/v1/fusion/traces/"+testTraceID+"?include=context_logs,system_logs&log_severity=error", withCookie(d.admin))
	if !d.stores.askedAbout("trace_id%3D%22%22") {
		t.Fatalf("context logs were not asked for: %v", d.stores.asked)
	}
}

func TestAFusedSearchReadsEveryHitInFull(t *testing.T) {
	d := newDataRig(t)
	r := d.get("/api/v1/fusion/traces?fused=true&omit=events", withCookie(d.admin))
	j := r.json(t)
	res, _ := j["results"].([]any)
	if r.Code != 200 || len(j["traces"].([]any)) != 1 || len(res) != 1 || res[0].(map[string]any)["status"].(float64) != 200 || res[0].(map[string]any)["trace"] == nil {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if j["summary"].(map[string]any)["ok"].(float64) != 1 {
		t.Fatalf("summary: %v", j["summary"])
	}
	// Not fused: no results key at all.
	if j := d.get("/api/v1/fusion/traces", withCookie(d.admin)).json(t); j["results"] != nil {
		t.Fatalf("a plain search carries results: %v", j)
	}
	// A fused search is a smaller page.
	d.get("/api/v1/fusion/traces?fused=true&limit=100", withCookie(d.admin))
	if !d.stores.askedAbout("limit=25") || d.stores.askedAbout("limit=100") {
		t.Fatalf("the search limit was not cut to %d", fusionapi.MaxBatch)
	}
	// NDJSON: the hits first, then each trace, then a count.
	r = d.get("/api/v1/fusion/traces?fused=true&stream=true", withCookie(d.admin))
	ls := d.lines(t, r)
	if r.Header().Get("Content-Type") != "application/x-ndjson" || len(ls) != 3 || ls[0]["type"] != "traces" || ls[1]["type"] != "result" || ls[1]["id"] != testTraceID || ls[2]["type"] != "summary" || ls[2]["ok"].(float64) != 1 {
		t.Fatalf("%s %s", r.Header().Get("Content-Type"), r.Body.String())
	}
}

func TestABatchReadsManyTracesAndReportsEachOne(t *testing.T) {
	d := newDataRig(t)
	second := strings.Repeat("ab", 16)
	r := d.post("/api/v1/fusion/traces/batch", map[string]any{"ids": []string{testTraceID, second, "nonsense", strings.ToUpper(testTraceID)},
		"include": []string{"logs"}, "omit": []string{"events"}, "pad": "5m", "max_logs": 10}, withCookie(d.admin))
	j := r.json(t)
	res := j["results"].([]any)
	if r.Code != 200 || len(res) != 3 { // the upper-case repeat of the first id is one trace
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if res[0].(map[string]any)["id"] != testTraceID || res[0].(map[string]any)["status"].(float64) != 200 || res[2].(map[string]any)["status"].(float64) != 400 {
		t.Fatalf("results: %s", r.Body.String())
	}
	if s := j["summary"].(map[string]any); s["requested"].(float64) != 3 || s["ok"].(float64) != 2 || s["failed"].(float64) != 1 {
		t.Fatalf("summary: %v", s)
	}
	if src := res[0].(map[string]any)["trace"].(map[string]any)["sources"].(map[string]any); src["logs"] != "ok" || src["metrics"] != "not requested" {
		t.Fatalf("include was not honoured: %v", src)
	}
	// With no include, a batch carries logs and metrics.
	r = d.post("/api/v1/fusion/traces/batch", map[string]any{"ids": []string{testTraceID}}, withCookie(d.admin))
	if src := r.json(t)["results"].([]any)[0].(map[string]any)["trace"].(map[string]any)["sources"].(map[string]any); src["metrics"] != "ok" {
		t.Fatalf("default include: %v", src)
	}
}

func TestABatchRefusesWhatItCannotRead(t *testing.T) {
	d := newDataRig(t)
	many := make([]string, 26)
	for i := range many {
		many[i] = strings.Repeat("0", 24) + strings.Repeat("0", 8-len(itoa64(int64(i+1)))) + itoa64(int64(i+1))
	}
	for name, body := range map[string]any{
		"not json":           "[1,2",
		"no ids":             map[string]any{"include": []string{"logs"}},
		"empty ids":          map[string]any{"ids": []string{}},
		"too many ids":       map[string]any{"ids": many},
		"an unknown field":   map[string]any{"ids": []string{testTraceID}, "colour": "red"},
		"fused":              map[string]any{"ids": []string{testTraceID}, "fused": true},
		"a bad include":      map[string]any{"ids": []string{testTraceID}, "include": []string{"secrets"}},
		"a bad number":       map[string]any{"ids": []string{testTraceID}, "max_logs": "lots"},
		"a nested value":     map[string]any{"ids": []string{testTraceID}, "pad": map[string]any{"a": 1}},
		"a number in a list": map[string]any{"ids": []any{1}},
	} {
		if r := d.post("/api/v1/fusion/traces/batch", body, withCookie(d.admin)); r.Code != 400 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body.String())
		}
	}
	// A body past the cap is refused before it is read in full.
	big := `{"ids":["` + strings.Repeat("a", maxBatchBody) + `"]}`
	if r := d.post("/api/v1/fusion/traces/batch", big, withCookie(d.admin)); r.Code != 400 {
		t.Errorf("a body of %d bytes: %d", len(big), r.Code)
	}
	// Nobody signed in, and a token that may not read traces.
	if r := d.post("/api/v1/fusion/traces/batch", map[string]any{"ids": []string{testTraceID}}); r.Code != 401 {
		t.Errorf("no credential: %d", r.Code)
	}
	tok, _ := d.mint(t, map[string]any{"name": "metrics only", "signals": []string{"metrics"}})
	if r := d.post("/api/v1/fusion/traces/batch", map[string]any{"ids": []string{testTraceID}}, bearer(tok)); r.Code != 200 || r.json(t)["results"].([]any)[0].(map[string]any)["status"].(float64) < 400 {
		t.Errorf("a token without traces read a trace: %d %s", r.Code, r.Body.String())
	}
}

func TestABatchStreamsEachTraceAsNDJSON(t *testing.T) {
	d := newDataRig(t)
	body := map[string]any{"ids": []string{testTraceID, strings.Repeat("ab", 16), "nonsense"}, "include": []string{"logs"}}
	for name, opts := range map[string][]opt{"the Accept header": {withCookie(d.admin), withHeader("Accept", "application/x-ndjson")}, "stream in the body": nil} {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		if opts == nil {
			b["stream"] = true
			opts = []opt{withCookie(d.admin)}
		}
		r := d.post("/api/v1/fusion/traces/batch", b, opts...)
		ls := d.lines(t, r)
		if r.Code != 200 || r.Header().Get("Content-Type") != "application/x-ndjson" || len(ls) != 4 {
			t.Fatalf("%s: %d %s", name, r.Code, r.Body.String())
		}
		ok, bad := 0, 0
		for _, l := range ls[:3] {
			if l["type"] != "result" {
				t.Fatalf("%s: %v", name, l)
			}
			if l["status"].(float64) == 200 {
				ok++
			} else {
				bad++
			}
		}
		last := ls[3]
		if ok != 2 || bad != 1 || last["type"] != "summary" || last["requested"].(float64) != 3 || last["failed"].(float64) != 1 {
			t.Fatalf("%s: %v", name, ls)
		}
	}
	// stream=false wins over the header.
	r := d.post("/api/v1/fusion/traces/batch?stream=false", map[string]any{"ids": []string{testTraceID}}, withCookie(d.admin), withHeader("Accept", "application/x-ndjson"))
	if r.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("stream=false: %s", r.Header().Get("Content-Type"))
	}
}

func TestABulkReadIsChargedPerTrace(t *testing.T) {
	d := newDataRig(t)
	d.a.fusionRL = NewLimiter(60, 5)
	tok, _ := d.mint(t, map[string]any{"name": "bulk"})
	ids := make([]string, 8)
	for i := range ids {
		ids[i] = strings.Repeat("0", 31) + "123456789abcdef"[i:i+1]
	}
	r := d.post("/api/v1/fusion/traces/batch", map[string]any{"ids": ids}, bearer(tok))
	if r.Code != 429 || r.Header().Get("Retry-After") == "" {
		t.Fatalf("8 traces against a burst of 5: %d %s", r.Code, r.Body.String())
	}
}

// ---- the description ----

func TestTheOpenAPIDescriptionIsPublicAndCoversEveryRoute(t *testing.T) {
	d := newDataRig(t)
	r := d.get("/api/v1/fusion/openapi.json")
	if r.Code != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", r.Code, r.Header().Get("Content-Type"))
	}
	var spec struct {
		OpenAPI string                               `json:"openapi"`
		Paths   map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &spec); err != nil || spec.OpenAPI != "3.0.3" {
		t.Fatalf("%v %s", err, spec.OpenAPI)
	}
	for _, op := range fusionOps(nil) {
		o := spec.Paths[fusionAPIPath+op.Path][strings.ToLower(op.Method)]
		if o == nil {
			t.Errorf("%s %s is not in the description", op.Method, op.Path)
			continue
		}
		// And the route is really served (anything but "no such route").
		path := fusionAPIPath + strings.NewReplacer("{id}", testTraceID, "{name}", "cart", "{label}", "service_name", "{tag}", "resource.service.name").Replace(op.Path)
		var rr resp
		if op.Method == "POST" {
			rr = d.post(path, map[string]any{"ids": []string{testTraceID}}, withCookie(d.admin))
		} else {
			rr = d.get(path, withCookie(d.admin))
		}
		if rr.Code == 404 || rr.Code == 405 {
			t.Errorf("%s %s is described but not served: %d", op.Method, path, rr.Code)
		}
	}
	// The other way round: nothing in the description that the table does not have.
	n := 0
	for _, methods := range spec.Paths {
		n += len(methods)
	}
	if n != len(fusionOps(nil)) {
		t.Errorf("%d operations described, %d routes", n, len(fusionOps(nil)))
	}
}

func TestEveryQueryParameterTheHandlersReadIsDocumented(t *testing.T) {
	src, err := os.ReadFile("fusion_data.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`\.(?:Get|Has)\("([a-z_]+)"\)`).FindAllStringSubmatch(string(src), -1) {
		if _, ok := fusionParams[m[1]]; !ok && m[1] != "ids" {
			t.Errorf("the handlers read %q but it is not described in fusionParams", m[1])
		}
	}
	for _, n := range fusionapi.FuseParamNames {
		if _, ok := fusionParams[n]; !ok {
			t.Errorf("the fused read takes %q but it is not described", n)
		}
	}
	// Every described parameter is used by a route, every route's parameters are described, and the fused ones are
	// offered by both routes that read a fused trace.
	used := map[string]bool{}
	for _, op := range fusionOps(nil) {
		for _, n := range op.Params {
			used[n] = true
			if _, ok := fusionParams[n]; !ok {
				t.Errorf("%s %s lists %q, which is not described", op.Method, op.Path, n)
			}
		}
		for _, n := range op.PathParams {
			if _, ok := fusionPathParams[n]; !ok || !strings.Contains(op.Path, "{"+n+"}") {
				t.Errorf("%s %s: path parameter %q", op.Method, op.Path, n)
			}
		}
		if op.Body == "" && op.Method == "POST" {
			t.Errorf("%s %s has no body", op.Method, op.Path)
		}
	}
	for n := range fusionParams {
		if !used[n] && n != "ids" {
			t.Errorf("%q is described but no route takes it", n)
		}
	}
	for _, path := range []string{"/traces", "/traces/{id}"} {
		for _, op := range fusionOps(nil) {
			if op.Path == path && op.Method == "GET" {
				for _, n := range fusionapi.FuseParamNames {
					found := false
					for _, p := range op.Params {
						found = found || p == n
					}
					if !found {
						t.Errorf("GET %s does not offer %q", path, n)
					}
				}
			}
		}
	}
}

func TestTheOpenAPIReferencesResolve(t *testing.T) {
	spec := fusionOpenAPI()
	raw, _ := json.Marshal(spec)
	comps := spec["components"].(obj)
	schemas := comps["schemas"].(obj)
	responses := comps["responses"].(obj)
	for _, m := range regexp.MustCompile(`"\$ref":"#/components/(schemas|responses)/([A-Za-z]+)"`).FindAllStringSubmatch(string(raw), -1) {
		var table obj = schemas
		if m[1] == "responses" {
			table = responses
		}
		if table[m[2]] == nil {
			t.Errorf("dangling reference to %s/%s", m[1], m[2])
		}
	}
	// Operation ids are unique, because client generators need that.
	seen := map[string]bool{}
	for _, op := range fusionOps(nil) {
		if id := opID(op); seen[id] {
			t.Errorf("operation id %q repeats", id)
		} else {
			seen[id] = true
		}
	}
}

func TestTheDocsPageIsSelfContainedAndNeedsNoCredential(t *testing.T) {
	d := newDataRig(t)
	page := d.get("/api/v1/fusion/docs")
	if page.Code != 200 || !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s", page.Code, page.Header().Get("Content-Type"))
	}
	csp := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "unsafe") || strings.Contains(csp, "http") {
		t.Fatalf("csp = %q", csp)
	}
	html := page.Body.String()
	// No inline script and nothing from another host: the CSP would stop them, and a page that relied on them would be broken.
	if regexp.MustCompile(`(?i)<script(\s[^>]*)?>[^<]`).MatchString(html) || regexp.MustCompile(`(?i)(src|href)="https?://`).MatchString(html) || regexp.MustCompile(`(?i)\son[a-z]+=`).MatchString(html) {
		t.Fatalf("the page is not self-contained:\n%s", html)
	}
	for _, p := range []string{"/api/v1/fusion/docs/app.js", "/api/v1/fusion/docs/app.css"} {
		if r := d.get(p); r.Code != 200 || r.Header().Get("Content-Security-Policy") != csp {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
	js := d.get("/api/v1/fusion/docs/app.js").Body.String()
	if strings.Contains(js, "innerHTML") || strings.Contains(js, "eval(") || strings.Contains(js, "document.write") {
		t.Error("the page script builds markup from strings")
	}
	// The rest of the API keeps its strict policy.
	if got := d.get("/api/v1/fusion/status", withCookie(d.admin)).Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || strings.Contains(got, "script-src") {
		t.Errorf("the data API's policy changed: %q", got)
	}
	// And it works when FUSION is not installed: the description is not data.
	d.a.Fusion = nil
	if r := d.get("/api/v1/fusion/openapi.json"); r.Code != 200 {
		t.Errorf("openapi without FUSION: %d", r.Code)
	}
}
