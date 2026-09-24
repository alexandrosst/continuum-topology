package server

import (
	"net/smtp"
	"regexp"
	"testing"
)

// sentMail is one message captureMail recorded instead of actually delivering.
type sentMail struct{ to, subject, body string }

// captureMail replaces the real SMTP delivery for the duration of the test with one that records what would
// have been sent, so tests can read the code back out of the body instead of running a mail server.
func captureMail(t *testing.T) *[]sentMail {
	t.Helper()
	var sent []sentMail
	orig := smtpSendMail
	smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		sent = append(sent, sentMail{to: to[0], body: string(msg)})
		return nil
	}
	t.Cleanup(func() { smtpSendMail = orig })
	return &sent
}

var codeRe = regexp.MustCompile(`\b(\d{6})\b`)

// codeFrom pulls the 6-digit code out of a captured message body (see emailCodeBody).
func codeFrom(t *testing.T, body string) string {
	t.Helper()
	m := codeRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no 6-digit code found in mail body: %q", body)
	}
	return m[1]
}

func TestEmailVerificationRequiresMailConfigured(t *testing.T) {
	a := newAdminRig(t)
	a.account(t, "alice")
	cookie := a.login(t, "alice", goodPW)

	if r := a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "alice@example.com"}, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("request without mail configured: %d %s", r.Code, r.Body.String())
	}
}

func TestEmailVerifyRejectsABadAddressWithoutSendingOrStoringIt(t *testing.T) {
	a := newAdminRig(t)
	a.base.Mailer = MailConfig{Host: "mail.internal", Port: "587", From: "continuum@example.com"}
	sent := captureMail(t)
	a.account(t, "bea")
	cookie := a.login(t, "bea", goodPW)

	if r := a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "not-an-address"}, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("request with a bad address: %d %s", r.Code, r.Body.String())
	}
	if len(*sent) != 0 {
		t.Fatalf("mail sent for a bad address: %v", *sent)
	}
	// email is omitempty, so a never-set address is simply absent from the JSON rather than an empty string.
	if me := a.do("GET", "/api/v1/auth/me", nil, withCookie(cookie)).json(t); me["user"].(map[string]any)["email"] != nil {
		t.Fatalf("a bad address was stored anyway: %v", me)
	}
}

func TestEmailVerifyResendCooldown(t *testing.T) {
	a := newAdminRig(t)
	a.base.Mailer = MailConfig{Host: "mail.internal", Port: "587", From: "continuum@example.com"}
	sent := captureMail(t)
	a.account(t, "cara")
	cookie := a.login(t, "cara", goodPW)

	if r := a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "cara@example.com"}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("request: %d %s", r.Code, r.Body.String())
	}
	if len(*sent) != 1 {
		t.Fatalf("want one mail sent, got %d", len(*sent))
	}
	if r := a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "cara@example.com"}, withCookie(cookie)); r.Code != 429 {
		t.Fatalf("immediate resend: %d %s", r.Code, r.Body.String())
	}
	if len(*sent) != 1 {
		t.Fatalf("a second mail was sent despite the cooldown: %v", *sent)
	}
}

func TestEmailConfirmWrongCodeAndEnableBeforeVerified(t *testing.T) {
	a := newAdminRig(t)
	a.base.Mailer = MailConfig{Host: "mail.internal", Port: "587", From: "continuum@example.com"}
	captureMail(t)
	a.account(t, "dee")
	cookie := a.login(t, "dee", goodPW)

	a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "dee@example.com"}, withCookie(cookie))
	if r := a.do("POST", "/api/v1/auth/email/confirm", map[string]string{"code": "000000"}, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("confirm with a wrong code: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/auth/email-otp/enable", nil, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("enable before verified: %d %s", r.Code, r.Body.String())
	}
}

// TestEmailVerifyConfirmEnableLoginAndDisable is the happy path end to end: it deliberately keeps the number
// of account-settings calls (which share a rate-limit bucket with TOTP setup/enable/disable) low, and leaves
// the wrong-code and replay edge cases to their own tests above so this one cannot spuriously hit that limit.
func TestEmailVerifyConfirmEnableLoginAndDisable(t *testing.T) {
	a := newAdminRig(t)
	a.base.Mailer = MailConfig{Host: "mail.internal", Port: "587", From: "continuum@example.com"}
	sent := captureMail(t)
	a.account(t, "elle")
	cookie := a.login(t, "elle", goodPW)

	r := a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "Elle@Example.com"}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("request: %d %s", r.Code, r.Body.String())
	}
	// Shown right away, before it is confirmed.
	u := r.json(t)["user"].(map[string]any)
	if u["email"] != "Elle@Example.com" || u["emailVerified"] != false {
		t.Fatalf("pending address not shown: %v", u)
	}
	code := codeFrom(t, (*sent)[len(*sent)-1].body)

	r = a.do("POST", "/api/v1/auth/email/confirm", map[string]string{"code": code}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("confirm: %d %s", r.Code, r.Body.String())
	}
	if u = r.json(t)["user"].(map[string]any); u["emailVerified"] != true {
		t.Fatalf("confirmed address not shown verified: %v", u)
	}

	if r := a.do("POST", "/api/v1/auth/email-otp/enable", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("enable: %d %s", r.Code, r.Body.String())
	}

	// Signing in now offers email as a method (this account has no TOTP).
	loginIP := "10.9.5.1"
	_, body := a.doLogin(t, loginIP, "elle", goodPW)
	pending, _ := body["pending"].(string)
	if pending == "" {
		t.Fatalf("no pending token: %v", body)
	}
	methods, _ := body["methods"].([]any)
	if len(methods) != 1 || methods[0] != "email" {
		t.Fatalf("methods = %v, want [email]", methods)
	}

	// Asking for the code mails one scoped to this pending sign-in.
	*sent = nil
	if r := a.do("POST", "/api/v1/auth/login/2fa/email", map[string]string{"pending": pending}); r.Code != 204 {
		t.Fatalf("request login code: %d %s", r.Code, r.Body.String())
	}
	if len(*sent) != 1 || (*sent)[0].to != "Elle@Example.com" {
		t.Fatalf("login code not mailed: %v", *sent)
	}
	loginCode := codeFrom(t, (*sent)[0].body)

	if r, _ := a.doLogin2FA(t, loginIP, pending, "000000"); r.Code != 401 {
		t.Fatalf("login/2fa with a wrong emailed code: %d %s", r.Code, r.Body.String())
	}
	r2, _ := a.doLogin2FA(t, loginIP, pending, loginCode)
	if r2.Code != 200 {
		t.Fatalf("login/2fa with the emailed code: %d %s", r2.Code, r2.Body.String())
	}
	sessionCookie := r2.cookie()
	if sessionCookie == "" {
		t.Fatal("no session cookie after a successful email 2FA login")
	}

	// Disabling needs the current password, and leaves the address itself verified.
	if r := a.do("POST", "/api/v1/auth/email-otp/disable", map[string]string{"password": "wrong"}, withCookie(sessionCookie)); r.Code != 400 {
		t.Fatalf("disable with the wrong password: %d %s", r.Code, r.Body.String())
	}
	r = a.do("POST", "/api/v1/auth/email-otp/disable", map[string]string{"password": goodPW}, withCookie(sessionCookie))
	if r.Code != 200 {
		t.Fatalf("disable: %d %s", r.Code, r.Body.String())
	}
	if u = r.json(t)["user"].(map[string]any); u["emailOtpEnabled"] != false || u["emailVerified"] != true {
		t.Fatalf("disable changed more than the flag: %v", u)
	}

	// Signing in no longer asks for a code.
	if r, _ := a.doLogin(t, "10.9.5.2", "elle", goodPW); r.Code != 200 {
		t.Fatalf("login after disabling email codes: %d %s", r.Code, r.Body.String())
	}
}

func TestEmailChangeResetsVerificationAndTurnsOtpOff(t *testing.T) {
	a := newAdminRig(t)
	a.base.Mailer = MailConfig{Host: "mail.internal", Port: "587", From: "continuum@example.com"}
	sent := captureMail(t)
	a.account(t, "fay")
	cookie := a.login(t, "fay", goodPW)

	a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "old@example.com"}, withCookie(cookie))
	code := codeFrom(t, (*sent)[len(*sent)-1].body)
	a.do("POST", "/api/v1/auth/email/confirm", map[string]string{"code": code}, withCookie(cookie))
	if r := a.do("POST", "/api/v1/auth/email-otp/enable", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("enable: %d %s", r.Code, r.Body.String())
	}

	// Changing the address drops verification and turns email-OTP back off - a second factor cannot keep
	// pointing at an address nobody has proven receives mail yet.
	r := a.do("POST", "/api/v1/auth/email/request", map[string]string{"email": "new@example.com"}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("request new address: %d %s", r.Code, r.Body.String())
	}
	u := r.json(t)["user"].(map[string]any)
	if u["email"] != "new@example.com" || u["emailVerified"] != false || u["emailOtpEnabled"] != false {
		t.Fatalf("changing address did not reset verification/otp: %v", u)
	}

	// A fresh sign-in no longer offers email as a method.
	_, body := a.doLogin(t, "10.9.5.3", "fay", goodPW)
	if body["pending"] != nil {
		t.Fatalf("login still pending 2FA after email-otp was turned off by the address change: %v", body)
	}
}
