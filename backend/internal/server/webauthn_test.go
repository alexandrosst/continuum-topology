package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"continuum/internal/store"
)

// fakeWebAuthnResponse is the only shape fakeWebAuthn's Finish methods understand: real WebAuthn responses
// carry a signed attestation or assertion object, but everything this package is responsible for - routing,
// rate limits, session bookkeeping, storing and looking up a credential - can be exercised without any of
// that, which is the entire point of testing against WebAuthnProvider rather than the real library.
type fakeWebAuthnResponse struct {
	CredentialID string `json:"credentialId"`
}

func fakeResponse(t *testing.T, credentialID string) []byte {
	t.Helper()
	b, err := json.Marshal(fakeWebAuthnResponse{CredentialID: credentialID})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeWebAuthn is a WebAuthnProvider that does no cryptography at all: Begin* returns a fixed options object
// and remembers nothing but the user, and Finish* trusts fakeWebAuthnResponse's CredentialID outright. failWith,
// when set, makes every Finish call fail, the way a real forged or mismatched response would.
type fakeWebAuthn struct {
	failWith string
}

func (f *fakeWebAuthn) BeginRegistration(rp RelyingParty, user WebAuthnUser) ([]byte, []byte, error) {
	return []byte(`{"publicKey":{"challenge":"fake-reg-challenge"}}`), []byte("reg:" + user.ID), nil
}

func (f *fakeWebAuthn) FinishRegistration(rp RelyingParty, user WebAuthnUser, session, response []byte) (store.WebAuthnCredential, error) {
	if f.failWith != "" {
		return store.WebAuthnCredential{}, errors.New(f.failWith)
	}
	var resp fakeWebAuthnResponse
	if err := json.Unmarshal(response, &resp); err != nil || resp.CredentialID == "" {
		return store.WebAuthnCredential{}, errors.New("bad response")
	}
	return store.WebAuthnCredential{CredentialID: []byte(resp.CredentialID), PublicKey: []byte("pub:" + resp.CredentialID)}, nil
}

func (f *fakeWebAuthn) BeginLogin(rp RelyingParty, user WebAuthnUser) ([]byte, []byte, error) {
	return []byte(`{"publicKey":{"challenge":"fake-login-challenge"}}`), []byte("login:" + user.ID), nil
}

func (f *fakeWebAuthn) FinishLogin(rp RelyingParty, user WebAuthnUser, session, response []byte) ([]byte, uint32, error) {
	if f.failWith != "" {
		return nil, 0, errors.New(f.failWith)
	}
	var resp fakeWebAuthnResponse
	if err := json.Unmarshal(response, &resp); err != nil {
		return nil, 0, errors.New("bad response")
	}
	for _, c := range user.Credentials {
		if string(c.CredentialID) == resp.CredentialID {
			return c.CredentialID, c.SignCount + 1, nil
		}
	}
	return nil, 0, errors.New("no such credential")
}

func withHost(h string) opt { return func(r *http.Request) { r.Host = h } }

func TestPasskeyRegistrationRequiresASession(t *testing.T) {
	a := newAdminRig(t)
	a.base.WebAuthn = &fakeWebAuthn{}
	if r := a.do("POST", "/api/v1/auth/webauthn/register/begin", nil); r.Code != 401 {
		t.Fatalf("begin without a session: %d %s", r.Code, r.Body.String())
	}
}

func TestPasskeysNotOfferedWithNoProviderWiredIn(t *testing.T) {
	a := newAdminRig(t) // a.base.WebAuthn left nil, as a build that omitted it would leave it
	a.account(t, "nyx")
	cookie := a.login(t, "nyx", goodPW)
	if r := a.do("POST", "/api/v1/auth/webauthn/register/begin", nil, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("begin with no provider: %d %s", r.Code, r.Body.String())
	}
}

func TestPasskeyRegistrationRefusedFromABareIPAddress(t *testing.T) {
	a := newAdminRig(t)
	a.base.WebAuthn = &fakeWebAuthn{}
	a.account(t, "opal")
	cookie := a.login(t, "opal", goodPW)
	if r := a.do("POST", "/api/v1/auth/webauthn/register/begin", nil, withCookie(cookie), withHost("192.0.2.10:8443")); r.Code != 409 {
		t.Fatalf("begin from a bare IP host: %d %s", r.Code, r.Body.String())
	}
}

// TestPasskeyRegisterLoginRenameAndRemove is the happy path end to end, kept to a handful of account-settings
// calls for the same reason TestEmailVerifyConfirmEnableLoginAndDisable is: they share a rate-limit bucket
// with TOTP and email setup.
func TestPasskeyRegisterLoginRenameAndRemove(t *testing.T) {
	a := newAdminRig(t)
	a.base.WebAuthn = &fakeWebAuthn{}
	a.account(t, "quinn")
	cookie := a.login(t, "quinn", goodPW)

	if r := a.do("POST", "/api/v1/auth/webauthn/register/begin", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("begin: %d %s", r.Code, r.Body.String())
	} else if !bytesLookLikeOptions(r.Body.Bytes()) {
		t.Fatalf("begin did not return the options object verbatim: %s", r.Body.String())
	}

	credID := base64.RawURLEncoding.EncodeToString([]byte("cred-1"))
	r := a.do("POST", "/api/v1/auth/webauthn/register/finish", map[string]any{"name": "YubiKey", "response": json.RawMessage(fakeResponse(t, "cred-1"))}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("finish: %d %s", r.Code, r.Body.String())
	}
	u := r.json(t)["user"].(map[string]any)
	passkeys, _ := u["passkeys"].([]any)
	if len(passkeys) != 1 {
		t.Fatalf("passkeys after registering: %v", u)
	}
	first := passkeys[0].(map[string]any)
	if first["name"] != "YubiKey" || first["id"] != credID {
		t.Fatalf("passkey doc = %v", first)
	}

	// Signing in now offers webauthn as a method (this account has no TOTP or email).
	loginIP := "10.9.6.1"
	_, body := a.doLogin(t, loginIP, "quinn", goodPW)
	pending, _ := body["pending"].(string)
	if pending == "" {
		t.Fatalf("no pending token: %v", body)
	}
	methods, _ := body["methods"].([]any)
	if len(methods) != 1 || methods[0] != "webauthn" {
		t.Fatalf("methods = %v, want [webauthn]", methods)
	}

	if r := a.do("POST", "/api/v1/auth/login/2fa/webauthn/begin", map[string]string{"pending": pending}, fromIP(loginIP)); r.Code != 200 {
		t.Fatalf("login begin: %d %s", r.Code, r.Body.String())
	} else if !bytesLookLikeOptions(r.Body.Bytes()) {
		t.Fatalf("login begin did not return the options object verbatim: %s", r.Body.String())
	}

	if r := a.do("POST", "/api/v1/auth/login/2fa/webauthn/finish", map[string]any{"pending": pending, "response": json.RawMessage(fakeResponse(t, "not-the-right-credential"))}, fromIP(loginIP)); r.Code != 401 {
		t.Fatalf("login finish with the wrong credential: %d %s", r.Code, r.Body.String())
	}
	// A failed assertion, like a real one-time WebAuthn challenge, can't be resubmitted: the browser would
	// have to start a fresh ceremony, which here means calling begin again for the same pending sign-in.
	if r := a.do("POST", "/api/v1/auth/login/2fa/webauthn/begin", map[string]string{"pending": pending}, fromIP(loginIP)); r.Code != 200 {
		t.Fatalf("login begin (retry): %d %s", r.Code, r.Body.String())
	}
	r = a.do("POST", "/api/v1/auth/login/2fa/webauthn/finish", map[string]any{"pending": pending, "response": json.RawMessage(fakeResponse(t, "cred-1"))}, fromIP(loginIP))
	if r.Code != 200 {
		t.Fatalf("login finish: %d %s", r.Code, r.Body.String())
	}
	sessionCookie := r.cookie()
	if sessionCookie == "" {
		t.Fatal("no session cookie after a successful passkey login")
	}
	stored, err := a.st.GetUser(a.ctx, a.accountID(t, "quinn"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.WebAuthnCredentials) != 1 || stored.WebAuthnCredentials[0].SignCount != 1 {
		t.Fatalf("sign count not bumped: %+v", stored.WebAuthnCredentials)
	}

	if r := a.do("POST", "/api/v1/auth/webauthn/"+credID+"/rename", map[string]string{"name": "Work YubiKey"}, withCookie(sessionCookie)); r.Code != 200 {
		t.Fatalf("rename: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t)["user"].(map[string]any)["passkeys"].([]any)[0].(map[string]any)["name"]; got != "Work YubiKey" {
		t.Fatalf("name after rename = %v", got)
	}

	if r := a.do("POST", "/api/v1/auth/webauthn/"+credID+"/remove", map[string]string{"password": "wrong"}, withCookie(sessionCookie)); r.Code != 400 {
		t.Fatalf("remove with the wrong password: %d %s", r.Code, r.Body.String())
	}
	r = a.do("POST", "/api/v1/auth/webauthn/"+credID+"/remove", map[string]string{"password": goodPW}, withCookie(sessionCookie))
	if r.Code != 200 {
		t.Fatalf("remove: %d %s", r.Code, r.Body.String())
	}
	if left, _ := r.json(t)["user"].(map[string]any)["passkeys"].([]any); len(left) != 0 {
		t.Fatalf("passkeys after removing the only one: %v", left)
	}

	// Signing in no longer asks for a second factor.
	if r, _ := a.doLogin(t, "10.9.6.2", "quinn", goodPW); r.Code != 200 {
		t.Fatalf("login after removing the only passkey: %d %s", r.Code, r.Body.String())
	}
}

func TestPasskeyRegistrationRejectsAForgedResponse(t *testing.T) {
	a := newAdminRig(t)
	a.base.WebAuthn = &fakeWebAuthn{failWith: "signature does not match"}
	a.account(t, "ren")
	cookie := a.login(t, "ren", goodPW)
	a.do("POST", "/api/v1/auth/webauthn/register/begin", nil, withCookie(cookie))
	if r := a.do("POST", "/api/v1/auth/webauthn/register/finish", map[string]any{"response": json.RawMessage(fakeResponse(t, "cred-x"))}, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("finish with a provider error: %d %s", r.Code, r.Body.String())
	}
}

// bytesLookLikeOptions checks that a begin* handler wrote the provider's JSON straight through rather than
// re-wrapping or re-encoding it.
func bytesLookLikeOptions(b []byte) bool {
	var v map[string]any
	return json.Unmarshal(b, &v) == nil && v["publicKey"] != nil
}

// accountID looks up an account created with (*adminRig).account by name, since that helper only returns
// the id at creation time and this file wants it again later without threading it through every call.
func (a *adminRig) accountID(t *testing.T, name string) string {
	t.Helper()
	u, err := a.st.GetUserByName(a.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}
