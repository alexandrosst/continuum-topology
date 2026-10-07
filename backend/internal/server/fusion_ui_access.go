package server

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Opening Prometheus or Grafana from the UI is a link into a new tab. The session cookie is SameSite=Strict, which a browser
// sends only when the navigation starts on the same site - and a new tab, a different spelling of the host (localhost and
// 127.0.0.1) or a link followed through a redirect does not count as that. The page then sees nobody signed in.
//
// So the UI asks for the link instead (POST /fusion/pages, an ordinary authenticated call that carries the session): the
// answer is the page's path with a ticket that works once, for thirty seconds. Redeeming it sets a second cookie that is good for
// these pages alone (Path=/fusion/), HttpOnly, and Lax - the one that has to come with a navigation from anywhere - and
// redirects to the page without the ticket. It reaches no other part of the server, the server's own session cookie stays
// Strict, and a person who stops being an administrator of FUSION's organisation loses the pages at their next request,
// because that is checked every time, not when the cookie was given.
//
// Nothing here is stored: a restart of the server asks the person to open the page again.

const (
	fusionUICookie     = "ikhnos_fusion_ui"
	fusionTicketParam  = "ikhnos_ticket"
	fusionTicketTTL    = 30 * time.Second
	fusionUISessionTTL = 8 * time.Hour
	// maxFusionUIGrants bounds what a loop of requests can make the server hold.
	maxFusionUIGrants = 4096
)

// fusionUIGrant is who a ticket or a page session is for.
type fusionUIGrant struct {
	userID, name string
	expires      time.Time
}

type fusionUIAccess struct {
	mu                sync.Mutex
	tickets, sessions map[string]fusionUIGrant // keyed by the hash of the secret
}

func newFusionUIAccess() *fusionUIAccess {
	return &fusionUIAccess{tickets: map[string]fusionUIGrant{}, sessions: map[string]fusionUIGrant{}}
}

// put stores a new grant under a fresh secret and returns it; "" when the table is full of live ones.
func (f *fusionUIAccess) put(m map[string]fusionUIGrant, g fusionUIGrant, now time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tickets)+len(f.sessions) >= maxFusionUIGrants {
		for _, t := range []map[string]fusionUIGrant{f.tickets, f.sessions} {
			for k, v := range t {
				if !v.expires.After(now) {
					delete(t, k)
				}
			}
		}
		if len(f.tickets)+len(f.sessions) >= maxFusionUIGrants {
			return ""
		}
	}
	secret, err := newSecret()
	if err != nil {
		return ""
	}
	m[string(HashSecret(secret))] = g
	return secret
}

// ticket makes a one-time ticket for a person.
func (f *fusionUIAccess) ticket(userID, name string, now time.Time) string {
	return f.put(f.tickets, fusionUIGrant{userID, name, now.Add(fusionTicketTTL)}, now)
}

// redeem uses a ticket up: it answers once, whatever the outcome.
func (f *fusionUIAccess) redeem(ticket string, now time.Time) (fusionUIGrant, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(HashSecret(ticket))
	g, ok := f.tickets[k]
	delete(f.tickets, k)
	return g, ok && g.expires.After(now)
}

// open starts a page session for who a ticket was redeemed by.
func (f *fusionUIAccess) open(g fusionUIGrant, now time.Time) string {
	g.expires = now.Add(fusionUISessionTTL)
	return f.put(f.sessions, g, now)
}

// session is who a page session cookie belongs to.
func (f *fusionUIAccess) session(secret string, now time.Time) (fusionUIGrant, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(HashSecret(secret))
	g, ok := f.sessions[k]
	if ok && !g.expires.After(now) {
		delete(f.sessions, k)
		return fusionUIGrant{}, false
	}
	return g, ok
}

func (f *fusionUIAccess) end(secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, string(HashSecret(secret)))
}

// fusionUISessionCaller is the person a page cookie stands for, if they are still an administrator of FUSION's organisation.
func (a *Admin) fusionUISessionCaller(r *http.Request) (fusionCaller, bool) {
	ck, err := r.Cookie(fusionUICookie)
	if err != nil || ck.Value == "" {
		return fusionCaller{}, false
	}
	g, ok := a.fusionUIAccess.session(ck.Value, a.C.Now())
	if !ok {
		return fusionCaller{}, false
	}
	m, err := a.C.Store.GetMembership(r.Context(), a.fusionOrg(), g.userID)
	if err != nil || roleRank[m.Role] < roleRank[RoleAdmin] {
		a.fusionUIAccess.end(ck.Value)
		return fusionCaller{}, false
	}
	return fusionCaller{Kind: "user", ID: g.userID, Name: g.name}, true
}

// redeemFusionTicket answers a request that carries a ticket: it sets the page cookie and sends the person to the same
// address without the ticket.
func (a *Admin) redeemFusionTicket(w http.ResponseWriter, r *http.Request, ticket string) {
	now := a.C.Now()
	g, ok := a.fusionUIAccess.redeem(ticket, now)
	if !ok {
		Metrics.authFailures.Add(1)
		if !a.authRL.Allow(LimitKey(a.clientIP(r))) {
			writeErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
			return
		}
		writeErr(w, http.StatusUnauthorized, "this link has expired - open the page again from Ikhnos")
		return
	}
	secret := a.fusionUIAccess.open(g, now)
	if secret == "" {
		writeErr(w, http.StatusServiceUnavailable, "too many pages are open, try again in a moment")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: fusionUICookie, Value: secret, Path: "/fusion/", MaxAge: int(fusionUISessionTTL / time.Second),
		HttpOnly: true, Secure: a.SecureCookies || a.secure(r), SameSite: http.SameSiteLaxMode})
	q := r.URL.Query()
	q.Del(fusionTicketParam)
	to := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to.String(), http.StatusSeeOther)
}

// openFusionPage is POST /fusion/pages {"page": "grafana" | "prometheus"}: the path to open, with a ticket in it.
func (a *Admin) openFusionPage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Page string `json:"page"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if a.Fusion == nil {
		a.fail(w, errf(KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return
	}
	if a.core(r).OrgID != a.fusionOrg() {
		a.fail(w, errf(KindForbidden, "FUSION's pages belong to this server's main organisation"))
		return
	}
	links := a.Fusion.uiLinks(a.Fusion.Status(r.Context()))
	var base string
	switch {
	case req.Page == "grafana" && links != nil:
		base = links.Grafana
	case req.Page == "prometheus" && links != nil:
		base = links.Prometheus
	case req.Page != "grafana" && req.Page != "prometheus":
		a.fail(w, errf(KindInvalid, "page must be grafana or prometheus"))
		return
	}
	if base == "" {
		a.fail(w, errf(KindConflict, "%s is not running yet", map[string]string{"grafana": "Grafana", "prometheus": "Prometheus"}[req.Page]))
		return
	}
	p := principal(r)
	t := a.fusionUIAccess.ticket(p.User.ID, p.User.Username, a.core(r).Now())
	if t == "" {
		a.fail(w, errf(KindConflict, "too many pages are open, try again in a moment"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"path": base + "?" + fusionTicketParam + "=" + t})
}

// ticketOf is the ticket on a request to a page, "" when there is none (or the request could not be one).
func ticketOf(r *http.Request) string {
	if r.Method != http.MethodGet || !strings.Contains(r.URL.RawQuery, fusionTicketParam+"=") {
		return ""
	}
	return r.URL.Query().Get(fusionTicketParam)
}
