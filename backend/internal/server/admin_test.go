package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"continuum/internal/store"
)

func init() { // cheap argon2 for tests; production values are exercised by TestProductionArgonParameters
	argonMemoryKiB, argonTime, argonThreads = 1024, 1, 1
}

const goodPW = "correct horse battery"

type adminRig struct {
	*env
	h http.Handler
	a *Admin
}

func newAdminRig(t *testing.T) *adminRig { return adminRigOn(t, newEnv(t)) }

func adminRigOn(t *testing.T, e *env) *adminRig {
	t.Helper()
	p := NewPlatform(e.base, nil)
	a := &Admin{P: p, C: e.base, AgentAddr: "x:1", ChartRef: "chart"}
	return &adminRig{env: e, h: a.Handler(), a: a}
}

// hub is organisation org-1's live hub.
func (a *adminRig) hub() *Hub {
	t, err := a.a.P.Tenant(a.ctx, "org-1")
	if err != nil {
		panic(err)
	}
	return t.Hub
}

// orgPath sends the organisation-scoped routes to org-1, so tests can keep writing /api/v1/state.
func orgPath(p string) string {
	rest := strings.TrimPrefix(p, "/api/v1/")
	for _, g := range []string{"auth/", "server", "orgs", "invites/preview", "invites/accept"} {
		if strings.HasPrefix(rest, g) {
			return p
		}
	}
	return "/api/v1/orgs/org-1/" + rest
}

type resp struct {
	*httptest.ResponseRecorder
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %q", r.Body.String())
	}
	return m
}

func (r resp) cookie() string {
	for _, c := range r.Result().Cookies() {
		if c.Name == cookieName {
			return c.Value
		}
	}
	return ""
}

type opt func(*http.Request)

func withCookie(v string) opt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: v}) }
}
func withHeader(k, v string) opt { return func(r *http.Request) { r.Header.Set(k, v) } }
func withoutXRW() opt            { return func(r *http.Request) { r.Header.Del("X-Requested-With") } }
func fromIP(ip string) opt       { return func(r *http.Request) { r.RemoteAddr = ip + ":4000" } }

func (a *adminRig) do(method, path string, body any, opts ...opt) resp {
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, orgPath(path), rd)
	req.RemoteAddr = "10.1.1.1:5555"
	req.Header.Set("X-Requested-With", "test")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return resp{rec}
}

// user makes a settled (no forced change) account and signs it in, returning the session cookie.
func (a *adminRig) user(t *testing.T, name, role string) (id, cookie string) {
	t.Helper()
	id = a.account(t, name)
	if err := a.st.AddMember(a.ctx, store.Membership{OrgID: "org-1", UserID: id, Role: role, CreatedAt: *a.now}); err != nil {
		t.Fatal(err)
	}
	return id, a.login(t, name, goodPW)
}

// account makes a settled account (password goodPW) that belongs to no organisation.
func (a *adminRig) account(t *testing.T, name string) string {
	t.Helper()
	u := store.User{ID: "u-" + randHex(5), Username: name, PasswordHash: mustHash(t, goodPW), CreatedAt: *a.now}
	if err := a.st.CreateUser(a.ctx, u); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (a *adminRig) login(t *testing.T, name, pw string) string {
	t.Helper()
	r := a.do("POST", "/api/v1/auth/login", map[string]string{"username": name, "password": pw})
	if r.Code != 200 {
		t.Fatalf("login as %s: %d %s", name, r.Code, r.Body.String())
	}
	return r.cookie()
}

func TestBootstrapAdminForcesAPasswordChangeAndSignsOutOtherBrowsers(t *testing.T) {
	a := adminRigOn(t, newEnvBare(t))
	created, first, err := a.base.BootstrapAdmin(a.ctx, "")
	if err != nil || !created || len(first) < 16 {
		t.Fatalf("bootstrap: %v %v %q", created, err, first)
	}
	if again, _, _ := a.base.BootstrapAdmin(a.ctx, ""); again {
		t.Fatal("bootstrap must do nothing once a user exists")
	}
	c1, c2 := a.login(t, "admin", first), a.login(t, "ADMIN", first) // user names are case-insensitive

	if r := a.do("GET", "/api/v1/state", nil, withCookie(c1)); r.Code != 403 {
		t.Fatalf("state before the password change: %d", r.Code)
	}
	me := a.do("GET", "/api/v1/auth/me", nil, withCookie(c1)).json(t)["user"].(map[string]any)
	if me["mustChangePassword"] != true {
		t.Fatalf("me = %v", me)
	}
	change := func(cur, next string) int {
		return a.do("POST", "/api/v1/auth/password", map[string]string{"current": cur, "new": next}, withCookie(c1)).Code
	}
	if change("wrong-current-password", "another long password") != 400 {
		t.Error("wrong current password accepted")
	}
	if change(first, "short") != 400 {
		t.Error("short password accepted")
	}
	if change(first, first) != 400 {
		t.Error("unchanged password accepted")
	}
	if change(first, "a brand new passphrase") != 200 {
		t.Fatal("valid change refused")
	}
	if r := a.do("GET", "/api/v1/state", nil, withCookie(c1)); r.Code != 200 {
		t.Fatalf("state after the change: %d", r.Code)
	}
	if r := a.do("GET", "/api/v1/auth/me", nil, withCookie(c2)); r.Code != 401 {
		t.Fatalf("the other browser stayed signed in: %d", r.Code)
	}
	if a.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": first}).Code != 401 {
		t.Fatal("the one-time password still works")
	}
}

func TestOperatorSuppliedAdminPasswordIsNotForcedToChange(t *testing.T) {
	a := adminRigOn(t, newEnvBare(t))
	if _, _, err := a.base.BootstrapAdmin(a.ctx, "short"); err == nil {
		t.Fatal("a weak operator password was accepted")
	}
	if created, gen, err := a.base.BootstrapAdmin(a.ctx, "from the secret store 1"); err != nil || !created || gen != "" {
		t.Fatalf("%v %v %q", created, err, gen)
	}
	c := a.login(t, "admin", "from the secret store 1")
	if a.do("GET", "/api/v1/state", nil, withCookie(c)).Code != 200 {
		t.Fatal("a chosen password should work at once")
	}
}

func TestWrongPasswordAndUnknownUserAreIndistinguishableAndThrottled(t *testing.T) {
	a := newAdminRig(t)
	a.user(t, "alex", RoleAdmin)
	wrong := a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": "not the password"}, fromIP("10.9.0.1"))
	ghost := a.do("POST", "/api/v1/auth/login", map[string]string{"username": "nobody", "password": "not the password"}, fromIP("10.9.0.2"))
	if wrong.Code != 401 || ghost.Code != 401 || wrong.Body.String() != ghost.Body.String() {
		t.Fatalf("wrong password %d %q vs unknown user %d %q", wrong.Code, wrong.Body.String(), ghost.Code, ghost.Body.String())
	}
	if wrong.cookie() != "" {
		t.Fatal("a failed login set a cookie")
	}
	// Six tries from one address on one account: the fifth is the last that is answered.
	var limited int
	for i := 0; i < 8; i++ {
		if a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": "guess number x"}, fromIP("10.9.0.3")).Code == 429 {
			limited++
		}
	}
	if limited < 3 {
		t.Fatalf("only %d of 8 guesses throttled", limited)
	}
	// The right password from the throttled address is refused too, until the bucket refills.
	if a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW}, fromIP("10.9.0.3")).Code != 429 {
		t.Error("throttle can be bypassed by guessing correctly")
	}
	// Another address is not throttled by the first one's bucket, but the account itself is now in backoff
	// (see TestLoginFailuresBackOffPerAccountWhateverTheAddress): a short wait, then it works.
	*a.now = a.now.Add(time.Minute)
	if a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW}, fromIP("10.9.0.4")).Code != 200 {
		t.Error("one address locked everyone out")
	}
	var failures int
	events, _ := a.st.ListAudit(a.ctx, "", 100) // failed sign-ins belong to no organisation
	for _, e := range events {
		if e.Action == "login-failed" {
			failures++
			if strings.Contains(e.Detail, "guess") {
				t.Error("a password was written to the audit log")
			}
		}
	}
	if failures < 5 {
		t.Errorf("only %d failed logins audited", failures)
	}
}

func TestOnlyFailedSessionChecksAreThrottled(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	for i := 0; i < 300; i++ { // the UI polls constantly with a valid session
		if c := a.do("GET", "/api/v1/info", nil, withCookie(cookie)).Code; c != 200 {
			t.Fatalf("poll %d: %d", i, c)
		}
	}
	var limited int
	for i := 0; i < 40; i++ {
		if a.do("GET", "/api/v1/info", nil, withCookie("cns_"+strings.Repeat("a", 43))).Code == 429 {
			limited++
		}
	}
	if limited < 20 {
		t.Fatalf("only %d of 40 forged sessions throttled", limited)
	}
	if c := a.do("GET", "/api/v1/info", nil).Code; c != 401 && c != 429 {
		t.Fatalf("anonymous request: %d", c)
	}
}

func TestRolesViewerReadsAdminWrites(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "boss", RoleAdmin)
	_, viewer := a.user(t, "reader", RoleViewer)
	for _, p := range []string{"/api/v1/info", "/api/v1/state", "/api/v1/workspace"} {
		if c := a.do("GET", p, nil, withCookie(viewer)).Code; c != 200 {
			t.Errorf("viewer GET %s: %d", p, c)
		}
	}
	forbidden := []struct{ m, p string }{
		{"GET", "/api/v1/tokens"}, {"POST", "/api/v1/tokens"}, {"PUT", "/api/v1/workspace"},
		{"POST", "/api/v1/agents/ag-x/approve"}, {"POST", "/api/v1/agents/ag-x/revoke"},
		{"GET", "/api/v1/invites"}, {"POST", "/api/v1/invites"}, {"POST", "/api/v1/members/u-x/role"},
	}
	for _, f := range forbidden {
		if c := a.do(f.m, f.p, map[string]any{}, withCookie(viewer), withHeader("If-Match", "0")).Code; c != 403 {
			t.Errorf("viewer %s %s: %d, want 403", f.m, f.p, c)
		}
	}
	if c := a.do("GET", "/api/v1/members", nil, withCookie(viewer)).Code; c != 200 {
		t.Errorf("viewer GET members: %d", c)
	}
	if c := a.do("GET", "/api/v1/invites", nil, withCookie(admin)).Code; c != 200 {
		t.Errorf("admin GET invites: %d", c)
	}
	// The audit trail names the person, not "admin".
	a.do("POST", "/api/v1/tokens", map[string]any{"name": "edge", "tier": 1}, withCookie(admin))
	events, _ := a.st.ListAudit(a.ctx, "org-1", 20)
	var seen bool
	for _, e := range events {
		seen = seen || (e.Action == "token-created" && e.Actor == "boss")
	}
	if !seen {
		t.Error("token creation was not attributed to the signed-in user")
	}
}

func TestCSRFDefences(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	body := map[string]any{"name": "edge", "tier": 1}
	if c := a.do("POST", "/api/v1/tokens", body, withCookie(cookie), withoutXRW()).Code; c != 403 {
		t.Errorf("no custom header: %d", c)
	}
	if c := a.do("POST", "/api/v1/tokens", body, withCookie(cookie), withHeader("Origin", "https://evil.example")).Code; c != 403 {
		t.Errorf("foreign origin: %d", c)
	}
	if c := a.do("POST", "/api/v1/tokens", body, withCookie(cookie), withHeader("Origin", "http://example.com")).Code; c != 201 {
		t.Errorf("same origin refused: %d", c) // httptest requests use host example.com
	}
	if c := a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW}, withHeader("Origin", "https://evil.example")).Code; c != 403 {
		t.Errorf("cross-site login: %d", c)
	}
	// An allowed development origin works, and gets credentialed CORS headers; others get none.
	a.a.Origins = []string{"http://localhost:5173"}
	r := a.do("GET", "/api/v1/info", nil, withCookie(cookie), withHeader("Origin", "http://localhost:5173"))
	if r.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || r.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("CORS headers for the allowed origin: %v", r.Header())
	}
	r = a.do("GET", "/api/v1/info", nil, withCookie(cookie), withHeader("Origin", "https://evil.example"))
	if r.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS granted to a foreign origin")
	}
}

func TestSessionCookieFlags(t *testing.T) {
	a := newAdminRig(t)
	a.user(t, "alex", RoleAdmin)
	login := func(opts ...opt) *http.Cookie {
		r := a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": goodPW}, opts...)
		for _, c := range r.Result().Cookies() {
			if (c.Name == cookieName || c.Name == hostCookieName) && c.MaxAge >= 0 {
				return c
			}
		}
		t.Fatal("no cookie")
		return nil
	}
	c := login()
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Fatalf("plain HTTP cookie: %+v", c)
	}
	if login(withHeader("X-Forwarded-Proto", "https")).Secure {
		t.Error("X-Forwarded-Proto trusted without --admin-behind-tls-proxy")
	}
	a.a.TrustProxy = true
	if !login(withHeader("X-Forwarded-Proto", "https")).Secure {
		t.Error("cookie not Secure behind a TLS proxy")
	}
	// Behind the proxy, the address that counts is the one the proxy saw, not the proxy's own.
	if ip := a.a.clientIP(httptest.NewRequest("GET", "/", nil)); ip != "192.0.2.1" {
		t.Errorf("no header: %s", ip)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if ip := a.a.clientIP(req); ip != "203.0.113.9" {
		t.Errorf("client ip behind proxy = %s (a client-supplied left-most entry must not be trusted)", ip)
	}
}

func TestSessionsExpireIdleAbsoluteAndOnDisable(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "boss", RoleAdmin)
	victimID, victim := a.user(t, "victim", RoleViewer)
	start := *a.now
	check := func(c string) int { return a.do("GET", "/api/v1/info", nil, withCookie(c)).Code }

	if check(victim) != 200 {
		t.Fatal("fresh session refused")
	}
	// Disabling signs the person out at once.
	now := time.Now()
	if err := a.st.SetDisabled(a.ctx, victimID, &now); err != nil {
		t.Fatal(err)
	}
	if check(victim) != 401 {
		t.Error("a disabled user's session still works")
	}
	if a.do("POST", "/api/v1/auth/login", map[string]string{"username": "victim", "password": goodPW}).Code != 401 {
		t.Error("a disabled user can sign in")
	}
	if err := a.st.SetDisabled(a.ctx, victimID, nil); err != nil {
		t.Fatal(err)
	}
	if a.do("POST", "/api/v1/auth/login", map[string]string{"username": "victim", "password": goodPW}).Code != 200 {
		t.Error("re-enabled user cannot sign in")
	}

	// Idle: unused for longer than SessionIdle.
	*a.now = start.Add(SessionIdle + time.Minute)
	if check(admin) != 401 {
		t.Error("an idle session survived")
	}
	// Absolute: kept busy, but older than SessionMax.
	*a.now = start
	fresh := a.login(t, "boss", goodPW)
	for h := 1; h <= 7*24; h += 6 {
		*a.now = start.Add(time.Duration(h) * time.Hour)
		if h < 7*24 && check(fresh) != 200 {
			t.Fatalf("an active session was dropped after %d h", h)
		}
	}
	*a.now = start.Add(SessionMax + time.Hour)
	if check(fresh) != 401 {
		t.Error("a session outlived the absolute limit")
	}
	*a.now = start
	// Logout ends it and clears the cookie.
	c := a.login(t, "boss", goodPW)
	out := a.do("POST", "/api/v1/auth/logout", nil, withCookie(c))
	if out.Code != 204 || check(c) != 401 {
		t.Errorf("logout: %d, then %d", out.Code, check(c))
	}
	if ck := out.Result().Cookies(); len(ck) == 0 || ck[0].MaxAge >= 0 {
		t.Error("logout did not clear the cookie")
	}
}

func TestWorkspaceRevisionsAndConflicts(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "boss", RoleAdmin)
	_, viewer := a.user(t, "reader", RoleViewer)

	first := a.do("GET", "/api/v1/workspace", nil, withCookie(admin)).json(t)
	if first["rev"].(float64) != 0 || first["data"] != nil {
		t.Fatalf("empty workspace = %v", first)
	}
	put := func(rev string, body any, opts ...opt) resp {
		return a.do("PUT", "/api/v1/workspace", body, append([]opt{withCookie(admin), withHeader("If-Match", rev)}, opts...)...)
	}
	doc := `{"schemaVersion":3,"clusters":[{"id":"cl-1"}]}`
	if r := put("0", []byte(doc)); r.Code != 200 || r.json(t)["rev"].(float64) != 1 {
		t.Fatalf("first save: %d %s", r.Code, r.Body.String())
	}
	// A second browser that still thinks the workspace is empty is told so, and learns who wrote it.
	r := put("0", []byte(`{"schemaVersion":3,"clusters":[]}`))
	if r.Code != 409 || r.json(t)["rev"].(float64) != 1 || r.json(t)["updatedBy"] != "boss" {
		t.Fatalf("stale save: %d %s", r.Code, r.Body.String())
	}
	got := a.do("GET", "/api/v1/workspace", nil, withCookie(viewer)).json(t)
	if got["rev"].(float64) != 1 || got["data"].(map[string]any)["schemaVersion"].(float64) != 4 { // the server saves the current format
		t.Fatalf("stored doc = %v", got)
	}
	if meta := a.do("GET", "/api/v1/workspace?meta=1", nil, withCookie(viewer)).json(t); meta["data"] != nil || meta["rev"].(float64) != 1 {
		t.Fatalf("meta = %v", meta)
	}
	if r := put("1", []byte(`{"schemaVersion":3,"clusters":[]}`)); r.Code != 200 || r.json(t)["rev"].(float64) != 2 {
		t.Fatalf("second save: %d %s", r.Code, r.Body.String())
	}

	if r := a.do("PUT", "/api/v1/workspace", []byte(doc), withCookie(admin)); r.Code != 428 {
		t.Errorf("missing If-Match: %d", r.Code)
	}
	if r := put("abc", []byte(doc)); r.Code != 428 {
		t.Errorf("bad If-Match: %d", r.Code)
	}
	for name, bad := range map[string][]byte{"not json": []byte("<html>"), "an array": []byte("[1]"), "no schema": []byte(`{"clusters":[]}`), "string schema": []byte(`{"schemaVersion":"3"}`)} {
		if r := put("2", bad); r.Code != 400 {
			t.Errorf("%s accepted: %d", name, r.Code)
		}
	}
	huge := []byte(`{"schemaVersion":3,"x":"` + strings.Repeat("a", MaxWorkspaceBytes) + `"}`)
	if r := put("2", huge); r.Code != 400 && r.Code != 413 {
		t.Errorf("oversized workspace: %d", r.Code)
	}
}

func TestWorkspaceSavesRaceExactlyOneWinner(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins, conflicts int
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.core.SaveWorkspace(e.ctx, fmt.Sprint("u", i), 0, []byte(`{"schemaVersion":3}`))
			mu.Lock()
			defer mu.Unlock()
			switch kindOf(err) {
			case 0:
				wins++
			case KindConflict:
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || conflicts != 19 {
		t.Fatalf("%d winners and %d conflicts among 20 simultaneous first saves", wins, conflicts)
	}
}

func TestPasswordHashing(t *testing.T) {
	h1, _ := HashPassword(goodPW)
	h2, _ := HashPassword(goodPW)
	if h1 == h2 || !strings.HasPrefix(h1, "$argon2id$v=19$") {
		t.Fatalf("hashes must be salted PHC strings: %q %q", h1, h2)
	}
	if ok, err := VerifyPassword(goodPW, h1); !ok || err != nil {
		t.Fatalf("verify: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(goodPW+"x", h1); ok {
		t.Fatal("wrong password verified")
	}
	for _, bad := range []string{"", "plaintext", "$argon2id$v=19$m=1,t=1,p=1$xx$yy", "$bcrypt$x", "$argon2id$v=19$m=99999999,t=1,p=1$AAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if ok, err := VerifyPassword(goodPW, bad); ok || err == nil {
			t.Errorf("hash %q was not rejected as malformed", bad)
		}
	}
	if CheckPasswordPolicy("alex", "Alex") == nil || CheckPasswordPolicy("alex", "aaaaaaaaaaaaaaaa") == nil ||
		CheckPasswordPolicy("alex", strings.Repeat("x1", 65)) == nil || CheckPasswordPolicy("alex", goodPW) != nil {
		t.Error("password policy")
	}
}

func TestProductionArgonParameters(t *testing.T) {
	m, ti, p := argonMemoryKiB, argonTime, argonThreads
	defer func() { argonMemoryKiB, argonTime, argonThreads = m, ti, p }()
	argonMemoryKiB, argonTime, argonThreads = 64*1024, 2, 2
	start := time.Now()
	h, _ := HashPassword(goodPW)
	if !strings.Contains(h, "m=65536,t=2,p=2") {
		t.Fatalf("parameters not recorded in the hash: %s", h)
	}
	if ok, _ := VerifyPassword(goodPW, h); !ok {
		t.Fatal("verify")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("two hashes took %v", d)
	}
	// A hash made with cheaper settings still verifies: parameters come from the hash.
	argonMemoryKiB, argonTime, argonThreads = 1024, 1, 1
	old, _ := HashPassword(goodPW)
	argonMemoryKiB, argonTime, argonThreads = 64*1024, 2, 2
	if ok, _ := VerifyPassword(goodPW, old); !ok {
		t.Fatal("older hash no longer verifies")
	}
}

func TestOfflineRecoveryResetsOrCreatesAnAccount(t *testing.T) {
	a := newAdminRig(t)
	id, cookie := a.user(t, "alex", RoleViewer)
	pw, err := a.base.RecoverPassword(a.ctx, "alex")
	if err != nil {
		t.Fatal(err)
	}
	if a.do("GET", "/api/v1/info", nil, withCookie(cookie)).Code != 401 {
		t.Error("recovery left old sessions alive")
	}
	c := a.login(t, "alex", pw)
	if a.do("GET", "/api/v1/info", nil, withCookie(c)).Code != 403 {
		t.Error("recovered account is not forced to change its password")
	}
	if m, err := a.st.GetMembership(a.ctx, "org-1", id); err != nil || m.Role != RoleViewer {
		t.Errorf("recovery must not change memberships: %+v %v", m, err)
	}
	pw2, err := a.base.RecoverPassword(a.ctx, "brand-new")
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.st.GetUserByName(a.ctx, "brand-new")
	if orgs, _ := a.st.ListMyOrgs(a.ctx, u.ID); err != nil || len(orgs) != 0 || pw2 == "" {
		t.Errorf("recovery of an unknown account should create it without any organisation: %+v %v %v", u, orgs, err)
	}
}
