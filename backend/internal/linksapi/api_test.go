package linksapi

import (
	"net/http"
	"os"
	"regexp"
	"testing"

	"continuum/internal/fusionhttp"
	"continuum/internal/httpkit"
)

type recordedRoutes map[string]httpkit.Need

func (r recordedRoutes) Handle(pattern string, n httpkit.Need, h http.HandlerFunc) {
	if h == nil {
		panic("a route with no handler: " + pattern)
	}
	r[pattern] = n
}

func (r recordedRoutes) Open(pattern string, h http.HandlerFunc) { r.Handle(pattern, -1, h) }

// Anyone in the organisation sees the figures of its links; replacing the credentials of the throughput tests is an administrator's.
func TestTheLinksMountWithTheRolesTheyAsk(t *testing.T) {
	got := recordedRoutes{}
	(&API{}).Mount(got)
	const o = httpkit.OrgPath
	want := recordedRoutes{
		"GET " + o + "/links/latest":              httpkit.MemberRole,
		"POST " + o + "/links/rotate-credentials": httpkit.AdminRole,
	}
	if len(got) != len(want) {
		t.Fatalf("mounted %v, want %v", got, want)
	}
	for p, n := range want {
		if got[p] != n {
			t.Errorf("%s asks %v, want %v", p, got[p], n)
		}
	}
}

// The links handler answers a route of FUSION's data API, which the table in fusionhttp describes; so every parameter it reads has to be
// one the table describes, which fusionhttp checks of its own handlers and this checks of this one.
func TestEveryQueryParameterTheDataHandlerReadsIsDocumented(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`\.(?:Get|Has)\("([a-z_]+)"\)`).FindAllStringSubmatch(string(src), -1) {
		if _, ok := fusionhttp.ParamDefs[m[1]]; !ok {
			t.Errorf("the links handler reads %q but it is not described in fusionhttp.ParamDefs", m[1])
		}
	}
}
