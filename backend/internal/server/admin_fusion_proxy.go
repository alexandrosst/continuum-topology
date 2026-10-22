package server

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"continuum/internal/fusion"
	"continuum/internal/fusionpages"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
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
	fusionPromPath    = fusionpages.PromPath
	fusionGrafanaPath = fusionpages.GrafanaPath
	// A proxied page streams long queries and large responses; the server's own write timeout is for its JSON API.
	fusionProxyWindow = 5 * time.Minute
	// Prometheus's page injects its path prefix with an inline script, so it needs 'unsafe-inline'; everything else is this
	// origin only. Grafana sends its own policy, which is left alone.
	fusionProxyCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'"
)

// uiLinksOf are the pages of FUSION that are up, at the paths this server serves them at.
func uiLinksOf(f *fusion.FusionControl, st fusion.FusionStatus) *fusion.PageLinks {
	return f.PageLinks(st, fusionPromPath, fusionGrafanaPath)
}

func promUpstreamOf(f *fusion.FusionControl) string {
	if f.PrometheusUI != "" {
		return f.PrometheusUI
	}
	return "http://" + f.Name + "-prometheus." + f.Namespace + ".svc:9090"
}

func grafanaUpstreamOf(f *fusion.FusionControl) string {
	if f.GrafanaUI != "" {
		return f.GrafanaUI
	}
	return "http://" + f.Name + "-grafana." + f.Namespace + ".svc:3000"
}

// fusionUI is the reverse proxy for one of the pages. component is "metrics" or "grafana".
func (a *Admin) fusionUI(component string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Fusion == nil {
			httpkit.WriteErr(w, http.StatusServiceUnavailable, a.Fusion.Status(r.Context()).Message)
			return
		}
		if t := fusionpages.TicketOf(r); t != "" {
			a.redeemFusionTicket(w, r, t)
			return
		}
		who, err := a.fd().Caller(r)
		if err != nil || who.Kind != "user" {
			// The session cookie does not come with a navigation that starts elsewhere (a new tab); the page cookie does.
			if pu, ok := a.fusionUISessionCaller(r); ok {
				who, err = pu, nil
			}
		}
		if err != nil || who.Kind != "user" { // a FUSION access token reads data through the API; it does not get a web page
			kernel.Metrics.AuthFailures.Add(1)
			if !a.fusionPageRL.Allow(kernel.LimitKey(a.clientIP(r))) {
				httpkit.WriteErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
				return
			}
			if e, ok := err.(*kernel.Error); ok && e.Kind == kernel.KindForbidden {
				httpkit.WriteErr(w, http.StatusForbidden, e.Msg)
				return
			}
			httpkit.WriteErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if !a.fusionRL.Allow("ui|" + who.ID) {
			w.Header().Set("Retry-After", "5")
			httpkit.WriteErr(w, http.StatusTooManyRequests, "too many requests, slow down")
			return
		}
		if fusionpages.WantsUpgrade(r.Header) {
			httpkit.WriteErr(w, http.StatusBadRequest, "this page does not use WebSockets through the server")
			return
		}
		// The path as the page sees it. Anything that is not already in its plain form is refused: a request is judged on one
		// reading of its path, and must not be able to be read another way by the page.
		path := r.URL.Path
		if component == "metrics" {
			path = "/" + strings.TrimPrefix(path, fusionPromPath)
		}
		if strings.Contains(path, "//") || strings.Contains(path, "/./") || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") || strings.Contains(path, "\\") {
			httpkit.WriteErr(w, http.StatusBadRequest, "that path is not valid")
			return
		}
		if status, allow := fusionpages.Policy(component, r.Method, path); status != 0 {
			if allow != "" {
				w.Header().Set("Allow", allow)
			}
			httpkit.WriteErr(w, status, "this request is not allowed through the server")
			return
		}
		// A page of Grafana or Prometheus changes things with POST/PUT/DELETE: only from this origin.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && !a.originAllowed(o, r.Host) {
				httpkit.WriteErr(w, http.StatusForbidden, "request came from an origin this server does not trust")
				return
			}
			limit := int64(fusionpages.MaxGrafanaBody)
			if component == "metrics" {
				limit = fusionpages.MaxPromBody
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		st := a.Fusion.Status(ctx)
		cancel()
		if !st.Available || !st.ComponentUp(fusion.Component(component)) {
			httpkit.WriteErr(w, http.StatusServiceUnavailable, map[string]string{"metrics": "Prometheus", "grafana": "Grafana"}[component]+" is not running. Turn FUSION on and wait for it to be up.")
			return
		}
		base := promUpstreamOf(a.Fusion)
		if component == "grafana" {
			base = grafanaUpstreamOf(a.Fusion)
		}
		target, err := url.Parse(base)
		if err != nil {
			httpkit.WriteErr(w, http.StatusInternalServerError, "FUSION's address is not valid")
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
					h.Set("X-WEBAUTH-USER", fusionpages.GrafanaLogin(who.Name))
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				// (Grafana's own cookies are not needed: every request is signed in by the header.)
				fusionpages.ScrubResponse(resp.Header)
				// A redirect to the address the page believes it lives at is made relative to where the person really is.
				if loc := resp.Header.Get("Location"); loc != "" {
					safe, err := fusionpages.LocalLocation(loc, r.Host, target)
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
				httpkit.WriteErr(w, http.StatusBadGateway, "FUSION's page did not answer")
			},
		}
		rp.ServeHTTP(w, r)
	})
}
