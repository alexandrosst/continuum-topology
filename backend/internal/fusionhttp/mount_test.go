package fusionhttp

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ikhnos/internal/fusionapi"
)

type recorded struct {
	open, credential []string
	mux              *http.ServeMux
}

func (r *recorded) Open(p string, h http.HandlerFunc) {
	r.open = append(r.open, p)
	r.mux.Handle(p, h)
}
func (r *recorded) Credential(p string, h http.Handler) {
	r.credential = append(r.credential, p)
	r.mux.Handle(p, h)
}

func TestMountPutsEveryRouteBehindACredentialAndOnlyTheDescriptionInTheOpen(t *testing.T) {
	g := newGuard(fakeAccounts{}, fakeTokens{})
	ops := []Op{
		{Method: "GET", Path: "/status", Tag: "Service", Response: "Status", Handler: func(w http.ResponseWriter, _ *http.Request, _ *fusionapi.Client, _ Caller) {}},
		{Method: "GET", Path: "/q", Tag: "Service", Response: "Status", AlsoPost: true, Params: []string{"from"}, Handler: func(w http.ResponseWriter, _ *http.Request, _ *fusionapi.Client, _ Caller) {}},
	}
	rec := &recorded{mux: http.NewServeMux()}
	g.Mount(rec, ops)
	want := []string{"GET /api/v1/fusion/status", "GET /api/v1/fusion/q", "POST /api/v1/fusion/q"}
	if !slices.Equal(rec.credential, want) {
		t.Errorf("credentialed routes = %v, want %v", rec.credential, want)
	}
	if !slices.Equal(rec.open, []string{"GET /api/v1/fusion/docs", "GET /api/v1/fusion/docs/app.js", "GET /api/v1/fusion/docs/app.css", "GET /api/v1/fusion/openapi.json"}) {
		t.Errorf("open routes = %v", rec.open)
	}
}

func TestAParameterARouteDoesNotListIsRefusedBeforeItsHandlerRuns(t *testing.T) {
	ran := false
	g := newGuard(fakeAccounts{p: admin}, fakeTokens{})
	rec := &recorded{mux: http.NewServeMux()}
	g.Mount(rec, []Op{{Method: "GET", Path: "/q", Tag: "T", Response: "R", Params: []string{"from"},
		Handler: func(http.ResponseWriter, *http.Request, *fusionapi.Client, Caller) { ran = true }}})
	r := httptest.NewRequest("GET", "/api/v1/fusion/q?frm=now-1h", nil)
	r.Header.Set("Authorization", "Bearer pat_abc")
	w := httptest.NewRecorder()
	rec.mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || ran {
		t.Errorf("code %d, handler ran: %v", w.Code, ran)
	}
}

func TestTheDescriptionNamesEveryRouteOfTheTable(t *testing.T) {
	ops := []Op{{Method: "GET", Path: "/a/{id}", Tag: "T", Summary: "s", PathParams: []string{"id"}, Response: "Status"}}
	spec := OpenAPI(ops)
	paths := spec["paths"].(obj)
	if _, ok := paths[APIPath+"/a/{id}"]; !ok || len(paths) != 1 {
		t.Errorf("paths = %v", paths)
	}
}

func TestARouteWithoutAHandlerIsNotMounted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a route with no handler was mounted")
		}
	}()
	newGuard(fakeAccounts{}, fakeTokens{}).Mount(&recorded{mux: http.NewServeMux()}, []Op{{Method: "GET", Path: "/x", Tag: "T", Response: "R"}})
}

func TestAParameterARouteCannotRunWithoutIsMarkedRequiredInTheDescription(t *testing.T) {
	var g *Guard
	ops := g.Ops()
	spec := OpenAPI(ops)["paths"].(obj)
	n := 0
	for _, op := range ops {
		params := spec[APIPath+op.Path].(obj)[strings.ToLower(op.Method)].(obj)["parameters"].([]obj)
		for _, p := range params {
			name, _ := p["name"].(string)
			in, _ := p["in"].(string)
			want := in == "path" || slices.Contains(op.Required, name)
			got, _ := p["required"].(bool)
			if got != want {
				t.Errorf("%s %s %s: required = %v, want %v", op.Method, op.Path, name, got, want)
			}
			if in == "query" && want {
				n++
			}
		}
		for _, r := range op.Required {
			if !slices.Contains(op.Params, r) {
				t.Errorf("%s %s requires %q, which it does not take", op.Method, op.Path, r)
			}
		}
	}
	if n == 0 {
		t.Error("no route marks a query parameter required")
	}
}
