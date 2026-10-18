package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/accountsapi"
	"continuum/internal/chart"
	"continuum/internal/decisionloop"
	"continuum/internal/estate"
	"continuum/internal/fusion"
	"continuum/internal/fusionapi"
	"continuum/internal/httpkit"
	"continuum/internal/install"
	"continuum/internal/installcfg"
	"continuum/internal/kernel"
	"continuum/internal/probe"
	"continuum/internal/settingsapi"
	"continuum/internal/store"
	"continuum/internal/telemetry"
	"continuum/internal/twin"
)

// Admin is the JSON API the UI talks to. It listens on its own port, separate from the agent
// port. Everything except sign-in needs a session cookie, and what a person may do depends on
// their role.
type Admin struct {
	routes []Route // what Handler mounted under /api, and what each asks of the caller (routes.go)

	// SelfProbe receives the reports of the node probes that ship with the server's own chart, and SelfProbeEvery is how
	// often they were told to report. Both are nil/zero on an install without those probes. See self_probe.go.
	SelfProbe      *probe.Receiver
	SelfProbeEvery time.Duration

	P *Platform
	C *Core // the platform-wide core (accounts, sign-in); an organisation's own is a.core(r)
	// Settings is how the server writes the commands it prints (the chart, the images, the address agents dial): see installcfg. Its
	// fields are promoted, so a.ChartRef and a.AgentAddr are the server's flags.
	installcfg.Settings
	Origins []string // browsers allowed cross-origin (dev); empty = same origin only
	UIDir   string   // built UI to serve, optional
	Version string
	// Fusion switches the FUSION bundled with the server's chart on and off. Nil when the server was installed without
	// it or without the permission to scale it; the endpoints then report it as unavailable and say why.
	Fusion *fusion.FusionControl
	// extras replaces the platform-backed provider of what Ikhnos knows (topology, applications, events) for a test.
	extras fusionapi.Extras
	// appCache holds the Ikhnos applications for a few seconds: working them out builds the whole state document (every agent
	// and the topology) and a dashboard or a script reading the API many times a second would otherwise redo it each time.
	appCache appGroupCache
	// linkReadings shares one read of the link figures in FUSION between everyone who has the Sites screen open.
	linkReadings linkReadingsCache
	// TrustProxy is set when a TLS-terminating proxy sits in front: the client address is read from
	// the last entry of X-Forwarded-For (what the proxy itself saw), and the request counts as HTTPS
	// when the proxy says so in X-Forwarded-Proto. Only correct when the proxy is the sole way in and
	// replaces those headers; otherwise a client could forge its address and the scheme.
	TrustProxy bool
	// SSOHeaderName, when set, turns on trusted-header SSO: a GET to /api/v1/auth/sso reads the caller's
	// asserted identity from this request header and, if it names an existing, enabled account, signs
	// that account in with no password or second factor - because whatever sits in front of us (an
	// OAuth2 proxy, an Envoy/Istio ext_authz filter, an IdP-integrated gateway) has already verified who
	// they are. This is a much stronger trust than TrustProxy's client-address reading: it hands out
	// real sessions on the header's say-so alone. Only set it when that proxy is the sole way to reach
	// this port AND it always overwrites this header on the way in - never merely adds to it - for every
	// request, authenticated or not; otherwise anyone who can reach this port directly can sign in as
	// whoever they like just by setting it themselves. It never creates or changes an account: the name
	// must already match an existing username exactly as a password sign-in's username would.
	SSOHeaderName string
	// SecureCookies marks the session cookie Secure (and gives it the __Host- prefix) on every response,
	// which the server sets whenever the admin listener is not on loopback: a browser then never sends
	// the session over plain HTTP. On loopback the cookie is Secure only when the request itself was HTTPS.
	SecureCookies bool
	authRL        *kernel.Limiter
	// hbFailRL throttles failed regional-operator heartbeat authentications per address (see operatorHeartbeat).
	hbFailRL *kernel.Limiter
	// probes remembers the last look at each operator's address (see checkOperatorAddress); probeDial replaces the
	// network in tests.
	probes    addressProbes
	probeDial func(ctx context.Context, network, addr string) (net.Conn, error)
	// fusionRL limits how fast one FUSION data-API caller (a token or a person) may read.
	fusionRL *kernel.Limiter
	// fusionPageRL throttles failed authentications on the Prometheus and Grafana pages per address. It is its own limiter, not
	// authRL's: a page makes a request for every asset it loads, so a stranger (or a browser tab whose cookie just ended)
	// can run through a budget in seconds, and on a shared limiter that would lock the sign-in out for everyone behind the
	// same address - and the other way round.
	fusionPageRL *kernel.Limiter
	// fusionUIAccess holds the one-time tickets and the page-only sessions of the Prometheus and Grafana pages (see fusion_ui_access.go).
	fusionUIAccess *fusionUIAccess
	// Readiness says what /readyz checks besides the database; nil checks only the database.
	Readiness *Readiness
}

const (
	cookieName = "continuum_session"
	// hostCookieName is the session cookie's name when it is Secure. The __Host- prefix makes a browser
	// refuse a cookie of that name unless it is Secure, has Path=/ and no Domain, so a sibling
	// subdomain or a plain-HTTP response cannot plant or overwrite it.
	hostCookieName = "__Host-" + cookieName
)

type ctxTenant struct{}

func (a *Admin) tn(r *http.Request) *Tenant {
	t, _ := r.Context().Value(ctxTenant{}).(*Tenant)
	return t
}

func (a *Admin) core(r *http.Request) *Core { return a.tn(r).C }

func (a *Admin) Handler() http.Handler {
	a.authRL = kernel.NewLimiter(30, 10)
	a.hbFailRL = kernel.NewLimiter(30, 10)
	a.fusionRL = kernel.NewLimiter(600, 60)
	a.fusionPageRL = kernel.NewLimiter(30, 10)
	a.fusionUIAccess = newFusionUIAccess()
	mux := http.NewServeMux()
	a.handle(mux, "GET /healthz", accessPublic, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	a.handle(mux, "GET /readyz", accessPublic, http.HandlerFunc(a.readyz))
	a.handle(mux, "GET /charts/{file}", accessPublic, http.HandlerFunc(a.serveChart))

	api := http.NewServeMux()
	route := func(pattern string, n httpkit.Need, h http.HandlerFunc) {
		a.handle(api, pattern, n.String(), a.guard(n, h))
	}
	open := func(pattern string, h http.HandlerFunc) { a.handle(api, pattern, accessPublic, h) }
	routes := routeFuncs{route, open}
	(&accountsapi.API{
		Accounts: a.C.Accounts,
		For:      func(r *http.Request) *accounts.Service { return a.core(r).Accounts },
		Edge: accountsapi.Edge{
			ClientIP: a.clientIP, SetCookie: a.setCookie, SessionCookie: sessionCookie, RelyingParty: a.relyingParty,
			AllowFailure:    func(r *http.Request) bool { return a.authRL.Allow(kernel.LimitKey(a.clientIP(r))) },
			EndPages:        func(userID string) { a.fusionUIAccess.endUser(userID) },
			EndPagesIn:      a.endFusionPagesIn,
			ClearPageCookie: a.clearFusionUICookie,
			Version:         a.Version, SSOHeader: a.SSOHeaderName,
		},
	}).Mount(routes)

	// Mail is server-wide, not one organisation's, and is gated to an owner of the default organisation
	// specifically - see requireDefaultOwner - rather than to any of the per-org role checks above.
	route("GET /api/v1/mail", httpkit.Settled, a.getMailConfig)
	route("PUT /api/v1/mail", httpkit.Settled, a.putMailConfig)

	const o = "/api/v1/orgs/{org}"
	route("GET "+o+"/info", httpkit.MemberRole, a.info)

	route("GET "+o+"/telemetry/self", httpkit.MemberRole, a.selfTelemetry)
	route("GET "+o+"/links/latest", httpkit.MemberRole, a.linksLatest)
	route("POST "+o+"/links/rotate-credentials", httpkit.AdminRole, a.rotateLinkCredentials)
	decisionloop.API{For: func(r *http.Request) *decisionloop.Service { return a.core(r).Loop }, Version: a.Version,
		Model: func(r *http.Request) (twin.Model, error) { m, _, err := a.tn(r).Hub.Model(r.Context()); return m, err }}.Mount(routes)
	(&settingsapi.API{For: func(r *http.Request) *kernel.Core { return a.core(r).Core }, ImageDefaults: func() kernel.ImageConfig { return a.ImageDefaults() }}).Mount(routes)
	telemetry.API{For: func(r *http.Request) *telemetry.Service { return a.core(r).Telemetry }}.Mount(routes)
	(&estate.API{For: func(r *http.Request) *estate.Hub { return a.tn(r).Hub }, Settings: &a.Settings, CAPin: a.C.CA.SPKIPin}).Mount(routes)

	// The decision loop: an advisor (an editor's API token) submits, a person decides. Widening is an administrator's, checked in the hub.

	route("GET "+o+"/operators", httpkit.AdminRole, a.listOperators)
	route("POST "+o+"/operators", httpkit.AdminRole, a.createOperator)
	route("GET "+o+"/operators/{id}", httpkit.AdminRole, a.getOperator)
	// What a person choosing where telemetry goes may pick: active operators and their state, no secrets - so
	// httpkit.EditorRole (the wizard's), where the list above stays an admin's.
	route("GET "+o+"/operator-destinations", httpkit.EditorRole, a.listOperatorDestinations)
	route("GET "+o+"/fusion", httpkit.AdminRole, a.getFusion)
	// The ticketed path that opens one of FUSION's own pages (Prometheus, Grafana) in a new tab; see fusion_ui_access.go.
	route("POST "+o+"/fusion/pages", httpkit.AdminRole, a.openFusionPage)
	route("POST "+o+"/fusion/enable", httpkit.AdminRole, a.enableFusion)
	route("POST "+o+"/fusion/disable", httpkit.AdminRole, a.disableFusion)
	route("GET "+o+"/fusion/retention", httpkit.AdminRole, a.getFusionRetention)
	route("PUT "+o+"/fusion/retention", httpkit.AdminRole, a.setFusionRetention)
	// FUSION access tokens: read-only credentials for the shared data API below.
	route("GET "+o+"/fusion/tokens", httpkit.AdminRole, a.listFusionTokens)
	route("POST "+o+"/fusion/tokens", httpkit.AdminRole, a.createFusionToken)
	route("DELETE "+o+"/fusion/tokens/{id}", httpkit.AdminRole, a.deleteFusionToken)
	route("POST "+o+"/operators/{id}/scope", httpkit.AdminRole, a.updateOperatorScope)
	// Installs the operator again: fresh receiver and client certificates from its stored CA, and a new receiver
	// token / heartbeat secret where it has one. Credential material, so httpkit.AdminRole and audited.
	route("POST "+o+"/operators/{id}/install", httpkit.AdminRole, a.reissueOperatorInstall)
	// What was issued for an operator (not the certificates: those are shown once): which sender holds which, and until when.
	route("GET "+o+"/operators/{id}/certificates", httpkit.AdminRole, a.listOperatorCertificates)
	route("POST "+o+"/operators/{id}/address", httpkit.AdminRole, a.setOperatorAddress)
	// One look at that address from this server: does anything answer there, and is it this operator.
	route("POST "+o+"/operators/{id}/address/check", httpkit.AdminRole, a.checkOperatorAddress)
	// The addresses discovery says this operator can be reached at, to pick from instead of typing one.
	route("GET "+o+"/operators/{id}/address-candidates", httpkit.AdminRole, a.listOperatorAddressCandidates)
	route("PUT "+o+"/operators/{id}/server-cluster", httpkit.AdminRole, a.putServerCluster)
	route("GET "+o+"/operators/{id}/senders", httpkit.AdminRole, a.listOperatorSenders)
	// Mints (or rotates) the operator's heartbeat secret: credential material, so httpkit.AdminRole like the rest.
	route("POST "+o+"/operators/{id}/heartbeat", httpkit.AdminRole, a.enableOperatorHeartbeat)
	route("POST "+o+"/operators/{id}/revoke", httpkit.AdminRole, a.revokeOperator)
	route("DELETE "+o+"/operators/{id}", httpkit.AdminRole, a.deleteOperator)

	// A telemetry intent only declares what one agent's bundled local operator should collect and where to
	// export it; the credentials and the install command come from /command below (httpkit.AdminRole) and the cluster's
	// owner still has to run it, so declaring takes httpkit.EditorRole rather than the httpkit.AdminRole an operator needs.
	// /command mints and returns operator credential material when the destination is an operator (via
	// IssueOperatorClientCert) - the same bar as operator CRUD itself (httpkit.AdminRole), higher than the
	// httpkit.EditorRole the rest of this intent's own CRUD takes above.
	route("POST "+o+"/telemetry-intents/{id}/command", httpkit.AdminRole, a.telemetryIntentCommand)

	// The shared data API over FUSION. It authenticates for itself (a FUSION access token, or an administrator of the
	// FUSION organisation) rather than through guard: the caller is not placed in an organisation by the path.
	a.registerFusionData(api)

	// A regional operator's collector reports that it is alive here. Authenticated by its own secret, with
	// no session, so it sits beside - not inside - the browser-facing wrappers: no CORS, no CSRF.
	a.handle(mux, "POST "+OperatorHeartbeatPath, accessOperatorSecret, http.HandlerFunc(a.operatorHeartbeat))
	// Prometheus's and Grafana's own pages, opened through the server for an administrator (admin_fusion_proxy.go).
	a.handle(mux, fusionPromPath, accessFusionUI, a.fusionUI("metrics"))
	a.handle(mux, fusionGrafanaPath, accessFusionUI, a.fusionUI("grafana"))
	a.handle(mux, "/api/", accessMount, a.cors(a.csrf(api)))
	if a.UIDir != "" {
		a.handle(mux, "/", accessMount, spa(a.UIDir))
	}
	return a.securityHeaders(mux)
}

// uiCSP is the Content-Security-Policy of the UI: everything from this origin only, no plugins, no
// framing, no base tag tricks. style-src allows inline styles because React Flow positions its nodes
// with them; scripts stay restricted to files served from here, so an injected <script> or inline
// handler does not run. Fonts are served by this server too (nothing is fetched from a CDN).
const uiCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

// apiCSP is for JSON and downloads: nothing on them should ever load anything.
const apiCSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), bluetooth=(), " +
	"accelerometer=(), gyroscope=(), magnetometer=(), midi=(), display-capture=(), interest-cohort=()"

func (a *Admin) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Security-Policy", apiCSP)
		} else {
			h.Set("Content-Security-Policy", uiCSP)
		}
		// Tell the browser to use HTTPS for this host from now on, but only when this very request was HTTPS
		// (directly, or as the trusted proxy reports it): sending it over plain HTTP would have no effect.
		if a.secure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Admin) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			for _, allowed := range a.Origins {
				if o == allowed {
					h := w.Header()
					h.Set("Access-Control-Allow-Origin", o)
					h.Set("Access-Control-Allow-Credentials", "true")
					h.Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With, If-Match")
					h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					h.Add("Vary", "Origin")
				}
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrf protects the cookie session. SameSite=Strict already keeps the cookie off cross-site
// requests; on top of that a state-changing request must come from an allowed origin and carry a
// custom header, which a browser will not attach cross-origin without a CORS preflight we refuse.
func (a *Admin) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			// A request authenticated by a personal API token (Authorization: Bearer ...) needs none of
			// this: a browser never attaches an Authorization header on its own the way it does a cookie,
			// so nothing here defends against forging one. Login and every cookie-authenticated request
			// still go through the check below - a bearer token is the one, narrow exception.
			if _, isBearer := bearerToken(r); !isBearer {
				if o := r.Header.Get("Origin"); o != "" && !a.originAllowed(o, r.Host) {
					httpkit.WriteErr(w, http.StatusForbidden, "request came from an origin this server does not trust")
					return
				}
				if r.Header.Get("X-Requested-With") == "" {
					httpkit.WriteErr(w, http.StatusForbidden, "missing X-Requested-With header")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Admin) originAllowed(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == host {
		return true
	}
	for _, o := range a.Origins {
		if o == origin {
			return true
		}
	}
	return false
}

func (a *Admin) clientIP(r *http.Request) string {
	if a.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1]) // the address the trusted proxy itself saw
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *Admin) secure(r *http.Request) bool {
	return r.TLS != nil || (a.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

// relyingParty derives this request's WebAuthn relying party from the address the browser actually has
// loaded: r.Host, the same source of truth originAllowed's same-origin check already trusts (rather than a
// value configured once at startup, since this server has no single fixed public domain the way a hosted
// SaaS would).
func (a *Admin) relyingParty(r *http.Request) accounts.RelyingParty {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	scheme := "http"
	if a.secure(r) {
		scheme = "https"
	}
	return accounts.RelyingParty{ID: host, Origin: scheme + "://" + r.Host, Name: accounts.TOTPIssuer}
}

// sessionCookie is the session secret the request carries, under either name: the __Host- one is what a
// Secure cookie is called, the plain one what a session opened over loopback HTTP (or by an earlier
// version) is called, and neither may lock the other out.
func sessionCookie(r *http.Request) (string, bool) {
	for _, n := range []string{hostCookieName, cookieName} {
		if ck, err := r.Cookie(n); err == nil && ck.Value != "" {
			return ck.Value, true
		}
	}
	return "", false
}

// bearerToken is the personal access token a script call carries, as an Authorization: Bearer header -
// never a cookie, since a script has no cookie jar to keep one in.
func bearerToken(r *http.Request) (string, bool) {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, p) {
		return "", false
	}
	return strings.TrimSpace(h[len(p):]), true
}

// guard authenticates the session, places the caller in the organisation named in the path, applies
// the role rule and bounds the request body. A person who is not a member of an organisation gets
// the same 404 as for one that does not exist; a member with too little standing gets 403.
func (a *Admin) guard(n httpkit.Need, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p accounts.Principal
		var err error
		if secret, ok := sessionCookie(r); ok {
			p, err = a.C.Accounts.Authenticate(r.Context(), secret)
		} else if secret, ok := bearerToken(r); ok {
			p, err = a.C.Accounts.AuthenticateAPIToken(r.Context(), secret)
		} else {
			err = kernel.Errf(kernel.KindUnauthenticated, "sign in required")
		}
		if err != nil {
			kernel.Metrics.AuthFailures.Add(1)
			// Failed authentications are throttled per address; a valid session is never counted,
			// so the UI polling every few seconds cannot lock itself out.
			if !a.authRL.Allow(kernel.LimitKey(a.clientIP(r))) {
				httpkit.WriteErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
				return
			}
			httpkit.WriteErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if n != httpkit.AnySession && p.User.MustChange {
			httpkit.WriteErr(w, http.StatusForbidden, "you must choose a new password before doing anything else")
			return
		}
		ctx := r.Context()
		if n >= httpkit.MemberRole {
			org := r.PathValue("org")
			if p, err = a.C.Accounts.Member(ctx, p, org); err != nil {
				a.fail(w, err)
				return
			}
			if !accounts.Allows(p, n.Action()) {
				httpkit.WriteErr(w, http.StatusForbidden, "your role in this organisation ("+p.Role+") does not allow this")
				return
			}
			t, err := a.P.Tenant(ctx, org)
			if err != nil {
				a.fail(w, err)
				return
			}
			ctx = context.WithValue(ctx, ctxTenant{}, t)
		}
		limit := int64(64 << 10)
		switch {
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/workspace"):
			limit = estate.MaxWorkspaceBytes + 4<<10
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/decisions"):
			// A run can compare several deciders at once, each with up to MAX_MOVES proposals
			// (see maxDecisionMovesTotal's own comment) - comfortably more than the default limit.
			limit = 256 << 10
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next(w, r.WithContext(accounts.WithCaller(ctx, p)))
	})
}

// fail answers with the error (see httpkit.Fail), logging an unexpected one to the server's log.
func (a *Admin) fail(w http.ResponseWriter, err error) { httpkit.Fail(w, a.C.Log, err) }

// ---- sign-in ----

// setCookie sets (or, with a negative maxAge, clears) the session cookie. Secure is on whenever the admin
// listener is reachable from other machines or the request came over HTTPS, and then the name carries the
// __Host- prefix. A cookie of the other name is cleared in the same response so a stale one cannot linger
// beside the new one.
func (a *Admin) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	secure := a.SecureCookies || a.secure(r)
	name, other := cookieName, hostCookieName
	if secure {
		name, other = hostCookieName, cookieName
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
	if _, err := r.Cookie(other); err == nil || maxAge < 0 {
		// The other name is expired (a __Host- cookie can only be cleared as a Secure one).
		http.SetCookie(w, &http.Cookie{Name: other, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: other == hostCookieName, SameSite: http.SameSiteStrictMode})
	}
}

// serveChart hands out the packaged agent chart (or the regional-operator or FUSION chart, by file name). It holds no secrets (the enrollment token and the CA pin are
// passed on the command line), so it needs no sign-in; that lets `helm install` take the URL directly.
func (a *Admin) serveChart(w http.ResponseWriter, r *http.Request) {
	c := chart.Agent
	if r.PathValue("file") == chart.RegionalOperator.Filename() {
		c = chart.RegionalOperator
	} else if r.PathValue("file") == chart.Fusion.Filename() {
		c = chart.Fusion
	} else if r.PathValue("file") != chart.Agent.Filename() {
		http.NotFound(w, r)
		return
	}
	b, err := c.Package()
	if err != nil {
		http.Error(w, "chart unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+c.Filename()+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (a *Admin) info(w http.ResponseWriter, r *http.Request) {
	t := a.tn(r)
	o, _ := a.C.Store.GetOrg(r.Context(), t.ID)
	img := a.images(t.C)
	httpkit.WriteJSON(w, 200, map[string]any{"orgId": t.ID, "orgName": o.Name, "role": httpkit.Principal(r).Role, "agentAddress": a.AgentAddr, "agentExposure": a.AgentExposure, "releaseName": a.ReleaseName, "releaseNamespace": a.ReleaseNamespace, "caPin": a.C.CA.SPKIPin(), "version": a.Version, "implementedTier": kernel.ImplementedTier, "geoip": a.P.Geo.Info(),
		// What the install wizard tells the operator to bring to the cluster: the chart file this server hands out
		// (empty when the command names a chart elsewhere) and where the image comes from (this organisation's
		// Settings → Installation, else the server's flags; empty registry: the chart's built-in names).
		// chartVersion is the version a `--version` flag must say - the one the release pipeline published this build's
		// chart under (agentChartVersion), NOT chart.Version(): the browser builds several commands itself from this
		// field, and the bundled Chart.yaml literal can name an old chart whose values schema rejects what this build
		// sends (helm: "Additional property ... is not allowed").
		"install": map[string]any{"chartFile": a.AgentChartFile(), "chartRef": a.AgentRef(img), "chartVersion": a.AgentVersion(), "imagesConfigured": img.Configured(), "imageRegistry": img.Registry, "imageTag": img.Tag, "imageDigest": img.Digest}})
}

// installCommand is the agent's install command for the token's ceiling, with the server's CA pin the agent will verify it against.
func (a *Admin) installCommand(img kernel.ImageConfig, secret string, t store.Token, extra ...install.Value) (string, error) {
	return a.Settings.AgentInstallCommand(img, a.C.CA.SPKIPin(), secret, t.AccessTier, extra...)
}

// CheckInstallConfig says, at start-up, whether the commands this server prints can be written with how it was set up: the chart it
// names and the versions it asks for. A server that cannot is not started, which is better than one whose every install page fails.
func (a *Admin) CheckInstallConfig() error {
	img := a.ImageDefaults()
	if _, err := a.installCommand(img, "check", store.Token{AccessTier: installcfg.ChartDefaultAccessTier}); err != nil {
		return fmt.Errorf("the agent install command cannot be written (check --chart-ref and the chart version): %w", err)
	}
	if err := a.checkOperatorInstall(img); err != nil {
		return fmt.Errorf("the regional operator's install command cannot be written (check --chart-ref and the chart version): %w", err)
	}
	return nil
}

// installConfigError is a command this server cannot write, because of how it was set up: logged, and told without the setting.
func (a *Admin) installConfigError(err error) error { return installcfg.ConfigError(a.C.Log, err) }

func (a *Admin) reasoned(w http.ResponseWriter, r *http.Request, do func(reason string) error) {
	httpkit.Reasoned(w, r, a.C.Log, do)
}

// ---- static UI with SPA fallback ----

func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() && !fileExists(filepath.Join(p, "index.html")) {
			r.URL.Path = "/"
		}
		fs.ServeHTTP(w, r)
	})
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// action is what a route's guard level asks of the caller. The levels below ActRead (a session with no organisation) ask nothing
// of a role.
