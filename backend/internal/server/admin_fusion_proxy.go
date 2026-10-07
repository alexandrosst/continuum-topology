package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The two pages FUSION has of its own - Prometheus's and Grafana's - are opened from the Ikhnos UI through the server, so
// they need no Ingress, no second login and no port-forward, and the stores stay inside the cluster. The
// server is the only way in: it signs the person in (an administrator of FUSION's organisation, by the session cookie, or - for the new tab the UI opens, which carries no session cookie - by the page cookie a one-time ticket gives, see fusion_ui_access.go),
// strips what must not reach the other side (their Ikhnos cookie and any credentials) and, for Grafana, tells it who
// they are in the one header it trusts (X-WEBAUTH-USER).
//
// The trust that implies, stated plainly: these pages are served from the same origin as the UI. Both are our own
// provisioned instances talking only to our own stores, they are reachable by administrators only, and Grafana's
// content-security-policy is on - but a script that got to run inside them could call this server's API as the signed-in
// administrator. Turn them off (grafana.enabled=false in the chart; the Prometheus page needs no switch, it is only a link)
// if that is not acceptable.

const (
	fusionPromPath    = "/fusion/prometheus/"
	fusionGrafanaPath = "/fusion/grafana/"
	// A proxied page streams long queries and large responses; the server's own write timeout is for its JSON API.
	fusionProxyWindow = 5 * time.Minute
	// Prometheus's page injects its path prefix with an inline script, so it needs 'unsafe-inline'; everything else is this
	// origin only. Grafana sends its own policy, which is left alone.
	fusionProxyCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'"
)

// What the pages may be asked to do. The server's own session is the credential, so the proxy is the whole of the
// access control: a page is for looking, and the doors that write data into FUSION, change its configuration or stop it
// stay shut whatever the page, or someone who got a script into it, asks for.
const (
	// Prometheus's UI and its API are read with GET. A long PromQL expression does not fit in a URL, so the query
	// endpoints also take a POST; nothing else does.
	maxPromBody = 1 << 20
	// Grafana saves dashboards and settings, so it takes the whole set of methods a web page uses.
	maxGrafanaBody = 16 << 20
)

var (
	promPostable = map[string]bool{
		"/api/v1/query": true, "/api/v1/query_range": true, "/api/v1/series": true, "/api/v1/labels": true, "/api/v1/query_exemplars": true,
	}
	// promDenied are Prometheus paths that are never reachable through the server: the OTLP and remote-write receivers
	// (writing data in would bypass the gateway's mutual TLS and a token's scope), remote read, the admin API (delete
	// series, snapshots), the lifecycle endpoints (/-/reload, /-/quit) and the Go profiler.
	promDenied = []string{"/api/v1/otlp", "/api/v1/write", "/api/v1/read", "/api/v1/admin", "/-/", "/debug/"}
	// grafanaDenied is Grafana Live, a WebSocket push channel that is switched off in the chart (GF_LIVE_MAX_CONNECTIONS).
	grafanaDenied = []string{"/api/live/"}
)

// fusionProxyPolicy decides whether a page's request may be proxied: method and path, where path is what the page itself
// would see (Prometheus's with our prefix removed, Grafana's under its sub path). It answers the status to refuse with,
// 0 to allow, and the methods to offer on a 405.
func fusionProxyPolicy(component, method, path string) (status int, allow string) {
	if component == "metrics" {
		for _, d := range promDenied {
			if path == strings.TrimSuffix(d, "/") || strings.HasPrefix(path, d) {
				return http.StatusForbidden, ""
			}
		}
		switch {
		case method == http.MethodGet || method == http.MethodHead:
			return 0, ""
		case method == http.MethodPost && promPostable[path]:
			return 0, ""
		}
		if promPostable[path] {
			return http.StatusMethodNotAllowed, "GET, HEAD, POST"
		}
		return http.StatusMethodNotAllowed, "GET, HEAD"
	}
	rel := strings.TrimPrefix(path, strings.TrimSuffix(fusionGrafanaPath, "/"))
	for _, d := range grafanaDenied {
		if strings.HasPrefix(rel, d) {
			return http.StatusForbidden, ""
		}
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return 0, ""
	}
	return http.StatusMethodNotAllowed, "GET, HEAD, POST, PUT, PATCH, DELETE"
}

// wantsUpgrade is a request to switch protocols (a WebSocket). Neither page needs one here - Grafana Live is off, and
// Prometheus has none - and a tunnel is the one thing the proxy's checks of method and path cannot look inside.
func wantsUpgrade(h http.Header) bool {
	if h.Get("Upgrade") != "" {
		return true
	}
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// errUnsafeRedirect is a redirect that would take the person somewhere other than FUSION's own page.
var errUnsafeRedirect = errors.New("a redirect to somewhere else")

// localLocation makes a redirect from a page safe to hand to the browser: to a place on this server, only. The page
// believes it lives at localhost (Prometheus's external URL, Grafana's root URL) or at its own Service, and says so in
// absolute redirects; those become relative. Anything pointing at another host - or written so that a browser would read
// it as one ("//host", "/\host") - is refused rather than passed on, so a page can never be made to bounce a signed-in
// administrator to another site.
func localLocation(loc, requestHost string, upstream *url.URL) (string, error) {
	u, err := url.Parse(loc)
	if err != nil {
		return "", errUnsafeRedirect
	}
	if u.Scheme != "" || u.Host != "" {
		switch u.Host {
		case requestHost, upstream.Host, "localhost:3000", "localhost:9090":
			if u.Scheme == "http" || u.Scheme == "https" {
				return u.RequestURI(), nil
			}
		}
		return "", errUnsafeRedirect
	}
	if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, `/\`) || strings.HasPrefix(loc, `\`) {
		return "", errUnsafeRedirect
	}
	return loc, nil
}

// scrubResponse removes what must not go from a page to the browser, and sets what must be there. The page is served from
// this server's origin, so what it sends is held to what this server would send itself.
func scrubResponse(h http.Header) {
	// Cookies would give the page's origin state of its own; CORS headers would open it to other sites (Prometheus
	// answers any Origin); the rest are about the page's own hosting, which this is not.
	for _, k := range []string{"Set-Cookie", "Set-Cookie2", "Server", "X-Powered-By", "Via", "Alt-Svc", "Strict-Transport-Security",
		"Clear-Site-Data", "Public-Key-Pins", "Report-To", "Nel", "Content-Security-Policy-Report-Only", "Pragma", "Expires"} {
		h.Del(k)
	}
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			h.Del(k)
		}
	}
	// The server's own value of these is already on the response (securityHeaders); a page's would only repeat or contradict it.
	for _, k := range []string{"X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy", "Cross-Origin-Opener-Policy", "Permissions-Policy"} {
		h.Del(k)
	}
}

// grafanaLoginPrefix namespaces the people Ikhnos signs in to Grafana, so that no Ikhnos username can ever be the login of a
// Grafana user that is not theirs.
const grafanaLoginPrefix = "ikhnos-"

// grafanaLogin is the login Grafana is told a signed-in person has (X-WEBAUTH-USER): the Ikhnos username under a prefix.
// Sent as it was, the first account every server has - "admin", Ikhnos's bootstrap user - would be Grafana's own built-in
// administrator, and Grafana would sign the person in as that Server Admin whatever role they were given. The prefix keeps
// the two name spaces apart: "ikhnos-admin" is an ordinary user Grafana creates on first sight, with the role it is
// configured to give people who arrive this way.
//
// The name is kept to what a Grafana login can safely hold - lowercase letters, digits and . @ - _ - which is every
// character an Ikhnos username may have (see usernameRe), lowercased because Ikhnos treats names that differ only in case as
// one account, and so does Grafana. Anything else (it cannot arise from a valid username; this is only so a name that
// somehow has one can never put a stray character into the header) is written as +hex+, and "+" itself is written that way
// too, so no two names give the same login.
func grafanaLogin(username string) string {
	var b strings.Builder
	b.WriteString(grafanaLoginPrefix)
	for _, r := range strings.ToLower(username) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '@', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteString("+" + strconv.FormatInt(int64(r), 16) + "+")
		}
	}
	return b.String()
}

// fusionUILinks are the paths the UI opens, present only for a page that is up right now.
type fusionUILinks struct {
	Prometheus string `json:"prometheus,omitempty"`
	Grafana    string `json:"grafana,omitempty"`
}

func (f *FusionControl) uiLinks(st FusionStatus) *fusionUILinks {
	if f == nil || !st.Available {
		return nil
	}
	var l fusionUILinks
	if st.componentUp("metrics") {
		l.Prometheus = fusionPromPath
	}
	if st.componentUp("grafana") {
		l.Grafana = fusionGrafanaPath
	}
	if l == (fusionUILinks{}) {
		return nil
	}
	return &l
}

func (f *FusionControl) promUpstream() string {
	if f.PrometheusUI != "" {
		return f.PrometheusUI
	}
	return "http://" + f.Name + "-prometheus." + f.Namespace + ".svc:9090"
}

func (f *FusionControl) grafanaUpstream() string {
	if f.GrafanaUI != "" {
		return f.GrafanaUI
	}
	return "http://" + f.Name + "-grafana." + f.Namespace + ".svc:3000"
}

// fusionUI is the reverse proxy for one of the pages. component is "metrics" or "grafana".
func (a *Admin) fusionUI(component string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Fusion == nil {
			writeErr(w, http.StatusServiceUnavailable, a.Fusion.Status(r.Context()).Message)
			return
		}
		if t := ticketOf(r); t != "" {
			a.redeemFusionTicket(w, r, t)
			return
		}
		who, err := a.fusionCaller(r)
		if err != nil || who.Kind != "user" {
			// The session cookie does not come with a navigation that starts elsewhere (a new tab); the page cookie does.
			if pu, ok := a.fusionUISessionCaller(r); ok {
				who, err = pu, nil
			}
		}
		if err != nil || who.Kind != "user" { // a FUSION access token reads data through the API; it does not get a web page
			Metrics.authFailures.Add(1)
			if !a.fusionPageRL.Allow(LimitKey(a.clientIP(r))) {
				writeErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
				return
			}
			if e, ok := err.(*Error); ok && e.Kind == KindForbidden {
				writeErr(w, http.StatusForbidden, e.Msg)
				return
			}
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if !a.fusionRL.Allow("ui|" + who.ID) {
			w.Header().Set("Retry-After", "5")
			writeErr(w, http.StatusTooManyRequests, "too many requests, slow down")
			return
		}
		if wantsUpgrade(r.Header) {
			writeErr(w, http.StatusBadRequest, "this page does not use WebSockets through the server")
			return
		}
		// The path as the page sees it. Anything that is not already in its plain form is refused: a request is judged on one
		// reading of its path, and must not be able to be read another way by the page.
		path := r.URL.Path
		if component == "metrics" {
			path = "/" + strings.TrimPrefix(path, fusionPromPath)
		}
		if strings.Contains(path, "//") || strings.Contains(path, "/./") || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") || strings.Contains(path, "\\") {
			writeErr(w, http.StatusBadRequest, "that path is not valid")
			return
		}
		if status, allow := fusionProxyPolicy(component, r.Method, path); status != 0 {
			if allow != "" {
				w.Header().Set("Allow", allow)
			}
			writeErr(w, status, "this request is not allowed through the server")
			return
		}
		// A page of Grafana or Prometheus changes things with POST/PUT/DELETE: only from this origin.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && !a.originAllowed(o, r.Host) {
				writeErr(w, http.StatusForbidden, "request came from an origin this server does not trust")
				return
			}
			limit := int64(maxGrafanaBody)
			if component == "metrics" {
				limit = maxPromBody
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		st := a.Fusion.Status(ctx)
		cancel()
		if !st.Available || !st.componentUp(component) {
			writeErr(w, http.StatusServiceUnavailable, map[string]string{"metrics": "Prometheus", "grafana": "Grafana"}[component]+" is not running. Turn FUSION on and wait for it to be up.")
			return
		}
		base := a.Fusion.promUpstream()
		if component == "grafana" {
			base = a.Fusion.grafanaUpstream()
		}
		target, err := url.Parse(base)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "FUSION's address is not valid")
			return
		}
		// Our own policy was set before we got here; the other side's, if it has one, replaces it.
		w.Header().Del("Content-Security-Policy")
		w.Header().Del("Cache-Control")
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(fusionProxyWindow))

		// The page is a long-lived thing (a query that runs for minutes), but not an unbounded one.
		ctx, stop := context.WithTimeout(r.Context(), fusionProxyWindow)
		defer stop()
		r = r.WithContext(ctx)

		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Host = pr.In.Host // so a page that checks its own origin sees the one the person is on
				if component == "metrics" {
					// Prometheus runs with --web.route-prefix=/ and only knows the prefix for the links it writes.
					pr.Out.URL.Path = "/" + strings.TrimPrefix(pr.In.URL.Path, fusionPromPath)
					if pr.In.URL.RawPath != "" {
						pr.Out.URL.RawPath = "/" + strings.TrimPrefix(pr.In.URL.RawPath, fusionPromPath)
					}
				}
				// (The proxy itself drops the hop-by-hop headers, and with Rewrite also Forwarded and X-Forwarded-*: what
				// the person's own proxy said about them is not a claim to repeat to the page.)
				h := pr.Out.Header
				// Nothing of ours goes through: not the person's Ikhnos session, not credentials, not forwarding claims -
				// and above all not a sign-in header of the client's own making.
				h.Del("Cookie")
				h.Del("Authorization")
				h.Del("Proxy-Authorization")
				h.Del("X-Requested-With")
				// The method was judged on the request line. A framework that lets a header replace it (the common
				// X-HTTP-Method-Override family) would turn an allowed POST into a DELETE or PUT after the check.
				h.Del("X-HTTP-Method-Override")
				h.Del("X-HTTP-Method")
				h.Del("X-Method-Override")
				for k := range h {
					if strings.HasPrefix(strings.ToLower(k), "x-webauth-") {
						h.Del(k)
					}
				}
				if component == "grafana" {
					h.Set("X-WEBAUTH-USER", grafanaLogin(who.Name))
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				// (Grafana's own cookies are not needed: every request is signed in by the header.)
				scrubResponse(resp.Header)
				// A redirect to the address the page believes it lives at is made relative to where the person really is.
				if loc := resp.Header.Get("Location"); loc != "" {
					safe, err := localLocation(loc, r.Host, target)
					if err != nil {
						return err
					}
					resp.Header.Set("Location", safe)
				}
				if resp.Header.Get("Content-Security-Policy") == "" {
					resp.Header.Set("Content-Security-Policy", fusionProxyCSP)
				}
				resp.Header.Set("Cache-Control", "no-store")
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				writeErr(w, http.StatusBadGateway, "FUSION's page did not answer")
			},
		}
		rp.ServeHTTP(w, r)
	})
}
