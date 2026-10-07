package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"continuum/internal/store"
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
	if seen.Header.Get("X-Webauth-User") != "ikhnos-root" {
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

// The server's platform-wide core has no organisation of its own, and FUSION belongs to the main one: asking for "FUSION's
// organisation" must never come out empty (it once did, and every page and token request was refused as coming from
// another organisation).
func TestFusionBelongsToTheMainOrganisationOfAServerWhoseCoreHasNone(t *testing.T) {
	base := &Core{DefaultOrg: "main"}
	if got := (&Admin{C: base, Fusion: &FusionControl{}}).fusionOrg(); got != "main" {
		t.Fatalf("no organisation configured: %q", got)
	}
	if got := (&Admin{C: &Core{}, Fusion: &FusionControl{}}).fusionOrg(); got != "default" {
		t.Fatalf("no flag either: %q", got)
	}
	if got := (&Admin{C: base, Fusion: &FusionControl{Org: "told"}}).fusionOrg(); got != "told" {
		t.Fatalf("configured: %q", got)
	}
	if got := (&Admin{C: &Core{OrgID: "org-1"}}).fusionOrg(); got != "org-1" {
		t.Fatalf("a core with an organisation: %q", got)
	}
}

// pageSession opens a page session for the person whose session cookie is given and returns the page cookie.
func (p *pageRig) pageSession(t *testing.T, session string) *http.Cookie {
	t.Helper()
	ck := pageCookie(p.get(p.ticketPath(t, "grafana", session)))
	if ck == nil {
		t.Fatal("no page cookie")
	}
	return ck
}

// pageCode is the status of a Grafana page request that carries only the page cookie, as a new tab does.
func (p *pageRig) pageCode(ck *http.Cookie) int {
	return p.get(fusionGrafanaPath, func(req *http.Request) { req.AddCookie(ck) }).Code
}

// The page cookie is judged like the session cookie: an account that was disabled, or that has to choose a new password,
// has no pages, even though its membership and role are still fine.
func TestThePageCookieStopsForADisabledAccountOrOneThatMustChangeItsPassword(t *testing.T) {
	p := newPageRig(t)
	id, cookie := p.user(t, "dana", RoleAdmin)
	ck := p.pageSession(t, cookie)
	if c := p.pageCode(ck); c != 200 {
		t.Fatalf("an administrator: %d", c)
	}
	if err := p.st.SetDisabled(p.ctx, id, p.now); err != nil {
		t.Fatal(err)
	}
	if c := p.pageCode(ck); c != 401 {
		t.Fatalf("a disabled account: %d", c)
	}
	if err := p.st.SetDisabled(p.ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	ck = p.pageSession(t, cookie)
	if err := p.st.SetPassword(p.ctx, id, mustHash(t, goodPW), true); err != nil {
		t.Fatal(err)
	}
	if c := p.pageCode(ck); c != 401 {
		t.Fatalf("an account that must change its password: %d", c)
	}
}

// Signing out ends the pages that sign-in opened, and tells the browser to drop the page cookie.
func TestLoggingOutEndsThePageSessionsAndClearsTheCookie(t *testing.T) {
	p := newPageRig(t)
	_, cookie := p.user(t, "dana", RoleAdmin)
	ck := p.pageSession(t, cookie)
	ticket := p.ticketPath(t, "grafana", cookie) // a link not opened yet
	r := p.do("POST", "/api/v1/auth/logout", nil, withCookie(cookie))
	if r.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", r.Code)
	}
	cleared := pageCookie(r)
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Path != "/fusion/" || cleared.Value != "" {
		t.Fatalf("the page cookie was not cleared: %+v", cleared)
	}
	if c := p.pageCode(ck); c != 401 {
		t.Fatalf("the page after signing out: %d", c)
	}
	if r := p.get(ticket); r.Code != 401 {
		t.Fatalf("a link from before signing out: %d", r.Code)
	}
}

// A new password signs the person's other browsers out; the pages those opened go with them.
func TestChangingThePasswordEndsThePageSessions(t *testing.T) {
	p := newPageRig(t)
	_, cookie := p.user(t, "dana", RoleAdmin)
	ck := p.pageSession(t, cookie)
	r := p.do("POST", "/api/v1/auth/password", map[string]string{"current": goodPW, "new": goodPW + "x"}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("change password: %d %s", r.Code, r.Body.String())
	}
	if c := p.pageCode(ck); c != 401 {
		t.Fatalf("the page after a password change: %d", c)
	}
}

// Losing the role that opens the pages, or the membership, ends the person's page sessions for good: being made an
// administrator again later does not bring the old cookie back to life.
func TestLosingTheRoleOrTheMembershipEndsThePageSessions(t *testing.T) {
	p := newPageRig(t)
	_, owner := p.user(t, "olga", RoleOwner)
	id, cookie := p.user(t, "dana", RoleAdmin)

	ck := p.pageSession(t, cookie)
	if r := p.do("POST", "/api/v1/members/"+id+"/role", map[string]string{"role": RoleViewer}, withCookie(owner)); r.Code != http.StatusNoContent {
		t.Fatalf("downgrade: %d %s", r.Code, r.Body.String())
	}
	if err := p.st.SetMemberRole(p.ctx, "org-1", id, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if c := p.pageCode(ck); c != 401 {
		t.Fatalf("a cookie from before a downgrade came back: %d", c)
	}

	// A promotion is not a loss: the pages stay.
	ck = p.pageSession(t, cookie)
	if r := p.do("POST", "/api/v1/members/"+id+"/role", map[string]string{"role": RoleOwner}, withCookie(owner)); r.Code != http.StatusNoContent {
		t.Fatalf("promotion: %d %s", r.Code, r.Body.String())
	}
	if c := p.pageCode(ck); c != 200 {
		t.Fatalf("a promotion ended the pages: %d", c)
	}
	if err := p.st.SetMemberRole(p.ctx, "org-1", id, RoleAdmin); err != nil {
		t.Fatal(err)
	}

	ck = p.pageSession(t, cookie)
	if r := p.do("POST", "/api/v1/members/"+id+"/remove", nil, withCookie(owner)); r.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", r.Code, r.Body.String())
	}
	if err := p.st.AddMember(p.ctx, store.Membership{OrgID: "org-1", UserID: id, Role: RoleAdmin, CreatedAt: *p.now}); err != nil {
		t.Fatal(err)
	}
	if c := p.pageCode(ck); c != 401 {
		t.Fatalf("a cookie from before the removal came back: %d", c)
	}
}

// One person cannot fill the table: past the cap their oldest page session goes, and others are not touched.
func TestAPersonHoldsAtMostSomePageSessionsAndTheOldestGoesFirst(t *testing.T) {
	f := newFusionUIAccess()
	now := time.Now()
	var secrets []string
	for i := 0; i < maxFusionUIGrantsPerUser+4; i++ {
		secret, _, out := f.redeemAndOpen(f.ticket("u-1", "dana", now), now)
		if out != fusionTicketOK {
			t.Fatalf("open %d: %v", i, out)
		}
		secrets = append(secrets, secret)
	}
	other, _, _ := f.redeemAndOpen(f.ticket("u-2", "olga", now), now)
	if n := len(f.sessions); n != maxFusionUIGrantsPerUser+1 {
		t.Fatalf("%d sessions held, want %d for dana and 1 for olga", n, maxFusionUIGrantsPerUser)
	}
	for i, s := range secrets {
		_, ok := f.session(s, now)
		if want := i >= 4; ok != want {
			t.Errorf("dana's session %d alive = %v, want %v (the 4 oldest go)", i, ok, want)
		}
	}
	if _, ok := f.session(other, now); !ok {
		t.Error("another person's session was evicted for dana's")
	}
	// Tickets are capped the same way.
	for i := 0; i < maxFusionUIGrantsPerUser+3; i++ {
		f.ticket("u-3", "sam", now)
	}
	n := 0
	for _, g := range f.tickets {
		if g.userID == "u-3" {
			n++
		}
	}
	if n != maxFusionUIGrantsPerUser {
		t.Fatalf("%d tickets held for one person, want %d", n, maxFusionUIGrantsPerUser)
	}
}

// Expired entries are cleared out as time goes by, not only once the table is full.
func TestExpiredPageGrantsAreSweptWithoutTheTableBeingFull(t *testing.T) {
	f := newFusionUIAccess()
	now := time.Now()
	for i := 0; i < 5; i++ {
		f.ticket("u-"+string(rune('a'+i)), "x", now)
		f.redeemAndOpen(f.ticket("u-"+string(rune('a'+i)), "x", now), now)
	}
	later := now.Add(fusionUISessionTTL + time.Minute)
	f.ticket("u-new", "x", later)
	if len(f.tickets) != 1 || len(f.sessions) != 0 {
		t.Fatalf("after the sweep: %d tickets and %d sessions left, want 1 and 0", len(f.tickets), len(f.sessions))
	}
}

// A table with no room for another page session answers "busy" and does not spend the ticket: the same link works as soon
// as there is room.
func TestAFullTableDoesNotBurnATicket(t *testing.T) {
	p := newPageRig(t)
	path := p.ticketPath(t, "grafana")
	far := p.now.Add(24 * time.Hour)
	f := p.a.fusionUIAccess
	f.mu.Lock()
	for i := 0; i < maxFusionUIGrants; i++ {
		f.sessions[strconv.Itoa(i)] = fusionUIGrant{userID: "u-" + strconv.Itoa(i), expires: far}
	}
	f.mu.Unlock()
	r := p.get(path)
	if r.Code != http.StatusServiceUnavailable || pageCookie(r) != nil {
		t.Fatalf("a full table: %d %q", r.Code, r.Body.String())
	}
	f.mu.Lock()
	delete(f.sessions, "0")
	f.mu.Unlock()
	if r := p.get(path); r.Code != http.StatusSeeOther || pageCookie(r) == nil {
		t.Fatalf("the same link once there was room: %d %q", r.Code, r.Body.String())
	}
}

// A stranger's failed requests to the pages are throttled on their own budget, not the one sign-in and the rest of the API
// share: after a page's assets have all been refused, the same address can still sign in and get an ordinary 401 (not a 429).
func TestFailedPageRequestsDoNotUseUpTheSignInBudget(t *testing.T) {
	p := newPageRig(t)
	ip := fromIP("10.7.7.7")
	limited := false
	for i := 0; i < 40; i++ {
		if r := p.get(fusionGrafanaPath+"public/build/app.js", ip); r.Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("failed page requests are not throttled at all")
	}
	if r := p.do("GET", "/api/v1/auth/me", nil, ip); r.Code != 401 {
		t.Fatalf("the sign-in budget of the same address was spent by page requests: %d", r.Code)
	}
	if r := p.do("POST", "/api/v1/auth/login", map[string]string{"username": "root", "password": "wrong"}, ip); r.Code == http.StatusTooManyRequests {
		t.Fatalf("sign-in from the same address was throttled: %d", r.Code)
	}
}
