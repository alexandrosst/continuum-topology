package server

import (
	"strings"
	"testing"
	"time"
)

// mustTOTPCode computes the code the secret would show at now, the same way an authenticator app does.
func mustTOTPCode(t *testing.T, secret string, now time.Time) string {
	t.Helper()
	step := uint64(now.Unix()) / uint64(totpStep.Seconds())
	code, err := totpAt(secret, step)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// doLogin posts a login attempt from ip and returns the decoded body (a session on success, an error - and
// a "pending" token when two-factor is required - otherwise).
func (a *adminRig) doLogin(t *testing.T, ip, name, pw string) (resp, map[string]any) {
	t.Helper()
	r := a.do("POST", "/api/v1/auth/login", map[string]string{"username": name, "password": pw}, fromIP(ip))
	return r, r.json(t)
}

func (a *adminRig) doLogin2FA(t *testing.T, ip, pending, code string) (resp, map[string]any) {
	t.Helper()
	r := a.do("POST", "/api/v1/auth/login/2fa", map[string]string{"pending": pending, "code": code}, fromIP(ip))
	return r, r.json(t)
}

func TestTwoFactorRequiresASessionToSetUp(t *testing.T) {
	a := newAdminRig(t)
	if r := a.do("POST", "/api/v1/auth/2fa/setup", nil); r.Code != 401 {
		t.Fatalf("setup without a session: %d %s", r.Code, r.Body.String())
	}
}

func TestTwoFactorSetupEnableLoginAndDisable(t *testing.T) {
	a := newAdminRig(t)
	a.account(t, "alice")
	// Every login-style call below is given its own source address: each is logically a separate sign-in
	// attempt (a person does not fire five of them back to back), and giving them distinct addresses keeps
	// this test about two-factor behaviour rather than about the per-address sign-in throttle, which has its
	// own tests.
	cookie := a.login(t, "alice", goodPW) // uses the default test address

	// A fresh account shows no 2FA and can sign in with just a password.
	if me := a.do("GET", "/api/v1/auth/me", nil, withCookie(cookie)).json(t); me["user"].(map[string]any)["twoFactorEnabled"] != false {
		t.Fatalf("fresh account already shows 2FA on: %v", me)
	}

	r := a.do("POST", "/api/v1/auth/2fa/setup", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("setup: %d %s", r.Code, r.Body.String())
	}
	body := r.json(t)
	secret, _ := body["secret"].(string)
	if secret == "" {
		t.Fatalf("no secret in %v", body)
	}
	if uri, _ := body["otpauthUrl"].(string); !strings.HasPrefix(uri, "otpauth://totp/") || !strings.Contains(uri, secret) {
		t.Fatalf("otpauthUrl = %q", uri)
	}

	// A setup nobody finished does not block sign-in.
	if r, _ := a.doLogin(t, "10.9.0.1", "alice", goodPW); r.Code != 200 {
		t.Fatalf("login with an unconfirmed setup: %d %s", r.Code, r.Body.String())
	}

	// The wrong code refuses to confirm it.
	if r := a.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": "000000"}, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("enable with a wrong code: %d %s", r.Code, r.Body.String())
	}

	// The right code turns it on and hands back recovery codes, once.
	r = a.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": mustTOTPCode(t, secret, *a.now)}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("enable: %d %s", r.Code, r.Body.String())
	}
	recovery, _ := r.json(t)["recoveryCodes"].([]any)
	if len(recovery) != RecoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(recovery), RecoveryCodeCount)
	}
	if me := a.do("GET", "/api/v1/auth/me", nil, withCookie(cookie)).json(t); me["user"].(map[string]any)["twoFactorEnabled"] != true {
		t.Fatalf("me does not show 2FA on: %v", me)
	}

	// Signing in now stops at a pending token instead of a session.
	loginIP := "10.9.0.2"
	r, body = a.doLogin(t, loginIP, "alice", goodPW)
	if r.Code != 401 {
		t.Fatalf("login with 2FA on: %d %s", r.Code, r.Body.String())
	}
	if r.cookie() != "" {
		t.Fatal("a session cookie was set before the code was checked")
	}
	pending, _ := body["pending"].(string)
	if pending == "" {
		t.Fatalf("no pending token in %v", body)
	}

	// The wrong code at that step does not open a session either.
	if r, _ := a.doLogin2FA(t, loginIP, pending, "000000"); r.Code != 401 {
		t.Fatalf("login/2fa with a wrong code: %d %s", r.Code, r.Body.String())
	}

	// The right code finishes the sign-in.
	r, _ = a.doLogin2FA(t, loginIP, pending, mustTOTPCode(t, secret, *a.now))
	if r.Code != 200 {
		t.Fatalf("login/2fa: %d %s", r.Code, r.Body.String())
	}
	sessionCookie := r.cookie()
	if sessionCookie == "" {
		t.Fatal("no session cookie after a successful 2FA login")
	}

	// A pending token is one-time: it cannot be replayed even with a fresh, correct code.
	if r, _ := a.doLogin2FA(t, loginIP, pending, mustTOTPCode(t, secret, *a.now)); r.Code != 401 {
		t.Fatalf("replayed pending token: %d %s", r.Code, r.Body.String())
	}

	// Disabling needs the current password.
	if r := a.do("POST", "/api/v1/auth/2fa/disable", map[string]string{"password": "wrong password"}, withCookie(sessionCookie)); r.Code != 400 {
		t.Fatalf("disable with the wrong password: %d %s", r.Code, r.Body.String())
	}
	r = a.do("POST", "/api/v1/auth/2fa/disable", map[string]string{"password": goodPW}, withCookie(sessionCookie))
	if r.Code != 200 {
		t.Fatalf("disable: %d %s", r.Code, r.Body.String())
	}

	// Signing in no longer asks for a code.
	if r, _ := a.doLogin(t, "10.9.0.3", "alice", goodPW); r.Code != 200 {
		t.Fatalf("login after disable: %d %s", r.Code, r.Body.String())
	}
}

func TestTwoFactorRecoveryCodeLoginIsOneTimeUse(t *testing.T) {
	a := newAdminRig(t)
	a.account(t, "bob")
	cookie := a.login(t, "bob", goodPW)

	secret := a.do("POST", "/api/v1/auth/2fa/setup", nil, withCookie(cookie)).json(t)["secret"].(string)
	enable := a.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": mustTOTPCode(t, secret, *a.now)}, withCookie(cookie))
	codes := enable.json(t)["recoveryCodes"].([]any)
	recovery := codes[0].(string)

	// Each round below is its own address, the same reasoning as in the setup/enable test above.
	_, body := a.doLogin(t, "10.9.1.1", "bob", goodPW)
	pending, _ := body["pending"].(string)
	if r, _ := a.doLogin2FA(t, "10.9.1.1", pending, recovery); r.Code != 200 {
		t.Fatalf("login with a recovery code: %d %s", r.Code, r.Body.String())
	}

	// The same code, freshly typed, is spent.
	_, body = a.doLogin(t, "10.9.1.2", "bob", goodPW)
	pending, _ = body["pending"].(string)
	if r, _ := a.doLogin2FA(t, "10.9.1.2", pending, recovery); r.Code != 401 {
		t.Fatalf("reused recovery code: %d %s", r.Code, r.Body.String())
	}

	// A recovery code retyped with different case, spaces and dashes still matches one that is left.
	other := codes[1].(string)
	messy := strings.ToUpper(strings.ReplaceAll(other, "-", " "))
	_, body = a.doLogin(t, "10.9.1.3", "bob", goodPW)
	pending, _ = body["pending"].(string)
	if r, _ := a.doLogin2FA(t, "10.9.1.3", pending, messy); r.Code != 200 {
		t.Fatalf("login with a reformatted recovery code: %d %s", r.Code, r.Body.String())
	}
}

func TestTwoFactorPendingLoginExpires(t *testing.T) {
	a := newAdminRig(t)
	a.account(t, "carol")
	cookie := a.login(t, "carol", goodPW)

	secret := a.do("POST", "/api/v1/auth/2fa/setup", nil, withCookie(cookie)).json(t)["secret"].(string)
	a.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": mustTOTPCode(t, secret, *a.now)}, withCookie(cookie))

	_, body := a.doLogin(t, "10.9.2.1", "carol", goodPW)
	pending, _ := body["pending"].(string)
	*a.now = a.now.Add(pendingLoginTTL + time.Minute)

	if r, _ := a.doLogin2FA(t, "10.9.2.1", pending, mustTOTPCode(t, secret, *a.now)); r.Code != 401 {
		t.Fatalf("expired pending token: %d %s", r.Code, r.Body.String())
	}
}

func TestTwoFactorCannotBeSetUpTwice(t *testing.T) {
	a := newAdminRig(t)
	a.account(t, "dana")
	cookie := a.login(t, "dana", goodPW)

	secret := a.do("POST", "/api/v1/auth/2fa/setup", nil, withCookie(cookie)).json(t)["secret"].(string)
	a.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": mustTOTPCode(t, secret, *a.now)}, withCookie(cookie))

	if r := a.do("POST", "/api/v1/auth/2fa/setup", nil, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("setup while already on: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": mustTOTPCode(t, secret, *a.now)}, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("enable while already on: %d %s", r.Code, r.Body.String())
	}
}
