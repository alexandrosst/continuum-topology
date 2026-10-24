package server

import (
	"encoding/json"
	"strings"
	"testing"

	"ikhnos/internal/fusionhttp"
	"ikhnos/internal/kernel"
)

// The guide of the docs page (internal/fusionhttp/guide.go) says how the API answers; each thing it says is asked of the server here.
func TestTheGuideOfTheDocsPageIsWhatTheServerDoes(t *testing.T) {
	d := newDataRig(t)
	d.a.extras = appExtras{shopGroups()}
	errText := func(r resp, code int, want string) {
		t.Helper()
		if r.Code != code {
			t.Errorf("want %d, got %d %s", code, r.Code, r.Body.String())
			return
		}
		var e map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &e); err != nil || len(e) != 1 || !strings.Contains(e["error"].(string), want) {
			t.Errorf("%d body %s: want only {\"error\"} containing %q", code, r.Body.String(), want)
		}
	}

	// 401: the same words whatever is wrong with the credential.
	errText(d.get("/api/v1/fusion/status"), 401, "sign in required")
	errText(d.get("/api/v1/fusion/status", bearer("cnf_unknown")), 401, "sign in required")
	// 403: a signal the token lacks, and a raw query from a token with a limit.
	metricsOnly, _ := d.mint(t, map[string]any{"name": "m", "signals": []string{"metrics"}})
	errText(d.get("/api/v1/fusion/logs", bearer(metricsOnly)), 403, "cannot read logs")
	shopOnly, _ := d.mint(t, map[string]any{"name": "s", "namespaces": []string{"shop"}})
	errText(d.get("/api/v1/fusion/metrics/query?query=up", bearer(shopOnly)), 403, "structured filters")
	errText(d.get("/api/v1/fusion/logs?query=%7Bapp%3D%22x%22%7D", bearer(shopOnly)), 403, "structured filters")
	// ... and a token without a limit may.
	open, _ := d.mint(t, map[string]any{"name": "o"})
	if r := d.get("/api/v1/fusion/metrics/query?query=up", bearer(open)); r.Code != 200 {
		t.Errorf("raw query from an unlimited token: %d %s", r.Code, r.Body.String())
	}
	// A credential sent on purpose beats the cookie of the administrator who is also signed in.
	errText(d.get("/api/v1/fusion/logs", bearer(metricsOnly), withCookie(d.admin)), 403, "cannot read logs")
	// 404: no such application, said in the one shape.
	errText(d.get("/api/v1/fusion/logs?application=nope", withCookie(d.admin)), 404, "no application")
	// 400: a parameter the route does not take, a limit that is not a number, a range the wrong way round or too long.
	errText(d.get("/api/v1/fusion/logs?frm=now-1h", withCookie(d.admin)), 400, "does not take")
	errText(d.get("/api/v1/fusion/status?frm=now-1h", withCookie(d.admin)), 400, `this route does not take "frm"; it takes no parameters`)
	errText(d.get("/api/v1/fusion/logs?limit=0", withCookie(d.admin)), 400, "limit")
	errText(d.get("/api/v1/fusion/metrics/names?from=now&to=now-1h", withCookie(d.admin)), 400, "before")
	errText(d.get("/api/v1/fusion/metrics/names?from=now-40d", withCookie(d.admin)), 400, "")
	// A limit above the maximum is cut to it, and the answer says which limit it used.
	r := d.get("/api/v1/fusion/logs?limit=999999", withCookie(d.admin))
	if got := r.json(t); r.Code != 200 || got["limit"] != float64(2000) {
		t.Errorf("limit above the maximum: %d %v", r.Code, got["limit"])
	}
	// A list says the range it covered, and has no cursor.
	if got := r.json(t); got["from"] == nil || got["to"] == nil || got["next"] != nil {
		t.Errorf("list answer = %v", got)
	}

	// 429: a read over the credential's limit says how long to wait; failed sign-ins from one address are refused without that.
	d.a.fd().Reads = kernel.NewLimiter(60, 1)
	d.get("/api/v1/fusion/status", withCookie(d.admin))
	r = d.get("/api/v1/fusion/status", withCookie(d.admin))
	errText(r, 429, "slow down")
	if r.Header().Get("Retry-After") != "5" {
		t.Errorf("Retry-After = %q", r.Header().Get("Retry-After"))
	}
	d.a.fd().Reads = kernel.NewLimiter(600, 60)
	d.a.fd().AuthFailures = kernel.NewLimiter(60, 1)
	d.get("/api/v1/fusion/status", bearer("cnf_unknown"))
	r = d.get("/api/v1/fusion/status", bearer("cnf_unknown"))
	errText(r, 429, "failed")
	if r.Header().Get("Retry-After") != "" {
		t.Errorf("a throttled sign-in carries Retry-After %q", r.Header().Get("Retry-After"))
	}

	// 503: FUSION not there, and no Retry-After to go by.
	d.a.Fusion = nil
	r = d.get("/api/v1/fusion/status", withCookie(d.admin))
	if r.Code != 503 || r.Header().Get("Retry-After") != "" {
		t.Errorf("FUSION off: %d Retry-After %q", r.Code, r.Header().Get("Retry-After"))
	}
}

func TestTheDescriptionCarriesTheGuideAndTheNotFoundOfRoutesThatNameOneThing(t *testing.T) {
	spec := fusionhttp.OpenAPI(fusionOps(nil))
	var ids []string
	for _, s := range spec["x-guide"].([]obj) {
		ids = append(ids, s["id"].(string))
		if s["title"] == "" || s["body"] == "" {
			t.Errorf("guide section %v is empty", s["id"])
		}
	}
	if strings.Join(ids, ",") != "auth,conventions,errors" {
		t.Errorf("guide = %v", ids)
	}
	paths := spec["paths"].(obj)
	for _, p := range []string{"/applications/{name}", "/services/{name}", "/traces/{id}", "/tempo/api/traces/{id}"} {
		if _, ok := paths[fusionhttp.APIPath+p].(obj)["get"].(obj)["responses"].(obj)["404"]; !ok {
			t.Errorf("%s does not describe its 404", p)
		}
	}
	if _, ok := paths[fusionhttp.APIPath+"/status"].(obj)["get"].(obj)["responses"].(obj)["404"]; ok {
		t.Error("/status describes a 404")
	}
}
