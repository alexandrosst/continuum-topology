package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uiRig is an admin server that also serves a (tiny) built UI.
func uiRig(t *testing.T) *adminRig {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	a := adminRigOn(t, e)
	a.a.UIDir = dir
	a.h = a.a.Handler()
	return a
}

// raw sends a request without the organisation path rewriting the rig's do() applies.
func (a *adminRig) raw(path string, opts ...opt) resp {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "10.1.1.1:5555"
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	a.a.Handler().ServeHTTP(rec, req)
	return resp{rec}
}

func TestSecurityHeadersOnTheUIAndTheAPI(t *testing.T) {
	a := uiRig(t)
	for _, path := range []string{"/", "/topology", "/index.html"} {
		r := a.raw(path)
		h := r.Header()
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "img-src 'self' data:", "style-src 'self' 'unsafe-inline'", "font-src 'self'", "connect-src 'self'",
			"frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'", "object-src 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP lacks %q: %s", path, want, csp)
			}
		}
		if strings.Contains(csp, "script-src") || strings.Contains(csp, "unsafe-eval") || strings.Contains(strings.SplitN(csp, "style-src", 2)[0], "unsafe-inline") {
			t.Errorf("%s: scripts must fall back to 'self' only: %s", path, csp)
		}
		for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cross-Origin-Opener-Policy": "same-origin"} {
			if h.Get(k) != v {
				t.Errorf("%s: %s = %q", path, k, h.Get(k))
			}
		}
		pp := h.Get("Permissions-Policy")
		for _, f := range []string{"camera=()", "microphone=()", "geolocation=()"} {
			if !strings.Contains(pp, f) {
				t.Errorf("%s: Permissions-Policy lacks %s: %q", path, f, pp)
			}
		}
	}
	api := a.do("GET", "/api/v1/server", nil)
	if api.Header().Get("Content-Security-Policy") == "" || api.Header().Get("Cache-Control") != "no-store" || strings.Contains(api.Header().Get("Content-Security-Policy"), "'self'") {
		t.Errorf("API headers: %v", api.Header())
	}
}

func TestHSTSOnlyForRequestsThatAreHTTPS(t *testing.T) {
	a := uiRig(t)
	hsts := func(r resp) string { return r.Header().Get("Strict-Transport-Security") }
	if v := hsts(a.raw("/")); v != "" {
		t.Errorf("HSTS over plain HTTP: %q", v)
	}
	// a forged header means nothing unless the operator said a proxy is in front
	if v := hsts(a.raw("/", withHeader("X-Forwarded-Proto", "https"))); v != "" {
		t.Errorf("X-Forwarded-Proto trusted without --admin-behind-tls-proxy: %q", v)
	}
	a.a.TrustProxy = true
	v := hsts(a.raw("/", withHeader("X-Forwarded-Proto", "https")))
	if !strings.Contains(v, "max-age=31536000") || !strings.Contains(v, "includeSubDomains") {
		t.Errorf("HSTS behind a TLS proxy: %q", v)
	}
	// directly over TLS
	req := httptest.NewRequest("GET", "https://example.test/", nil) // sets req.TLS
	rec := httptest.NewRecorder()
	a.a.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS over TLS")
	}
}

func TestSessionCookieIsHostPrefixedAndSecureOffLoopback(t *testing.T) {
	a := newAdminRig(t)
	a.user(t, "alex", RoleAdmin)
	find := func(r resp, name string) *http.Cookie {
		for _, c := range r.Result().Cookies() {
			if c.Name == name {
				return c
			}
		}
		return nil
	}
	login := func() resp {
		return a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW})
	}

	// Loopback development (plain HTTP): the old name, not Secure, so it works over http://127.0.0.1.
	r := login()
	plain := find(r, cookieName)
	if plain == nil || plain.Secure || find(r, hostCookieName) != nil && find(r, hostCookieName).MaxAge >= 0 {
		t.Fatalf("loopback cookie: %v", r.Result().Cookies())
	}
	oldSession := plain.Value

	// The operator now serves it off loopback: every cookie is Secure and __Host- prefixed.
	a.a.SecureCookies = true
	r = login()
	hc := find(r, hostCookieName)
	if hc == nil || !hc.Secure || !hc.HttpOnly || hc.Path != "/" || hc.Domain != "" || hc.SameSite != http.SameSiteStrictMode {
		t.Fatalf("__Host- cookie: %+v", hc)
	}
	if old := find(r, cookieName); old != nil && old.MaxAge >= 0 {
		t.Fatalf("both names were set: %+v", old)
	}
	// Both names are read: the new one, and a session opened under the old name before the switch.
	if c := a.do("GET", "/api/v1/auth/me", nil, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: hostCookieName, Value: hc.Value}) }).Code; c != 200 {
		t.Errorf("the __Host- cookie was not accepted: %d", c)
	}
	if c := a.do("GET", "/api/v1/auth/me", nil, withCookie(oldSession)).Code; c != 200 {
		t.Errorf("an existing session under the old name was locked out: %d", c)
	}
	// A garbage value under the preferred name does not shadow a good one under the other.
	both := func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: hostCookieName, Value: ""})
		r.AddCookie(&http.Cookie{Name: cookieName, Value: oldSession})
	}
	if c := a.do("GET", "/api/v1/auth/me", nil, both).Code; c != 200 {
		t.Errorf("an empty __Host- cookie hid the valid session: %d", c)
	}
	// Logging out clears both names (the __Host- one as a Secure cookie, which is the only way a browser honours it).
	out := a.do("POST", "/api/v1/auth/logout", nil, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: hostCookieName, Value: hc.Value}) })
	if out.Code != 204 {
		t.Fatalf("logout: %d", out.Code)
	}
	if c := find(out, hostCookieName); c == nil || c.MaxAge >= 0 || !c.Secure {
		t.Errorf("__Host- cookie not cleared: %+v", c)
	}
	if c := find(out, cookieName); c == nil || c.MaxAge >= 0 {
		t.Errorf("old cookie not cleared: %+v", c)
	}
	if c := a.do("GET", "/api/v1/auth/me", nil, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: hostCookieName, Value: hc.Value}) }).Code; c != 401 {
		t.Errorf("the session survived logout: %d", c)
	}
	// Behind a TLS proxy on loopback the request itself is HTTPS, which also makes it Secure.
	a.a.SecureCookies, a.a.TrustProxy = false, true
	r = a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW}, withHeader("X-Forwarded-Proto", "https"))
	if c := find(r, hostCookieName); c == nil || !c.Secure {
		t.Errorf("cookie behind the proxy: %v", r.Result().Cookies())
	}
}
