package passkey

import (
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"continuum/internal/server"
	"continuum/internal/store"
)

// These tests only exercise this package's own plumbing - JSON round-tripping, the RelyingParty→Config
// mapping, and the WebAuthnCredential↔webauthn.Credential conversions - not the WebAuthn protocol itself
// (challenge generation, attestation and assertion verification), which is go-webauthn's own responsibility
// and is already covered by its test suite. The ceremony logic this server actually owns - session
// bookkeeping, rate limits, HTTP routing - is fully tested against a fake provider in internal/server, which
// is the entire reason WebAuthnProvider exists as a seam.

func testRP() server.RelyingParty {
	return server.RelyingParty{ID: "localhost", Origin: "https://localhost", Name: "Continuum Test"}
}

func testUser() server.WebAuthnUser {
	return server.WebAuthnUser{ID: "user-1", Username: "quinn"}
}

func TestBeginRegistrationReturnsOptionsAndASession(t *testing.T) {
	p := New()
	optionsJSON, session, err := p.BeginRegistration(testRP(), testUser())
	if err != nil {
		t.Fatal(err)
	}
	if len(session) == 0 {
		t.Fatal("empty session")
	}
	var v map[string]any
	if err := json.Unmarshal(optionsJSON, &v); err != nil {
		t.Fatalf("options is not valid JSON: %v", err)
	}
	pk, _ := v["publicKey"].(map[string]any)
	if pk == nil {
		t.Fatalf("options has no \"publicKey\" object: %s", optionsJSON)
	}
	if pk["challenge"] == nil {
		t.Fatalf("publicKey has no challenge: %s", optionsJSON)
	}
	// Unmarshal into the real session type - the same one BeginRegistration marshaled - rather than probing
	// for a field name by hand, so this does not depend on guessing go-webauthn's own JSON tags.
	var again webauthn.SessionData
	if err := json.Unmarshal(session, &again); err != nil {
		t.Fatalf("session does not round-trip through webauthn.SessionData: %v", err)
	}
	if len(again.Challenge) == 0 {
		t.Fatalf("session has an empty challenge after round-tripping: %s", session)
	}
}

func TestBeginLoginReturnsOptionsAndASession(t *testing.T) {
	p := New()
	u := testUser()
	u.Credentials = []store.WebAuthnCredential{{CredentialID: []byte("cred-1"), PublicKey: []byte("pub-1"), Transports: []string{"internal", "hybrid"}}}
	optionsJSON, session, err := p.BeginLogin(testRP(), u)
	if err != nil {
		t.Fatal(err)
	}
	if len(session) == 0 {
		t.Fatal("empty session")
	}
	var v map[string]any
	if err := json.Unmarshal(optionsJSON, &v); err != nil {
		t.Fatalf("options is not valid JSON: %v", err)
	}
	if v["publicKey"] == nil {
		t.Fatalf("options has no \"publicKey\" object: %s", optionsJSON)
	}
}

func TestFinishRegistrationRejectsGarbageResponse(t *testing.T) {
	p := New()
	rp, user := testRP(), testUser()
	_, session, err := p.BeginRegistration(rp, user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.FinishRegistration(rp, user, session, []byte("not a webauthn response")); err == nil {
		t.Fatal("expected an error for a garbage response, got none")
	}
}

func TestFinishRegistrationRejectsACorruptedSession(t *testing.T) {
	p := New()
	rp, user := testRP(), testUser()
	if _, err := p.FinishRegistration(rp, user, []byte("not json"), []byte("{}")); err == nil {
		t.Fatal("expected an error for a corrupted session, got none")
	}
}

func TestFinishLoginRejectsGarbageResponse(t *testing.T) {
	p := New()
	rp := testRP()
	u := testUser()
	u.Credentials = []store.WebAuthnCredential{{CredentialID: []byte("cred-1"), PublicKey: []byte("pub-1")}}
	_, session, err := p.BeginLogin(rp, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.FinishLogin(rp, u, session, []byte("not a webauthn response")); err == nil {
		t.Fatal("expected an error for a garbage response, got none")
	}
}

func TestWuserCredentialsMapping(t *testing.T) {
	u := wuser{server.WebAuthnUser{
		ID: "user-1", Username: "quinn",
		Credentials: []store.WebAuthnCredential{
			{CredentialID: []byte("cred-1"), PublicKey: []byte("pub-1"), SignCount: 7, Transports: []string{"usb", "nfc"}},
		},
	}}
	if got := string(u.WebAuthnID()); got != "user-1" {
		t.Errorf("WebAuthnID = %q", got)
	}
	if got := u.WebAuthnName(); got != "quinn" {
		t.Errorf("WebAuthnName = %q", got)
	}
	creds := u.WebAuthnCredentials()
	if len(creds) != 1 {
		t.Fatalf("credentials = %v", creds)
	}
	c := creds[0]
	if string(c.ID) != "cred-1" || string(c.PublicKey) != "pub-1" || c.Authenticator.SignCount != 7 {
		t.Fatalf("credential = %+v", c)
	}
	if len(c.Transport) != 2 || string(c.Transport[0]) != "usb" || string(c.Transport[1]) != "nfc" {
		t.Fatalf("transports = %v", c.Transport)
	}
}
