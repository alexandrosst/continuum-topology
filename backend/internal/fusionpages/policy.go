package fusionpages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The paths the pages are served at.
const (
	PromPath    = "/fusion/prometheus/"
	GrafanaPath = "/fusion/grafana/"
)

// What the pages may be asked to do. The server's own session is the credential, so the proxy is the whole of the
// access control: a page is for looking, and the doors that write data into FUSION, change its configuration or stop it
// stay shut whatever the page, or someone who got a script into it, asks for.
const (
	// Prometheus's UI and its API are read with GET. A long PromQL expression does not fit in a URL, so the query
	// endpoints also take a POST; nothing else does.
	MaxPromBody = 1 << 20
	// Grafana saves dashboards and settings, so it takes the whole set of methods a web page uses.
	MaxGrafanaBody = 16 << 20
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

// grafanaLoginPrefix namespaces the people Ikhnos signs in to Grafana, so that no Ikhnos username can ever be the login of a
// Grafana user that is not theirs.
const grafanaLoginPrefix = "ikhnos-"

// ErrUnsafeRedirect is a redirect that would take the person somewhere other than FUSION's own page.
var ErrUnsafeRedirect = errors.New("a redirect to somewhere else")

// Policy decides whether a page's request may be proxied: method and path, where path is what the page itself
// would see (Prometheus's with our prefix removed, Grafana's under its sub path). It answers the status to refuse with,
// 0 to allow, and the methods to offer on a 405.
func Policy(component, method, path string) (status int, allow string) {
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
	rel := strings.TrimPrefix(path, strings.TrimSuffix(GrafanaPath, "/"))
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

// WantsUpgrade is a request to switch protocols (a WebSocket). Neither page needs one here - Grafana Live is off, and
// Prometheus has none - and a tunnel is the one thing the proxy's checks of method and path cannot look inside.
func WantsUpgrade(h http.Header) bool {
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

// LocalLocation makes a redirect from a page safe to hand to the browser: to a place on this server, only. The page
// believes it lives at localhost (Prometheus's external URL, Grafana's root URL) or at its own Service, and says so in
// absolute redirects; those become relative. Anything pointing at another host - or written so that a browser would read
// it as one ("//host", "/\host") - is refused rather than passed on, so a page can never be made to bounce a signed-in
// administrator to another site.
func LocalLocation(loc, requestHost string, upstream *url.URL) (string, error) {
	u, err := url.Parse(loc)
	if err != nil {
		return "", ErrUnsafeRedirect
	}
	if u.Scheme != "" || u.Host != "" {
		switch u.Host {
		case requestHost, upstream.Host, "localhost:3000", "localhost:9090":
			if u.Scheme == "http" || u.Scheme == "https" {
				return u.RequestURI(), nil
			}
		}
		return "", ErrUnsafeRedirect
	}
	if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, `/\`) || strings.HasPrefix(loc, `\`) {
		return "", ErrUnsafeRedirect
	}
	return loc, nil
}

// ScrubResponse removes what must not go from a page to the browser, and sets what must be there. The page is served from
// this server's origin, so what it sends is held to what this server would send itself.
func ScrubResponse(h http.Header) {
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

// GrafanaLogin is the login Grafana is told a signed-in person has (X-WEBAUTH-USER): the Ikhnos username under a prefix.
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
func GrafanaLogin(username string) string {
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
