package fusionapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func mirrorReq(method, rawURL, body string) *http.Request {
	r := httptest.NewRequest(method, rawURL, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return r
}

func TestMirrorForwardsTheRequestAndReturnsTheBackendsOwnAnswer(t *testing.T) {
	f := newFake(t)
	var gotBody, gotAccept, gotMethod, gotFlag string
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotAccept, gotMethod = string(b), r.Header.Get("Accept"), r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}
	f.loki = func(w http.ResponseWriter, r *http.Request) {
		gotFlag = r.Header.Get("X-Loki-Response-Encoding-Flags")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`parse error at line 1, col 4: syntax error: unexpected IDENTIFIER`))
	}
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/protobuf")
		_, _ = w.Write([]byte{0x0a, 0x01, 0x02})
	}
	c := f.client()
	ctx := context.Background()

	r, err := c.Mirror(ctx, AllSignals(), MirrorPrometheus, "/api/v1/query", mirrorReq("GET", "/x?query=sum%28up%29&time=1791200000", ""))
	if err != nil || r.Status != 200 || r.ContentType != "application/json" || !strings.Contains(string(r.Body), `"resultType":"vector"`) {
		t.Fatalf("%+v %v", r, err)
	}
	if q := f.last("/api/v1/query"); q.Get("query") != "sum(up)" || q.Get("time") != "1791200000" || gotAccept != "application/json" || gotMethod != "GET" {
		t.Fatalf("what the store was asked: %v %q %q", q, gotAccept, gotMethod)
	}
	// A long query is sent as a form, and reaches the store as one.
	form := url.Values{"query": {"up"}, "step": {"30"}}.Encode()
	r, err = c.Mirror(ctx, AllSignals(), MirrorPrometheus, "/api/v1/query_range", mirrorReq("POST", "/x", form))
	if err != nil || r.Status != 200 || gotMethod != "POST" || gotBody != form {
		t.Fatalf("POST: %+v %v method=%q body=%q", r, err, gotMethod, gotBody)
	}
	// What a store says about the caller's own query is passed on in the store's own words and status.
	req := mirrorReq("GET", "/x?query=%7Bbroken", "")
	req.Header.Set("X-Loki-Response-Encoding-Flags", "categorize-labels")
	r, err = c.Mirror(ctx, AllSignals(), MirrorLoki, "/loki/api/v1/query_range", req)
	if err != nil || r.Status != 400 || !strings.Contains(string(r.Body), "parse error") || gotFlag != "categorize-labels" {
		t.Fatalf("a bad query: %+v %v flag=%q", r, err, gotFlag)
	}
	// Protobuf comes back as protobuf.
	req = mirrorReq("GET", "/x", "")
	req.Header.Set("Accept", "application/protobuf")
	r, err = c.Mirror(ctx, AllSignals(), MirrorTempo, "/api/traces/"+traceHex, req)
	if err != nil || r.ContentType != "application/protobuf" || len(r.Body) != 3 {
		t.Fatalf("protobuf: %+v %v", r, err)
	}
}

func TestMirrorRefusesWhoMayNotUseItAndWhatItDoesNotServe(t *testing.T) {
	f := newFake(t)
	asked := false
	f.prom = func(w http.ResponseWriter, r *http.Request) { asked = true; w.Write([]byte(`{}`)) }
	c := f.client()
	ctx := context.Background()
	expect := func(name string, err error, status int) {
		t.Helper()
		var e *Error
		if !asErr(err, &e) || e.Status != status {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := c.Mirror(ctx, limited, MirrorPrometheus, "/api/v1/query", mirrorReq("GET", "/x?query=up", ""))
	expect("a namespace-limited token", err, 403)
	_, err = c.Mirror(ctx, Scope{Signals: []string{SignalLogs, SignalTraces}}, MirrorPrometheus, "/api/v1/query", mirrorReq("GET", "/x?query=up", ""))
	expect("a token without metrics", err, 403)
	_, err = c.Mirror(ctx, Scope{Signals: []string{SignalMetrics}, Clusters: []string{"cl-1"}}, MirrorPrometheus, "/api/v1/query", mirrorReq("GET", "/x", ""))
	expect("a cluster-limited token", err, 403)
	if asked {
		t.Fatal("the store was asked on behalf of a caller who may not use the mirror")
	}
	_, err = c.Mirror(ctx, AllSignals(), "etcd", "/x", mirrorReq("GET", "/x", ""))
	expect("an unknown store", err, 400)
	_, err = c.Mirror(ctx, AllSignals(), MirrorPrometheus, "/api/v1/query", mirrorReq("DELETE", "/x", ""))
	expect("DELETE", err, 405)
	req := mirrorReq("POST", "/x", `{"query":"up"}`)
	req.Header.Set("Content-Type", "application/json")
	_, err = c.Mirror(ctx, AllSignals(), MirrorPrometheus, "/api/v1/query", req)
	expect("a JSON POST", err, 415)
	_, err = c.Mirror(ctx, AllSignals(), MirrorPrometheus, "/api/v1/query", mirrorReq("POST", "/x", strings.Repeat("a", maxMirrorBody+1)))
	expect("a huge form", err, 413)
	_, err = c.Mirror(ctx, AllSignals(), MirrorPrometheus, "/api/v1/query", mirrorReq("GET", "/x?query="+strings.Repeat("a", maxMirrorQuery), ""))
	expect("a huge query string", err, 400)
	// A store that is not configured is unavailable, not a crash.
	_, err = (&Client{}).Mirror(ctx, AllSignals(), MirrorLoki, "/loki/api/v1/labels", mirrorReq("GET", "/x", ""))
	expect("an unconfigured store", err, 503)
	for _, bad := range []string{"", "a/b", "..", "a..b", "a b", "a?b", "a%2Fb", strings.Repeat("a", 300)} {
		if MirrorSegment("label", bad) == nil {
			t.Errorf("%q was accepted as a path segment", bad)
		}
	}
	for _, ok := range []string{"__name__", "k8s.pod.name", "resource.service.name", ".http.method", traceHex} {
		if err := MirrorSegment("label", ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

func TestMirrorDoesNotLeakAStoreFailure(t *testing.T) {
	f := newFake(t)
	f.prom = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`panic at /opt/secret/path.go:12 on host db-7.internal`))
	}
	f.loki = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }
	f.tempo = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"no org id"}`))
	}
	c := f.client()
	for store, want := range map[string]int{MirrorPrometheus: 502, MirrorLoki: 429, MirrorTempo: 400} {
		_, err := c.Mirror(context.Background(), AllSignals(), store, "/x", mirrorReq("GET", "/x", ""))
		var e *Error
		if !asErr(err, &e) || e.Status != want || strings.Contains(e.Msg, "secret") || strings.Contains(e.Msg, "db-7") {
			t.Errorf("%s: %v", store, err)
		}
	}
	// And one bigger than the cap is refused rather than held.
	f2 := newFake(t)
	f2.prom = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 4096))) }
	c2 := f2.client()
	c2.MaxBytes = 1024
	_, err := c2.Mirror(context.Background(), AllSignals(), MirrorPrometheus, "/x", mirrorReq("GET", "/x", ""))
	var e *Error
	if !asErr(err, &e) || e.Status != 422 {
		t.Fatalf("a big answer: %v", err)
	}
}

func asErr(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
