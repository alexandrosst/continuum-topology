package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// pageCookie is the cookie a redeemed ticket sets, nil when there is none.
func pageCookie(r resp) *http.Cookie {
	for _, c := range r.Result().Cookies() {
		if c.Name == fusionUICookie {
			return c
		}
	}
	return nil
}

// ticketPath asks for a page's link as the person whose session cookie it is given (the administrator's when none is).
func (p *pageRig) ticketPath(t *testing.T, page string, session ...string) string {
	t.Helper()
	c := p.admin
	if len(session) > 0 {
		c = session[0]
	}
	r := p.do("POST", "/api/v1/fusion/pages", map[string]string{"page": page}, withCookie(c))
	if r.Code != 200 {
		t.Fatalf("pages: %d %s", r.Code, r.Body.String())
	}
	return r.json(t)["path"].(string)
}

// A link opened in a new tab carries no session cookie (it is SameSite=Strict), so the page is reached with a one-time
// ticket instead: it sets a cookie for the pages alone, redirects to the page without the ticket, and the page then loads on
// that cookie - and none of it reaches Grafana.
func TestAPageOpensFromATicketWithoutTheSessionCookie(t *testing.T) {
	p := newPageRig(t)
	path := p.ticketPath(t, "grafana")
	if !strings.HasPrefix(path, fusionGrafanaPath+"?"+fusionTicketParam+"=") {
		t.Fatalf("path = %s", path)
	}
	// The new tab: no cookie of any kind.
	r := p.get(path)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != fusionGrafanaPath {
		t.Fatalf("redeem: %d to %q", r.Code, r.Header().Get("Location"))
	}
	ck := pageCookie(r)
	if ck == nil || ck.Path != "/fusion/" || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.MaxAge <= 0 {
		t.Fatalf("page cookie = %+v", ck)
	}
	if strings.Contains(r.Header().Get("Location"), "ticket") || p.last("grafana") != nil {
		t.Fatal("the ticket was passed on, or the redemption reached Grafana")
	}
	// The redirect is followed with that cookie alone.
	page := func(u string) resp {
		return p.get(u, func(req *http.Request) { req.AddCookie(ck) })
	}
	if r := page(fusionGrafanaPath + "d/abc?orgId=1"); r.Code != 200 || r.Body.String() != "grafana /fusion/grafana/d/abc" {
		t.Fatalf("page: %d %q", r.Code, r.Body.String())
	}
	seen := p.last("grafana")
	if seen.Header.Get("Cookie") != "" || strings.Contains(seen.URL.RawQuery, "ticket") {
		t.Fatalf("Grafana was sent our cookie or the ticket: %q ?%s", seen.Header.Get("Cookie"), seen.URL.RawQuery)
	}
	if seen.Header.Get("X-Webauth-User") != "root" {
		t.Fatalf("Grafana was told %q, want the person the ticket was for", seen.Header.Get("X-Webauth-User"))
	}
	// Prometheus too, on the same cookie.
	if r := page(fusionPromPath + "graph"); r.Code == 401 {
		t.Fatalf("the page cookie did not open Prometheus")
	}
}

// What the cookie is for is these pages, and nothing else: not the API, not the data API.
func TestThePageCookieOpensNothingButThePages(t *testing.T) {
	p := newPageRig(t)
	ck := pageCookie(p.get(p.ticketPath(t, "grafana")))
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/operators", "/api/v1/fusion", "/api/v1/fusion/tokens"} {
		if r := p.do("GET", path, nil, func(req *http.Request) { req.AddCookie(ck) }); r.Code != 401 {
			t.Errorf("%s with the page cookie: %d", path, r.Code)
		}
	}
	// A page that writes keeps its same-origin rule: the cookie does not make a foreign origin acceptable.
	r := p.send("POST", fusionGrafanaPath+"api/ds/query", strings.NewReader("{}"), withHeader("Origin", "https://evil.example"), func(req *http.Request) {
		req.Header.Del("Cookie")
		req.AddCookie(ck)
	})
	if r.Code != 403 {
		t.Fatalf("a write from another origin: %d", r.Code)
	}
}

// A ticket is good once, for a short time, and only on a GET.
func TestAPageTicketIsSingleUseAndShortLived(t *testing.T) {
	p := newPageRig(t)
	path := p.ticketPath(t, "grafana")
	if r := p.get(path); r.Code != http.StatusSeeOther {
		t.Fatalf("first use: %d", r.Code)
	}
	r := p.get(path)
	if r.Code != 401 || pageCookie(r) != nil || !strings.Contains(r.Body.String(), "expired") {
		t.Fatalf("second use: %d %q", r.Code, r.Body.String())
	}
	path = p.ticketPath(t, "grafana")
	*p.now = p.now.Add(fusionTicketTTL + time.Second)
	if r := p.get(path); r.Code != 401 || pageCookie(r) != nil {
		t.Fatalf("an old ticket: %d", r.Code)
	}
	path = p.ticketPath(t, "grafana")
	if r := p.send("POST", path, strings.NewReader("{}"), withHeader("Origin", "")); r.Code == http.StatusSeeOther {
		t.Fatal("a POST redeemed a ticket")
	}
	// The ticket is a credential for exactly one person's pages: a made-up one is not.
	if r := p.get(fusionGrafanaPath + "?" + fusionTicketParam + "=nope"); r.Code != 401 {
		t.Fatalf("a made-up ticket: %d", r.Code)
	}
}

// The cookie is judged on every request: a person who stops being an administrator, or whose cookie ends, loses the pages.
func TestThePageCookieStopsWhenThePersonIsNoLongerAnAdministrator(t *testing.T) {
	p := newPageRig(t)
	id, cookie := p.user(t, "dana", RoleAdmin)
	ck := pageCookie(p.get(p.ticketPath(t, "prometheus", cookie)))
	if ck == nil {
		t.Fatal("no cookie")
	}
	open := func() int { return p.get(fusionGrafanaPath, func(req *http.Request) { req.AddCookie(ck) }).Code }
	if c := open(); c != 200 {
		t.Fatalf("an administrator: %d", c)
	}
	if err := p.st.SetMemberRole(p.ctx, "org-1", id, RoleViewer); err != nil {
		t.Fatal(err)
	}
	if c := open(); c != 401 {
		t.Fatalf("after losing the role: %d", c)
	}
	if err := p.st.SetMemberRole(p.ctx, "org-1", id, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if c := open(); c != 401 {
		t.Fatalf("a cookie that was ended came back: %d", c)
	}
	// And by the clock.
	ck = pageCookie(p.get(p.ticketPath(t, "grafana")))
	*p.now = p.now.Add(fusionUISessionTTL + time.Minute)
	if c := open(); c != 401 {
		t.Fatalf("after eight hours: %d", c)
	}
}

// Who may ask for a link: administrators of FUSION's organisation, for a page that is running, by name.
func TestOnlyAnAdministratorMayAskForAPageLink(t *testing.T) {
	p := newPageRig(t)
	_, viewer := p.user(t, "vera", RoleViewer)
	ask := func(page string, o ...opt) resp {
		return p.do("POST", "/api/v1/fusion/pages", map[string]string{"page": page}, o...)
	}
	if r := ask("grafana", withCookie(viewer)); r.Code != 403 {
		t.Errorf("a viewer: %d", r.Code)
	}
	if r := ask("grafana"); r.Code != 401 {
		t.Errorf("nobody: %d", r.Code)
	}
	if r := ask("loki", withCookie(p.admin)); r.Code != 400 && r.Code != 422 {
		t.Errorf("an unknown page: %d", r.Code)
	}
	if r := ask("grafana", withCookie(p.admin)); r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("an administrator: %d %v", r.Code, r.Header())
	}
	p.kube.replicas["continuum-fusion-grafana"] = 0
	if r := ask("grafana", withCookie(p.admin)); r.Code != 409 || !strings.Contains(r.Body.String(), "not running") {
		t.Errorf("a page that is down: %d %s", r.Code, r.Body.String())
	}
}
