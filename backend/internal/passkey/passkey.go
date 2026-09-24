// Package passkey adapts github.com/go-webauthn/webauthn to the server.WebAuthnProvider interface declared
// in backend/internal/server/webauthn.go. It is the one place in this module that imports go-webauthn, kept
// deliberately separate from everything else so that internal/server (schema, session bookkeeping, HTTP
// routing, and their tests, all built against a fake WebAuthnProvider) never needs the dependency at all.
//
// See README.md in this directory for why that separation exists and how to build and test this package.
package passkey

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"continuum/internal/server"
	"continuum/internal/store"
)

// Provider implements server.WebAuthnProvider with the real WebAuthn protocol. It holds no state of its
// own between calls - every ceremony builds a fresh *webauthn.WebAuthn from the caller's RelyingParty, since
// this server has no single fixed RP ID/origin the way a hosted SaaS would (see server.RelyingParty's doc
// comment: it is computed per request, from whatever Host header the browser actually used). Constructing a
// *webauthn.WebAuthn only validates a small config struct - it does no I/O - so doing that once per call is
// cheap, and it keeps every ceremony as strict about matching the request's own host as the CSRF check
// already is elsewhere in this server.
type Provider struct{}

// New returns a ready-to-use Provider.
func New() *Provider { return &Provider{} }

func webAuthnFor(rp server.RelyingParty) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          rp.ID,
		RPDisplayName: rp.Name,
		RPOrigins:     []string{rp.Origin},
	})
}

// wuser adapts server.WebAuthnUser to go-webauthn's own User interface. It is a thin view, not a copy: it
// exists only for the duration of one Begin/Finish call.
type wuser struct {
	server.WebAuthnUser
}

func (u wuser) WebAuthnID() []byte          { return []byte(u.ID) }
func (u wuser) WebAuthnName() string        { return u.Username }
func (u wuser) WebAuthnDisplayName() string { return u.Username }
func (u wuser) WebAuthnIcon() string        { return "" }

func (u wuser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, len(u.Credentials))
	for i, c := range u.Credentials {
		transports := make([]protocol.AuthenticatorTransport, len(c.Transports))
		for j, t := range c.Transports {
			transports[j] = protocol.AuthenticatorTransport(t)
		}
		out[i] = webauthn.Credential{
			ID:            c.CredentialID,
			PublicKey:     c.PublicKey,
			Transport:     transports,
			Authenticator: webauthn.Authenticator{SignCount: c.SignCount},
		}
	}
	return out
}

// jsonRequest wraps response in a throwaway *http.Request the way go-webauthn's Finish* methods expect: they
// read and parse a request body rather than taking a parsed value directly, because their usual caller is an
// HTTP handler passing the real *http.Request straight through. Everything at the WebAuthnProvider boundary
// speaks plain bytes instead, so this is the seam that bridges the two.
func jsonRequest(body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (p *Provider) BeginRegistration(rp server.RelyingParty, user server.WebAuthnUser) ([]byte, []byte, error) {
	w, err := webAuthnFor(rp)
	if err != nil {
		return nil, nil, fmt.Errorf("configuring relying party %q: %w", rp.ID, err)
	}
	creation, session, err := w.BeginRegistration(wuser{user})
	if err != nil {
		return nil, nil, err
	}
	optionsJSON, err := json.Marshal(creation)
	if err != nil {
		return nil, nil, err
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return nil, nil, err
	}
	return optionsJSON, sessionJSON, nil
}

func (p *Provider) FinishRegistration(rp server.RelyingParty, user server.WebAuthnUser, sessionJSON, response []byte) (store.WebAuthnCredential, error) {
	w, err := webAuthnFor(rp)
	if err != nil {
		return store.WebAuthnCredential{}, fmt.Errorf("configuring relying party %q: %w", rp.ID, err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(sessionJSON, &session); err != nil {
		return store.WebAuthnCredential{}, fmt.Errorf("decoding stored registration session: %w", err)
	}
	req, err := jsonRequest(response)
	if err != nil {
		return store.WebAuthnCredential{}, err
	}
	cred, err := w.FinishRegistration(wuser{user}, session, req)
	if err != nil {
		return store.WebAuthnCredential{}, err
	}
	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}
	return store.WebAuthnCredential{
		CredentialID: cred.ID,
		PublicKey:    cred.PublicKey,
		SignCount:    cred.Authenticator.SignCount,
		Transports:   transports,
	}, nil
}

func (p *Provider) BeginLogin(rp server.RelyingParty, user server.WebAuthnUser) ([]byte, []byte, error) {
	w, err := webAuthnFor(rp)
	if err != nil {
		return nil, nil, fmt.Errorf("configuring relying party %q: %w", rp.ID, err)
	}
	assertion, session, err := w.BeginLogin(wuser{user})
	if err != nil {
		return nil, nil, err
	}
	optionsJSON, err := json.Marshal(assertion)
	if err != nil {
		return nil, nil, err
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return nil, nil, err
	}
	return optionsJSON, sessionJSON, nil
}

func (p *Provider) FinishLogin(rp server.RelyingParty, user server.WebAuthnUser, sessionJSON, response []byte) ([]byte, uint32, error) {
	w, err := webAuthnFor(rp)
	if err != nil {
		return nil, 0, fmt.Errorf("configuring relying party %q: %w", rp.ID, err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(sessionJSON, &session); err != nil {
		return nil, 0, fmt.Errorf("decoding stored login session: %w", err)
	}
	req, err := jsonRequest(response)
	if err != nil {
		return nil, 0, err
	}
	cred, err := w.FinishLogin(wuser{user}, session, req)
	if err != nil {
		return nil, 0, err
	}
	return cred.ID, cred.Authenticator.SignCount, nil
}
