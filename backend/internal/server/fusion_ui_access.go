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
	// maxFusionUIGrants bounds what a loop of requests can make the server hold: tickets and page sessions each have a
	// table of this size. They are counted apart so that a table of sessions that is full never costs a ticket (see
	// redeemAndOpen).
	maxFusionUIGrants = 4096
	// maxFusionUIGrantsPerUser bounds what one person can hold of each kind. Opening a page is something an administrator
	// does a few times a day; a script (or a stolen cookie) looping on it would otherwise fill the whole table and shut every
	// other administrator out of the pages until the entries expire. Past the cap the person's own oldest is dropped.
	maxFusionUIGrantsPerUser = 16
	// fusionUISweepEvery is how often expired grants are cleared out even when the table is not full.
	fusionUISweepEvery = time.Minute
)

// fusionUIGrant is who a ticket or a page session is for.
type fusionUIGrant struct {
	userID, name string
	expires      time.Time
	seq          uint64 // the order grants were made in, to tell a person's oldest (see makeRoomLocked)
}

type fusionUIAccess struct {
	mu                sync.Mutex
	tickets, sessions map[string]fusionUIGrant // keyed by the hash of the secret
	swept             time.Time                // when expired grants were last cleared out
	seq               uint64
}

func newFusionUIAccess() *fusionUIAccess {
	return &fusionUIAccess{tickets: map[string]fusionUIGrant{}, sessions: map[string]fusionUIGrant{}}
}

// sweepLocked drops what has expired, at most once every fusionUISweepEvery unless force is set (a table that is full
// must look at once). Without this an expired entry stayed until the table filled up, so the table's size said nothing
// about how many pages were really open. f.mu is held.
func (f *fusionUIAccess) sweepLocked(now time.Time, force bool) {
	if !force && now.Sub(f.swept) < fusionUISweepEvery {
		return
	}
	f.swept = now
	for _, t := range []map[string]fusionUIGrant{f.tickets, f.sessions} {
		for k, v := range t {
			if !v.expires.After(now) {
				delete(t, k)
			}
		}
	}
}

// makeRoomLocked gets m ready to take one more grant for userID: expired ones are swept, the person's oldest is evicted
// if they already hold the most they may, and it says whether there is room in the table. f.mu is held.
func (f *fusionUIAccess) makeRoomLocked(m map[string]fusionUIGrant, userID string, now time.Time) bool {
	f.sweepLocked(now, false)
	if len(m) >= maxFusionUIGrants {
		f.sweepLocked(now, true)
	}
	for {
		var oldest string
		var oldestSeq uint64
		n := 0
		for k, v := range m {
			if v.userID != userID {
				continue
			}
			if n++; oldest == "" || v.seq < oldestSeq {
				oldest, oldestSeq = k, v.seq
			}
		}
		if n < maxFusionUIGrantsPerUser {
			break
		}
		delete(m, oldest)
	}
	return len(m) < maxFusionUIGrants
}

// put stores a new grant under a fresh secret and returns it; "" when the table is full of live ones.
func (f *fusionUIAccess) put(m map[string]fusionUIGrant, g fusionUIGrant, now time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.makeRoomLocked(m, g.userID, now) {
		return ""
	}
	secret, err := newSecret()
	if err != nil {
		return ""
	}
	f.seq++
	g.seq = f.seq
	m[string(HashSecret(secret))] = g
	return secret
}

// ticket makes a one-time ticket for a person.
func (f *fusionUIAccess) ticket(userID, name string, now time.Time) string {
	return f.put(f.tickets, fusionUIGrant{userID: userID, name: name, expires: now.Add(fusionTicketTTL)}, now)
}

// fusionTicketOutcome is what redeemAndOpen found.
type fusionTicketOutcome int

const (
	fusionTicketOK      fusionTicketOutcome = iota // the ticket is used up and a page session is open
	fusionTicketInvalid                            // unknown, used or expired (and gone either way)
	fusionTicketBusy                               // good, but there is no room for a page session: the ticket is kept
)

// redeemAndOpen is what a request carrying a ticket does: it uses the ticket up and opens the page session it is for,
// as one step. The room for the session is made BEFORE the ticket is spent, so a table that has no room (or a secret that
// could not be made) answers "busy" and leaves the ticket as it was - the person's retry a moment later still works,
// instead of finding a link that was burnt without giving them anything. An unknown, used or expired ticket answers
// "invalid"; it is deleted, so it answers once whatever the outcome.
func (f *fusionUIAccess) redeemAndOpen(ticket string, now time.Time) (secret string, who fusionUIGrant, out fusionTicketOutcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(HashSecret(ticket))
	g, ok := f.tickets[k]
	if !ok {
		return "", fusionUIGrant{}, fusionTicketInvalid
	}
	if !g.expires.After(now) {
		delete(f.tickets, k)
		return "", fusionUIGrant{}, fusionTicketInvalid
	}
	if !f.makeRoomLocked(f.sessions, g.userID, now) {
		return "", g, fusionTicketBusy
	}
	secret, err := newSecret()
	if err != nil {
		return "", g, fusionTicketBusy
	}
	delete(f.tickets, k)
	g.expires = now.Add(fusionUISessionTTL)
	f.seq++
	g.seq = f.seq
	f.sessions[string(HashSecret(secret))] = g
	return secret, g, fusionTicketOK
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

// endUser ends every page session and ticket a person holds. It is what signing out, a new password, and losing the role
// that gives access (or the membership itself) do: the page cookie is checked on every request anyway, but a grant that is
// not removed would come back to life if the person were made an administrator again, and until then it would sit in the
// table. Safe on a nil table (an Admin that was never given its handler).
func (f *fusionUIAccess) endUser(userID string) {
	if f == nil || userID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range []map[string]fusionUIGrant{f.tickets, f.sessions} {
		for k, v := range t {
			if v.userID == userID {
				delete(t, k)
			}
		}
	}
}

// fusionUISessionCaller is the person a page cookie stands for, if they are still an administrator of FUSION's organisation
// and could still sign in: an account that has been disabled, or that must choose a new password before anything else (the
// same two refusals fusionCaller makes for the session cookie), loses the pages at its next request too.
func (a *Admin) fusionUISessionCaller(r *http.Request) (fusionCaller, bool) {
	ck, err := r.Cookie(fusionUICookie)
	if err != nil || ck.Value == "" {
		return fusionCaller{}, false
	}
	g, ok := a.fusionUIAccess.session(ck.Value, a.C.Now())
	if !ok {
		return fusionCaller{}, false
	}
	u, err := a.C.Store.GetUser(r.Context(), g.userID)
	if err != nil || u.DisabledAt != nil || u.MustChange {
		a.fusionUIAccess.end(ck.Value)
		return fusionCaller{}, false
	}
	m, err := a.C.Store.GetMembership(r.Context(), a.fusionOrg(), g.userID)
	if err != nil || roleRank[m.Role] < roleRank[RoleAdmin] {
		a.fusionUIAccess.end(ck.Value)
		return fusionCaller{}, false
	}
	return fusionCaller{Kind: "user", ID: g.userID, Name: u.Username}, true
}

// endFusionPagesIn ends a person's page sessions when what changed was their standing in FUSION's organisation (the pages
// are that organisation's alone, so a change in any other one does not touch them).
func (a *Admin) endFusionPagesIn(r *http.Request, userID string) {
	if a.core(r).OrgID == a.fusionOrg() {
		a.fusionUIAccess.endUser(userID)
	}
}

// clearFusionUICookie tells the browser to drop the page cookie, with the same name, path and flags it was set with.
func (a *Admin) clearFusionUICookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: fusionUICookie, Value: "", Path: "/fusion/", MaxAge: -1,
		HttpOnly: true, Secure: a.SecureCookies || a.secure(r), SameSite: http.SameSiteLaxMode})
}

// redeemFusionTicket answers a request that carries a ticket: it sets the page cookie and sends the person to the same
// address without the ticket.
func (a *Admin) redeemFusionTicket(w http.ResponseWriter, r *http.Request, ticket string) {
	now := a.C.Now()
	secret, _, out := a.fusionUIAccess.redeemAndOpen(ticket, now)
	switch out {
	case fusionTicketInvalid:
		Metrics.authFailures.Add(1)
		// Not the sign-in's limiter: a stranger's bad links must not use up the budget a person signing in from the same
		// address needs (see fusionPageRL).
		if !a.fusionPageRL.Allow(LimitKey(a.clientIP(r))) {
			writeErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
			return
		}
		writeErr(w, http.StatusUnauthorized, "this link has expired - open the page again from Ikhnos")
		return
	case fusionTicketBusy:
		// The ticket was not spent: the same link works again in a moment.
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
