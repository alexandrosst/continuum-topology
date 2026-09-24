package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"continuum/internal/chart"
	"continuum/internal/store"
	"continuum/internal/workspace"
)

// Admin is the JSON API the UI talks to. It listens on its own port, separate from the agent
// port. Everything except sign-in needs a session cookie, and what a person may do depends on
// their role.
type Admin struct {
	P         *Platform
	C         *Core  // the platform-wide core (accounts, sign-in); an organisation's own is a.core(r)
	AgentAddr string // host:port agents dial, shown in the install command
	// AgentExposure is how AgentAddr is reached (loadbalancer, nodeport, gateway, clusterip), from --agent-exposure.
	// Purely descriptive - the server cannot check it against how the port is actually reachable - and only used to
	// pick which guidance Settings → Installation shows for changing AgentAddr later. Empty on older installs (a
	// server chart from before this flag existed): the UI then shows generic guidance for all three methods.
	AgentExposure string
	// ReleaseName and ReleaseNamespace are this Helm release's own name and namespace, from --release-name and
	// --release-namespace. Also purely descriptive, also empty on an older install; together with AgentExposure they
	// let Settings → Installation print an exact, ready-to-run helm upgrade command instead of one with blanks in it.
	ReleaseName, ReleaseNamespace string
	ChartRef  string // an explicit chart reference for install commands; empty means the copy the server serves
	// ImageRegistry, ImageTag and ImageDigest are the server-wide defaults (--image-registry, --image-tag,
	// --image-digest) for where install commands pull the agent image and chart from. An organisation's own
	// Settings → Installation wins over them; with neither, the chart's built-in image names apply. There is no
	// built-in default registry: see images.go.
	ImageRegistry, ImageTag, ImageDigest string
	Origins                              []string // browsers allowed cross-origin (dev); empty = same origin only
	UIDir                                string   // built UI to serve, optional
	Version                              string
	// AgentChartVersion is the continuum-agent chart version the release pipeline actually published alongside
	// this exact server build (edge: 0.0.0-edge.<sha>; a tagged release: the tag) - set at link time, empty on a
	// build that skipped that (a local `go build`, or an image scripts/publish.sh built without it). Only matters
	// for a `--version` flag against an OCI/registry chart reference; a local ./file.tgz reference (no registry
	// configured) always serves the exact chart this binary was built from and needs no version at all.
	AgentChartVersion string
	// TrustProxy is set when a TLS-terminating proxy sits in front: the client address is read from
	// the last entry of X-Forwarded-For (what the proxy itself saw), and the request counts as HTTPS
	// when the proxy says so in X-Forwarded-Proto. Only correct when the proxy is the sole way in and
	// replaces those headers; otherwise a client could forge its address and the scheme.
	TrustProxy bool
	// SecureCookies marks the session cookie Secure (and gives it the __Host- prefix) on every response,
	// which the server sets whenever the admin listener is not on loopback: a browser then never sends
	// the session over plain HTTP. On loopback the cookie is Secure only when the request itself was HTTPS.
	SecureCookies bool
	authRL        *Limiter
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

type ctxPrincipal struct{}
type ctxTenant struct{}

// need is what a route asks of the caller. The organisation levels also require the {org} in the path
// to be one the caller belongs to.
type need int

const (
	anySession need = iota // signed in, even if a password change is still pending
	settled                // signed in, password settled, no organisation involved
	memberRole             // belongs to the organisation (any role: reads)
	editorRole
	adminRole
	ownerRole
)

var needRole = map[need]string{memberRole: RoleViewer, editorRole: RoleEditor, adminRole: RoleAdmin, ownerRole: RoleOwner}

func (a *Admin) tn(r *http.Request) *Tenant {
	t, _ := r.Context().Value(ctxTenant{}).(*Tenant)
	return t
}

func (a *Admin) core(r *http.Request) *Core { return a.tn(r).C }

func (a *Admin) Handler() http.Handler {
	a.authRL = NewLimiter(30, 10)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.HandleFunc("GET /charts/{file}", a.serveChart)

	api := http.NewServeMux()
	route := func(pattern string, n need, h http.HandlerFunc) { api.Handle(pattern, a.guard(n, h)) }
	api.HandleFunc("GET /api/v1/server", a.serverInfo)
	api.HandleFunc("POST /api/v1/auth/login", a.login)
	api.HandleFunc("POST /api/v1/auth/login/2fa", a.login2FA)
	api.HandleFunc("POST /api/v1/auth/login/2fa/email", a.requestLoginEmailCode)
	api.HandleFunc("POST /api/v1/auth/login/2fa/webauthn/begin", a.beginPasskeyLogin)
	api.HandleFunc("POST /api/v1/auth/login/2fa/webauthn/finish", a.finishPasskeyLogin)
	api.HandleFunc("POST /api/v1/auth/register", a.register)
	api.HandleFunc("POST /api/v1/auth/logout", a.logout)
	api.HandleFunc("POST /api/v1/invites/preview", a.previewInvite)
	route("GET /api/v1/auth/me", anySession, a.me)
	route("POST /api/v1/auth/password", anySession, a.changePassword)
	route("POST /api/v1/auth/2fa/setup", anySession, a.setup2FA)
	route("POST /api/v1/auth/2fa/enable", anySession, a.enable2FA)
	route("POST /api/v1/auth/2fa/disable", anySession, a.disable2FA)
	route("POST /api/v1/auth/email/request", anySession, a.requestEmailVerification)
	route("POST /api/v1/auth/email/confirm", anySession, a.confirmEmail)
	route("POST /api/v1/auth/email-otp/enable", anySession, a.enableEmailOTP)
	route("POST /api/v1/auth/email-otp/disable", anySession, a.disableEmailOTP)
	route("POST /api/v1/auth/webauthn/register/begin", anySession, a.beginPasskeyRegistration)
	route("POST /api/v1/auth/webauthn/register/finish", anySession, a.finishPasskeyRegistration)
	route("POST /api/v1/auth/webauthn/{id}/rename", anySession, a.renamePasskey)
	route("POST /api/v1/auth/webauthn/{id}/remove", anySession, a.removePasskey)
	route("GET /api/v1/orgs", settled, a.listOrgs)
	route("POST /api/v1/orgs", settled, a.createOrg)
	route("POST /api/v1/invites/accept", settled, a.acceptInvite)

	const o = "/api/v1/orgs/{org}"
	route("GET "+o+"/info", memberRole, a.info)
	route("POST "+o+"/rename", ownerRole, a.renameOrg)
	route("POST "+o+"/delete", ownerRole, a.deleteOrg)
	route("POST "+o+"/leave", memberRole, a.leaveOrg)
	route("GET "+o+"/members", memberRole, a.listMembers)
	route("POST "+o+"/members/{id}/role", adminRole, a.setMemberRole)
	route("POST "+o+"/members/{id}/remove", adminRole, a.removeMember)
	route("GET "+o+"/invites", adminRole, a.listInvites)
	route("POST "+o+"/invites", adminRole, a.createInvite)
	route("DELETE "+o+"/invites/{id}", adminRole, a.revokeInvite)

	route("GET "+o+"/state", memberRole, a.state)
	route("GET "+o+"/model", memberRole, a.model)
	route("GET "+o+"/workspace", memberRole, a.getWorkspace)
	route("PUT "+o+"/workspace", editorRole, a.putWorkspace)

	route("GET "+o+"/settings", memberRole, a.getSettings)
	route("PUT "+o+"/settings", adminRole, a.putSettings)
	route("GET "+o+"/history", memberRole, a.historyIndex)
	route("GET "+o+"/history/snapshot", memberRole, a.historySnapshot)
	route("GET "+o+"/history/traffic", memberRole, a.historyTraffic)
	route("POST "+o+"/history/record", adminRole, a.recordNow)
	route("GET "+o+"/events", memberRole, a.listEvents)
	route("POST "+o+"/decide", editorRole, a.decide)
	route("GET "+o+"/storage", memberRole, a.storage)
	route("GET "+o+"/timeline", memberRole, a.timeline)
	route("GET "+o+"/audit", adminRole, a.audit)
	route("GET "+o+"/workspace/revisions", memberRole, a.workspaceRevisions)
	route("GET "+o+"/workspace/at", memberRole, a.workspaceAt)

	route("GET "+o+"/tokens", adminRole, a.listTokens)
	route("POST "+o+"/tokens", adminRole, a.createToken)
	route("DELETE "+o+"/tokens/{id}", adminRole, a.deleteToken)
	route("POST "+o+"/agents/{id}/approve", adminRole, a.approve)
	route("POST "+o+"/agents/{id}/reject", adminRole, a.reject)
	route("POST "+o+"/agents/{id}/revoke", adminRole, a.revoke)
	route("POST "+o+"/agents/{id}/tier", editorRole, a.setAgentTier)
	route("POST "+o+"/agents/{id}/consent", editorRole, a.setAgentConsent)

	mux.Handle("/api/", a.cors(a.csrf(api)))
	if a.UIDir != "" {
		mux.Handle("/", spa(a.UIDir))
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
			if o := r.Header.Get("Origin"); o != "" && !a.originAllowed(o, r.Host) {
				writeErr(w, http.StatusForbidden, "request came from an origin this server does not trust")
				return
			}
			if r.Header.Get("X-Requested-With") == "" {
				writeErr(w, http.StatusForbidden, "missing X-Requested-With header")
				return
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
func (a *Admin) relyingParty(r *http.Request) RelyingParty {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	scheme := "http"
	if a.secure(r) {
		scheme = "https"
	}
	return RelyingParty{ID: host, Origin: scheme + "://" + r.Host, Name: totpIssuer}
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

// guard authenticates the session, places the caller in the organisation named in the path, applies
// the role rule and bounds the request body. A person who is not a member of an organisation gets
// the same 404 as for one that does not exist; a member with too little standing gets 403.
func (a *Admin) guard(n need, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Principal
		var err error
		if secret, ok := sessionCookie(r); ok {
			p, err = a.C.Authenticate(r.Context(), secret)
		} else {
			err = errf(KindUnauthenticated, "sign in required")
		}
		if err != nil {
			Metrics.authFailures.Add(1)
			// Failed authentications are throttled per address; a valid session is never counted,
			// so the UI polling every few seconds cannot lock itself out.
			if !a.authRL.Allow(LimitKey(a.clientIP(r))) {
				writeErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
				return
			}
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if n != anySession && p.User.MustChange {
			writeErr(w, http.StatusForbidden, "you must choose a new password before doing anything else")
			return
		}
		ctx := r.Context()
		if n >= memberRole {
			org := r.PathValue("org")
			if p, err = a.C.Member(ctx, p, org); err != nil {
				a.fail(w, err)
				return
			}
			if roleRank[p.Role] < roleRank[needRole[n]] {
				writeErr(w, http.StatusForbidden, "your role in this organisation ("+p.Role+") does not allow this")
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
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/workspace") {
			limit = MaxWorkspaceBytes + 4<<10
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next(w, r.WithContext(context.WithValue(ctx, ctxPrincipal{}, p)))
	})
}

func principal(r *http.Request) Principal {
	p, _ := r.Context().Value(ctxPrincipal{}).(Principal)
	return p
}

func actor(r *http.Request) string { return principal(r).User.Username }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *Admin) fail(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		code := map[Kind]int{KindInvalid: 400, KindUnauthenticated: 401, KindNotFound: 404, KindConflict: 409, KindRateLimited: 429, KindInternal: 500, KindForbidden: 403, KindTwoFactorRequired: 401}[e.Kind]
		if len(e.Data) > 0 {
			out := map[string]any{"error": e.Msg}
			for k, v := range e.Data {
				out[k] = v
			}
			writeJSON(w, code, out)
			return
		}
		writeErr(w, code, e.Msg)
		return
	}
	a.C.Log.Error("admin request failed", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errf(KindInvalid, "invalid request body")
	}
	return nil
}

// ---- sign-in ----

type userDoc struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	MustChangePassword bool   `json:"mustChangePassword"`
	CreatedAt          string `json:"createdAt"`
	LastLogin          string `json:"lastLogin,omitempty"`
	TwoFactorEnabled   bool   `json:"twoFactorEnabled"`
	// Email is shown even while unverified, so Settings can say "verifying jane@example.com...".
	Email           string `json:"email,omitempty"`
	EmailVerified   bool   `json:"emailVerified"`
	EmailOTPEnabled bool   `json:"emailOtpEnabled"`
	MailConfigured  bool   `json:"mailConfigured"` // whether the server can send mail at all
	// Passkeys never carries a public key or anything else needed to verify a login, only what settings needs
	// to show a person their own credentials and let them rename or remove one.
	Passkeys []passkeyDoc `json:"passkeys"`
}

type passkeyDoc struct {
	ID         string `json:"id"` // base64url CredentialID - what rename/remove address it by
	Name       string `json:"name"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
}

func toUserDoc(u store.User, mailConfigured bool) userDoc {
	passkeys := make([]passkeyDoc, len(u.WebAuthnCredentials))
	for i, cr := range u.WebAuthnCredentials {
		passkeys[i] = passkeyDoc{ID: base64.RawURLEncoding.EncodeToString(cr.CredentialID), Name: cr.Name, CreatedAt: rfc(cr.CreatedAt), LastUsedAt: rfcp(cr.LastUsedAt)}
	}
	return userDoc{
		ID: u.ID, Username: u.Username, MustChangePassword: u.MustChange, CreatedAt: rfc(u.CreatedAt), LastLogin: rfcp(u.LastLogin),
		TwoFactorEnabled: u.TOTPEnabledAt != nil,
		Email:            u.Email, EmailVerified: u.EmailVerifiedAt != nil, EmailOTPEnabled: u.EmailOTPEnabledAt != nil,
		MailConfigured: mailConfigured,
		Passkeys:       passkeys,
	}
}

type orgDoc struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// session is the answer to sign-in, sign-up and "who am I": the person and every organisation they
// belong to, so the UI needs no second call to know where it may go.
func (a *Admin) session(ctx context.Context, u store.User) map[string]any {
	orgs := []orgDoc{}
	if mine, err := a.C.Store.ListMyOrgs(ctx, u.ID); err == nil {
		for _, o := range mine {
			orgs = append(orgs, orgDoc{ID: o.ID, Name: o.Name, Role: o.Role})
		}
	}
	return map[string]any{"user": toUserDoc(u, a.C.Mailer.Enabled()), "orgs": orgs}
}

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

// serverInfo is public: what the sign-in and sign-up pages need before anyone is signed in.
func (a *Admin) serverInfo(w http.ResponseWriter, r *http.Request) {
	mode := a.C.RegMode
	writeJSON(w, 200, map[string]any{"registration": mode, "version": a.Version})
}

// chartFile is the packaged chart's file name. The server always serves its own copy, whether or not the
// install command uses it, so an operator can fall back to it.
func (a *Admin) chartFile() string { return chart.Filename() }

// chartRef is the chart the install command names, or "" when it names the downloaded file. An explicit
// --chart-ref wins ("local" forces the file). Otherwise a configured image registry also holds the chart, as an
// OCI artifact next to the images, so the printed command needs nothing downloaded first.
func (a *Admin) chartRef(img ImageConfig) string {
	switch {
	case a.ChartRef == "local":
		return ""
	case a.ChartRef != "":
		return a.ChartRef
	case img.Configured():
		return "oci://" + OCIBase(img.Registry) + "/continuum-agent"
	}
	return ""
}

// OCIBase turns an image registry setting into where an OCI registry keeps things under it. A bare name
// ("myteam") is a Docker Hub namespace, and Docker Hub serves Helm charts from registry-1.docker.io.
func OCIBase(registry string) string {
	r := strings.Trim(strings.TrimSpace(registry), "/")
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		if rest, ok := strings.CutPrefix(r, p); ok {
			return "registry-1.docker.io/" + rest
		}
	}
	first, _, _ := strings.Cut(r, "/")
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return r
	}
	return "registry-1.docker.io/" + r
}

// serveChart hands out the packaged agent chart. It holds no secrets (the enrollment token and the CA pin are
// passed on the command line), so it needs no sign-in; that lets `helm install` take the URL directly.
func (a *Admin) serveChart(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("file") != chart.Filename() {
		http.NotFound(w, r)
		return
	}
	b, err := chart.Package()
	if err != nil {
		http.Error(w, "chart unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+chart.Filename()+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (a *Admin) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	secret, u, err := a.C.Login(r.Context(), a.clientIP(r), req.Username, req.Password)
	if err != nil {
		// A password that checks out but needs a TOTP code next comes back as an ordinary error (a
		// KindTwoFactorRequired one, its "pending" token merged into this same JSON body by a.fail) rather
		// than a special-cased response: the UI tells it apart from a wrong password by that field.
		a.fail(w, err)
		return
	}
	a.setCookie(w, r, secret, int(SessionMax.Seconds()))
	writeJSON(w, 200, a.session(r.Context(), u))
}

// login2FA finishes a sign-in login left pending on a two-factor code.
func (a *Admin) login2FA(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Pending string `json:"pending"`
		Code    string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	secret, u, err := a.C.Login2FA(r.Context(), a.clientIP(r), req.Pending, req.Code)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.setCookie(w, r, secret, int(SessionMax.Seconds()))
	writeJSON(w, 200, a.session(r.Context(), u))
}

func (a *Admin) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Invite   string `json:"invite"`
		Org      string `json:"org"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	secret, u, err := a.C.Register(r.Context(), a.clientIP(r), req.Username, req.Password, strings.TrimSpace(req.Invite), req.Org)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.setCookie(w, r, secret, int(SessionMax.Seconds()))
	writeJSON(w, 201, a.session(r.Context(), u))
}

func (a *Admin) logout(w http.ResponseWriter, r *http.Request) {
	if secret, ok := sessionCookie(r); ok {
		if p, err := a.C.Authenticate(r.Context(), secret); err == nil {
			a.C.Logout(r.Context(), p)
		}
	}
	a.setCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.session(r.Context(), principal(r).User))
}

func (a *Admin) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.ChangePassword(r.Context(), principal(r), req.Current, req.New); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

// setup2FA starts turning two-factor authentication on: it returns the fresh secret and the otpauth://
// URI for it, shown as text and a copyable key rather than a QR code (every authenticator app also
// accepts typing a secret in by hand).
func (a *Admin) setup2FA(w http.ResponseWriter, r *http.Request) {
	secret, uri, err := a.C.Setup2FA(r.Context(), principal(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"secret": secret, "otpauthUrl": uri})
}

// enable2FA confirms a setup with one code from it, turns two-factor authentication on, and hands back
// this account's recovery codes - shown once, since only their hashes are kept from here on.
func (a *Admin) enable2FA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	codes, err := a.C.Enable2FA(r.Context(), principal(r), req.Code)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"recoveryCodes": codes})
}

func (a *Admin) disable2FA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.Disable2FA(r.Context(), principal(r), req.Password); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

// requestLoginEmailCode is called from the pending-2FA screen when a person picks "email me a code"
// instead of typing one from an authenticator app; login2FA then accepts the mailed code back exactly like
// a TOTP one. No session exists yet at this point, same as login and login2FA themselves.
func (a *Admin) requestLoginEmailCode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Pending string `json:"pending"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.RequestLoginEmailCode(r.Context(), a.clientIP(r), req.Pending); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestEmailVerification starts confirming a new email address on the signed-in account.
func (a *Admin) requestEmailVerification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.RequestEmailVerification(r.Context(), principal(r), req.Email); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

// confirmEmail finishes requestEmailVerification with the code that was mailed.
func (a *Admin) confirmEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.ConfirmEmail(r.Context(), principal(r), req.Code); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

// enableEmailOTP turns on email as a second sign-in factor for the already-verified address on the account.
func (a *Admin) enableEmailOTP(w http.ResponseWriter, r *http.Request) {
	if err := a.C.EnableEmailOTP(r.Context(), principal(r)); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

func (a *Admin) disableEmailOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.DisableEmailOTP(r.Context(), principal(r), req.Password); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

// writeOptions hands back a WebAuthn options object exactly as the provider built it - already valid JSON,
// so unlike writeJSON this never re-encodes it.
func writeOptions(w http.ResponseWriter, options []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(options)
}

// beginPasskeyRegistration starts registering a new passkey or security key on the signed-in account. The
// response is the CredentialCreationOptions object navigator.credentials.create() takes directly.
func (a *Admin) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	options, err := a.C.BeginPasskeyRegistration(r.Context(), principal(r), a.relyingParty(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeOptions(w, options)
}

// finishPasskeyRegistration completes a registration beginPasskeyRegistration started.
func (a *Admin) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.FinishPasskeyRegistration(r.Context(), principal(r), a.relyingParty(r), req.Name, req.Response); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

// beginPasskeyLogin starts the passkey half of a pending two-factor sign-in. No session exists yet, same as
// login and login2FA.
func (a *Admin) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Pending string `json:"pending"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	options, err := a.C.BeginPasskeyLogin(r.Context(), a.clientIP(r), req.Pending, a.relyingParty(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeOptions(w, options)
}

// finishPasskeyLogin completes a passkey sign-in and, unlike login2FA, opens the session itself: a passkey's
// response is a signed assertion object, not a short code that fits login2FA's single field.
func (a *Admin) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pending  string          `json:"pending"`
		Response json.RawMessage `json:"response"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	secret, u, err := a.C.FinishPasskeyLogin(r.Context(), a.clientIP(r), req.Pending, a.relyingParty(r), req.Response)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.setCookie(w, r, secret, int(SessionMax.Seconds()))
	writeJSON(w, 200, a.session(r.Context(), u))
}

func (a *Admin) renamePasskey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.RenamePasskey(r.Context(), principal(r), r.PathValue("id"), req.Name); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

func (a *Admin) removePasskey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.C.RemovePasskey(r.Context(), principal(r), r.PathValue("id"), req.Password); err != nil {
		a.fail(w, err)
		return
	}
	u, _ := a.C.Store.GetUser(r.Context(), principal(r).User.ID)
	writeJSON(w, 200, a.session(r.Context(), u))
}

// ---- organisations, members, invitations ----

func (a *Admin) listOrgs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.session(r.Context(), principal(r).User)["orgs"])
}

func (a *Admin) createOrg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	o, err := a.C.CreateOrg(r.Context(), principal(r), req.Name)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 201, orgDoc{ID: o.ID, Name: o.Name, Role: RoleOwner})
}

func (a *Admin) previewInvite(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		Token string `json:"token"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	inv, name, err := a.C.PreviewInvite(r.Context(), a.clientIP(r), strings.TrimSpace(req.Token))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"organisation": name, "role": inv.Role, "expiresAt": rfc(inv.ExpiresAt)})
}

func (a *Admin) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	o, role, err := a.C.AcceptInvite(r.Context(), a.clientIP(r), principal(r), strings.TrimSpace(req.Token))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, orgDoc{ID: o.ID, Name: o.Name, Role: role})
}

func (a *Admin) renameOrg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.core(r).RenameOrg(r.Context(), principal(r), req.Name); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) deleteOrg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.core(r).DeleteOrg(r.Context(), principal(r), req.Confirm); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) leaveOrg(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if err := a.core(r).RemoveMember(r.Context(), p, p.User.ID); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type memberDoc struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	JoinedAt string `json:"joinedAt"`
	LastSeen string `json:"lastLogin,omitempty"`
	You      bool   `json:"you,omitempty"`
}

func (a *Admin) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := a.C.Store.ListMembers(r.Context(), a.core(r).OrgID)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []memberDoc{}
	for _, m := range ms {
		out = append(out, memberDoc{ID: m.User.ID, Username: m.Username, Role: m.Role, JoinedAt: rfc(m.JoinedAt), LastSeen: rfcp(m.LastLogin), You: m.User.ID == principal(r).User.ID})
	}
	writeJSON(w, 200, out)
}

func (a *Admin) setMemberRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.core(r).SetMemberRole(r.Context(), principal(r), r.PathValue("id"), req.Role); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) removeMember(w http.ResponseWriter, r *http.Request) {
	if err := a.core(r).RemoveMember(r.Context(), principal(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type inviteDoc struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Label     string `json:"label,omitempty"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	Used      bool   `json:"used"`
	UsedBy    string `json:"usedBy,omitempty"`
	Expired   bool   `json:"expired,omitempty"`
}

func (a *Admin) inviteDoc(ctx context.Context, i store.Invite) inviteDoc {
	d := inviteDoc{ID: i.ID, Role: i.Role, Label: i.Label, CreatedBy: a.C.nameOf(ctx, i.CreatedBy), CreatedAt: rfc(i.CreatedAt), ExpiresAt: rfc(i.ExpiresAt), Used: i.UsedAt != nil, Expired: i.UsedAt == nil && a.C.Now().After(i.ExpiresAt)}
	if i.UsedBy != "" {
		d.UsedBy = a.C.nameOf(ctx, i.UsedBy)
	}
	return d
}

func (a *Admin) listInvites(w http.ResponseWriter, r *http.Request) {
	is, err := a.C.Store.ListInvites(r.Context(), a.core(r).OrgID)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []inviteDoc{}
	for _, i := range is {
		out = append(out, a.inviteDoc(r.Context(), i))
	}
	writeJSON(w, 200, out)
}

func (a *Admin) createInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role  string `json:"role"`
		Label string `json:"label"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	secret, inv, err := a.core(r).CreateInvite(r.Context(), principal(r), req.Role, req.Label)
	if err != nil {
		a.fail(w, err)
		return
	}
	// The secret appears here once and is never retrievable again.
	writeJSON(w, 201, map[string]any{"token": secret, "invite": a.inviteDoc(r.Context(), inv)})
}

func (a *Admin) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := a.core(r).RevokeInvite(r.Context(), principal(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- workspace ----

// getWorkspace returns the stored document. "?meta=1" returns only the revision, which is what the
// UI polls to notice that someone else saved.
func (a *Admin) getWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := a.C.Store.GetWorkspace(r.Context(), a.core(r).OrgID)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := map[string]any{"rev": ws.Rev, "updatedAt": rfcNZ(ws.UpdatedAt), "updatedBy": ws.UpdatedBy, "formatVersion": workspace.CurrentVersion}
	if r.URL.Query().Get("meta") == "" && ws.Rev > 0 {
		data, note := ws.Data, ws.Note
		if workspace.Peek(data) < workspace.CurrentVersion {
			// A document saved before observed facts were held apart from the workspace: shown as it would be saved now.
			if d, rep, err := workspace.Declare(data); err == nil {
				data = d
				if note == "" && rep.Changed() {
					note = rep.Note()
				}
			}
		}
		out["data"] = json.RawMessage(data)
		if note != "" {
			out["note"] = note
		}
	}
	writeJSON(w, 200, out)
}

func rfcNZ(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return rfc(t)
}

// putWorkspace saves the document. The If-Match header carries the revision the client last saw
// (0 for the first save); a mismatch answers 409 with the current revision so the UI can offer
// "load theirs" or "overwrite".
func (a *Admin) putWorkspace(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
	if err != nil || rev < 0 {
		writeErr(w, http.StatusPreconditionRequired, "send the revision you last saw in If-Match")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "workspace is too large")
		return
	}
	ws, err := a.core(r).SaveWorkspace(r.Context(), actor(r), rev, body)
	var e *Error
	if errors.As(err, &e) && e.Kind == KindConflict {
		writeJSON(w, http.StatusConflict, map[string]any{"error": e.Msg, "rev": ws.Rev, "updatedAt": rfcNZ(ws.UpdatedAt), "updatedBy": ws.UpdatedBy})
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	out := map[string]any{"rev": ws.Rev, "updatedAt": rfc(ws.UpdatedAt), "updatedBy": ws.UpdatedBy}
	if ws.Note != "" {
		out["note"] = ws.Note
	}
	writeJSON(w, 200, out)
}

func (a *Admin) info(w http.ResponseWriter, r *http.Request) {
	t := a.tn(r)
	o, _ := a.C.Store.GetOrg(r.Context(), t.ID)
	img := a.images(t.C)
	writeJSON(w, 200, map[string]any{"orgId": t.ID, "orgName": o.Name, "role": principal(r).Role, "agentAddress": a.AgentAddr, "agentExposure": a.AgentExposure, "releaseName": a.ReleaseName, "releaseNamespace": a.ReleaseNamespace, "caPin": a.C.CA.Pin(), "version": a.Version, "implementedTier": ImplementedTier, "geoip": a.P.Geo.Info(),
		// What the install wizard tells the operator to bring to the cluster: the chart file this server hands out
		// (empty when the command names a chart elsewhere) and where the image comes from (this organisation's
		// Settings → Installation, else the server's flags; empty registry: the chart's built-in names).
		"install": map[string]any{"chartFile": a.chartFile(), "chartRef": a.chartRef(img), "chartVersion": chart.Version(), "imagesConfigured": img.Configured(), "imageRegistry": img.Registry, "imageTag": img.Tag, "imageDigest": img.Digest}})
}

// state is what the UI polls. The audit trail is part of it only for administrators and owners: it names
// people, and it is what they review; everyone else gets the topology and an empty list.
func (a *Admin) state(w http.ResponseWriter, r *http.Request) {
	doc, err := a.tn(r).Hub.StateFor(r.Context(), roleRank[principal(r).Role] >= roleRank[RoleAdmin])
	if err != nil {
		a.fail(w, err)
		return
	}
	if roleRank[principal(r).Role] >= roleRank[RoleEditor] {
		img := a.images(a.core(r))
		for i, ag := range doc.Agents {
			if ag.AccessTier < ag.InstalledTier {
				doc.Agents[i].HardenHelm = a.upgradeCommand(img, ag.AccessTier, ag.Namespace, ag.ReleaseName)
			}
		}
	} else {
		withoutDiagnostics(&doc)
	}
	writeJSON(w, 200, doc)
}

type tokenDoc struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tier      int    `json:"tier"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	Used      bool   `json:"used"`
	UsedBy    string `json:"usedBy,omitempty"`
	// ExpectedCluster is the cluster (kube-system UID) the token is bound to; absent for a token any cluster may use.
	ExpectedCluster string `json:"expectedCluster,omitempty"`
}

func toTokenDoc(t store.Token) tokenDoc {
	return tokenDoc{ID: t.ID, Name: t.Label, Tier: t.AccessTier, CreatedAt: rfc(t.CreatedAt), ExpiresAt: rfc(t.ExpiresAt), Used: t.UsedAt != nil, UsedBy: t.UsedBy, ExpectedCluster: t.ExpectedFingerprint}
}

func (a *Admin) listTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := a.C.Store.ListTokens(r.Context(), a.core(r).OrgID)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []tokenDoc{}
	for _, t := range ts {
		out = append(out, toTokenDoc(t))
	}
	writeJSON(w, 200, out)
}

func (a *Admin) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Tier int    `json:"tier"`
		// ExpectedCluster optionally binds the token to one cluster: the UID of its kube-system namespace.
		ExpectedCluster string `json:"expectedCluster"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	secret, t, err := a.core(r).CreateTokenFor(r.Context(), actor(r), req.Name, req.Tier, req.ExpectedCluster)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"token": secret, "meta": toTokenDoc(t), "install": a.installCommand(a.images(a.core(r)), secret, t)})
}

// chartDefaultAccessTier mirrors continuum-agent/values.yaml's own access.tier default. Printing --set access.tier=N
// when N is already what the chart defaults to would only make the common case's command longer for no reason.
const chartDefaultAccessTier = 2

// agentChartVersion is what a `--version` flag against an OCI/registry chart reference should say: the version the
// release pipeline actually published this build's chart under, when the binary was linked with that information,
// else chart.Version() (the chart's own Chart.yaml literal) as the best available guess. The two agree for a chart
// packaged straight from a checkout with no `helm package --version` override (scripts/publish.sh does this); they
// can disagree for a CI-published chart, whose OCI version is a separate, deliberately-overridden string (edge:
// 0.0.0-edge.<sha>; a tagged release: the tag) that Chart.yaml's checked-in text was never meant to track.
func (a *Admin) agentChartVersion() string {
	if a.AgentChartVersion != "" {
		return a.AgentChartVersion
	}
	return chart.Version()
}

// installCommand is what the operator runs on the cluster. The token appears here once, in the
// response to its creation, and is never retrievable again.
//
// With an image registry configured it also points the chart at the one image (image.repository, plus image.tag and
// image.digest when set; with a digest the chart pulls repository@digest and ignores the tag) and fetches the chart
// from the same registry. Without one it names the chart file the server serves and leaves every image value to the
// chart's own defaults.
func (a *Admin) installCommand(img ImageConfig, secret string, t store.Token) string {
	ref, version := a.chartRef(img), ""
	if ref == "" {
		ref = "./" + chart.Filename() // the file the wizard offers to download
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + a.agentChartVersion() // a registry or repository holds many versions
	}
	// enrollment.key packs the CA pin and the one-time token into the single flag the chart splits back apart at
	// render time (see continuum-agent's _helpers.tpl): one thing to paste instead of two, without hiding either
	// value's own text - the pin is 64 hex characters, the token cannot contain ".", so joining them is unambiguous
	// and needs no escaping.
	var b strings.Builder
	fmt.Fprintf(&b, "helm install continuum-agent %s%s \\\n  --namespace continuum-system --create-namespace \\\n  --set server.address=%s \\\n  --set enrollment.key=%s.%s",
		ref, version, a.AgentAddr, a.C.CA.Pin(), secret)
	if t.AccessTier != chartDefaultAccessTier {
		fmt.Fprintf(&b, " \\\n  --set access.tier=%d", t.AccessTier)
	}
	if img.Configured() {
		fmt.Fprintf(&b, " \\\n  --set image.repository=%s/continuum", img.Registry)
		if img.Tag != "" {
			fmt.Fprintf(&b, " \\\n  --set image.tag=%s", img.Tag)
		}
		if img.Digest != "" {
			fmt.Fprintf(&b, " \\\n  --set image.digest=%s", img.Digest)
		}
	}
	return b.String()
}

func (a *Admin) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := a.core(r).DeleteToken(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) approve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Code is the approval code the agent printed in its log. Confirm is the older fingerprint prefix,
		// still accepted for an agent that enrolled without a code.
		Code    string `json:"code"`
		Confirm string `json:"confirm"`
		Tier    int    `json:"tier"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	proof := strings.TrimSpace(req.Code)
	if proof == "" {
		proof = strings.TrimSpace(req.Confirm)
	}
	if err := a.core(r).Approve(r.Context(), actor(r), r.PathValue("id"), proof, req.Tier); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) reject(w http.ResponseWriter, r *http.Request) {
	a.reasoned(w, r, func(reason string) error { return a.core(r).Reject(r.Context(), actor(r), r.PathValue("id"), reason) })
}

func (a *Admin) revoke(w http.ResponseWriter, r *http.Request) {
	a.reasoned(w, r, func(reason string) error { return a.core(r).Revoke(r.Context(), actor(r), r.PathValue("id"), reason) })
}

func (a *Admin) reasoned(w http.ResponseWriter, r *http.Request, do func(reason string) error) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if err := do(req.Reason); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
