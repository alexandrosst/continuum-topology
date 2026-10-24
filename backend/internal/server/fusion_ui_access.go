package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ikhnos/internal/accounts"
	"ikhnos/internal/fusionhttp"
	"ikhnos/internal/fusionpages"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/kernel"
)

// fusionUISessionCaller is the person a page cookie stands for, if they are still an administrator of FUSION's organisation
// and could still sign in: an account that has been disabled, or that must choose a new password before anything else (the
// same two refusals the guard (fusionhttp.Guard.Caller) makes for the session cookie), loses the pages at its next request too.
func (a *Admin) fusionUISessionCaller(r *http.Request) (fusionhttp.Caller, bool) {
	ck, err := r.Cookie(fusionpages.Cookie)
	if err != nil || ck.Value == "" {
		return fusionhttp.Caller{}, false
	}
	g, ok := a.fusionUIAccess.Session(ck.Value, a.C.Now())
	if !ok {
		return fusionhttp.Caller{}, false
	}
	u, err := a.C.Store.GetUser(r.Context(), g.UserID)
	if err != nil || u.DisabledAt != nil || u.MustChange {
		a.fusionUIAccess.End(ck.Value)
		return fusionhttp.Caller{}, false
	}
	m, err := a.C.Store.GetMembership(r.Context(), a.fusionOrg(), g.UserID)
	if err != nil || !accounts.Allows(accounts.Standing(m.Role), accounts.ActAdminister) {
		a.fusionUIAccess.End(ck.Value)
		return fusionhttp.Caller{}, false
	}
	return fusionhttp.Caller{Kind: "user", ID: g.UserID, Name: u.Username}, true
}

// endFusionPagesIn ends a person's page sessions when what changed was their standing in FUSION's organisation (the pages
// are that organisation's alone, so a change in any other one does not touch them).
func (a *Admin) endFusionPagesIn(r *http.Request, userID string) {
	if a.core(r).OrgID == a.fusionOrg() {
		a.fusionUIAccess.EndUser(userID)
	}
}

// clearFusionUICookie tells the browser to drop the page cookie, with the same name, path and flags it was set with.
func (a *Admin) clearFusionUICookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: fusionpages.Cookie, Value: "", Path: "/fusion/", MaxAge: -1,
		HttpOnly: true, Secure: a.SecureCookies || a.secure(r), SameSite: http.SameSiteLaxMode})
}

// redeemFusionTicket answers a request that carries a ticket: it sets the page cookie and sends the person to the same
// address without the ticket.
func (a *Admin) redeemFusionTicket(w http.ResponseWriter, r *http.Request, ticket string) {
	now := a.C.Now()
	secret, _, out := a.fusionUIAccess.RedeemAndOpen(ticket, now)
	switch out {
	case fusionpages.Invalid:
		kernel.Metrics.AuthFailures.Add(1)
		// Not the sign-in's limiter: a stranger's bad links must not use up the budget a person signing in from the same
		// address needs (see fusionPageRL).
		if !a.fusionPageRL.Allow(kernel.LimitKey(a.clientIP(r))) {
			httpkit.WriteErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
			return
		}
		httpkit.WriteErr(w, http.StatusUnauthorized, "this link has expired - open the page again from Ikhnos")
		return
	case fusionpages.Busy:
		// The ticket was not spent: the same link works again in a moment.
		httpkit.WriteErr(w, http.StatusServiceUnavailable, "too many pages are open, try again in a moment")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: fusionpages.Cookie, Value: secret, Path: "/fusion/", MaxAge: int(fusionpages.SessionTTL / time.Second),
		HttpOnly: true, Secure: a.SecureCookies || a.secure(r), SameSite: http.SameSiteLaxMode})
	q := r.URL.Query()
	q.Del(fusionpages.TicketParam)
	to := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to.String(), http.StatusSeeOther)
}

var (
	dashboardUID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	clusterRef   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

// openFusionPage is POST /fusion/pages {"page": "grafana" | "prometheus", "dashboard"?, "cluster"?}: the path to open, with a ticket in it.
func (a *Admin) openFusionPage(w http.ResponseWriter, r *http.Request) {
	var req openFusionPageRequest
	if err := httpkit.Decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if a.Fusion == nil {
		a.fail(w, kernel.Errf(kernel.KindConflict, "%s", a.Fusion.Status(r.Context()).Message))
		return
	}
	if a.core(r).OrgID != a.fusionOrg() {
		a.fail(w, kernel.Errf(kernel.KindForbidden, "FUSION's pages belong to this server's main organisation"))
		return
	}
	links := uiLinksOf(a.Fusion, a.Fusion.Status(r.Context()))
	var base string
	switch {
	case req.Page == "grafana" && links != nil:
		base = links.Grafana
	case req.Page == "prometheus" && links != nil:
		base = links.Prometheus
	case req.Page != "grafana" && req.Page != "prometheus":
		a.fail(w, kernel.Errf(kernel.KindInvalid, "page must be grafana or prometheus"))
		return
	}
	if base == "" {
		a.fail(w, kernel.Errf(kernel.KindConflict, "%s is not running yet", map[string]string{"grafana": "Grafana", "prometheus": "Prometheus"}[req.Page]))
		return
	}
	// Where in the page: a shipped dashboard, and the cluster its picker starts on. Both end up in an address, so each is held to the few
	// characters a dashboard uid and a cluster id are made of.
	query := ""
	switch {
	case req.Dashboard == "" && req.Cluster == "":
	case req.Page != "grafana":
		a.fail(w, kernel.Errf(kernel.KindInvalid, "a dashboard can only be asked for with page grafana"))
		return
	case req.Dashboard == "" || !dashboardUID.MatchString(req.Dashboard) || (req.Cluster != "" && !clusterRef.MatchString(req.Cluster)):
		a.fail(w, kernel.Errf(kernel.KindInvalid, "dashboard must be a dashboard uid (lowercase letters, digits and hyphens), and cluster a cluster id"))
		return
	default:
		base = strings.TrimSuffix(base, "/") + "/d/" + req.Dashboard
		if req.Cluster != "" {
			query = "var-cluster=" + url.QueryEscape(req.Cluster) + "&"
		}
	}
	p := httpkit.Principal(r)
	t := a.fusionUIAccess.Ticket(p.User.ID, p.User.Username, a.core(r).Now())
	if t == "" {
		a.fail(w, kernel.Errf(kernel.KindConflict, "too many pages are open, try again in a moment"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpkit.WriteJSON(w, 200, fusionPageDoc{Path: base + "?" + query + fusionpages.TicketParam + "=" + t})
}
