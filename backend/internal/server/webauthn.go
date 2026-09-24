package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"continuum/internal/store"
)

// webauthnCeremonyTTL bounds how long a passkey registration or sign-in challenge stays valid: long enough
// to get through the OS/browser prompt, short enough that an abandoned one does not sit around as a
// replayable challenge.
const webauthnCeremonyTTL = 5 * time.Minute

// maxPasskeyName bounds a person's own label for a credential ("MacBook Touch ID").
const maxPasskeyName = 60

// RelyingParty is who a WebAuthn ceremony proves a credential to. It is computed fresh from each request's
// own Host header (see Admin.relyingParty), since this server has no single fixed public domain the way a
// hosted SaaS would - whatever address the browser actually has loaded is what ID and Origin must say back,
// or the browser refuses the ceremony outright as a mismatch.
type RelyingParty struct {
	ID     string // the domain alone, no scheme or port (e.g. "continuum.example.com", or "localhost")
	Origin string // the full origin the browser sees (e.g. "https://continuum.example.com")
	Name   string // shown in the OS/browser's own passkey picker
}

// WebAuthnUser is what a WebAuthn ceremony needs to know about an account.
type WebAuthnUser struct {
	ID          string
	Username    string
	Credentials []store.WebAuthnCredential
}

func webauthnUser(u store.User) WebAuthnUser {
	return WebAuthnUser{ID: u.ID, Username: u.Username, Credentials: u.WebAuthnCredentials}
}

// WebAuthnProvider is the one seam between this package - self-contained, stdlib-only, and fully tested
// here with a fake - and the actual WebAuthn/passkey protocol, which needs github.com/go-webauthn/webauthn:
// a dependency this sandbox has no network path to fetch (see internal/passkey, built and verified on a
// real machine instead - README there explains why). Everything at this boundary speaks plain JSON, exactly
// the bytes a browser's navigator.credentials create()/get() sends and expects, so Core and its tests never
// need the real library: tests use a fake that speaks the same shapes.
//
// FinishRegistration and FinishLogin carry every check that makes WebAuthn actually secure - the challenge
// matches, the origin and RP ID match, the signature verifies against the stored public key, and (for
// FinishLogin) the authenticator's own signature counter advanced - and must return an error rather than a
// result a caller could mistake for success if any of that fails.
type WebAuthnProvider interface {
	// BeginRegistration returns the CredentialCreationOptions JSON to hand the browser, and an opaque
	// session blob FinishRegistration needs back unchanged (kept server-side only; never sent to the browser).
	BeginRegistration(rp RelyingParty, user WebAuthnUser) (optionsJSON, session []byte, err error)
	// FinishRegistration verifies the browser's response against session and returns the credential to store.
	// CreatedAt and Name are filled in by the caller, not this method.
	FinishRegistration(rp RelyingParty, user WebAuthnUser, session, response []byte) (store.WebAuthnCredential, error)
	// BeginLogin returns the CredentialRequestOptions JSON to hand the browser and an opaque session blob.
	BeginLogin(rp RelyingParty, user WebAuthnUser) (optionsJSON, session []byte, err error)
	// FinishLogin verifies the browser's response and returns which of user.Credentials answered (by its
	// CredentialID) and its new signature counter, safe for the caller to store as-is.
	FinishLogin(rp RelyingParty, user WebAuthnUser, session, response []byte) (credentialID []byte, signCount uint32, err error)
}

// errNoPasskeys is what every passkey endpoint returns when the server has no provider wired in - not
// expected outside a build or test that deliberately left Core.WebAuthn nil.
func errNoPasskeys() *Error { return errf(KindConflict, "passkeys are not available on this server") }

// validRelyingPartyID reports whether id can be a WebAuthn RP ID at all: the spec requires a registrable
// domain suffix (or exactly "localhost"), never a bare IP literal - a browser refuses the ceremony outright
// otherwise. Checked here, with a clear message, rather than left to fail mysteriously inside the browser.
func validRelyingPartyID(id string) bool {
	_, err := netip.ParseAddr(id)
	return err != nil // parses as an IP -> invalid RP ID; anything else (a real hostname) -> fine
}

func (c *Core) putWebAuthnRegSession(userID string, session []byte) {
	a := c.auth
	now := c.Now()
	a.webauthnRegMu.Lock()
	defer a.webauthnRegMu.Unlock()
	if a.webauthnReg == nil {
		a.webauthnReg = map[string]webauthnSession{}
	}
	for k, v := range a.webauthnReg {
		if now.After(v.expiresAt) {
			delete(a.webauthnReg, k)
		}
	}
	a.webauthnReg[userID] = webauthnSession{data: session, expiresAt: now.Add(webauthnCeremonyTTL)}
}

// takeWebAuthnRegSession removes and returns the session, whether or not it was found or still valid: a
// registration challenge is single-use, so a failed Finish must not be retryable against the same one.
func (c *Core) takeWebAuthnRegSession(userID string) ([]byte, bool) {
	a := c.auth
	a.webauthnRegMu.Lock()
	defer a.webauthnRegMu.Unlock()
	s, ok := a.webauthnReg[userID]
	delete(a.webauthnReg, userID)
	if !ok || c.Now().After(s.expiresAt) {
		return nil, false
	}
	return s.data, true
}

func (c *Core) putWebAuthnLoginSession(pending string, session []byte) {
	a := c.auth
	now := c.Now()
	a.webauthnLoginMu.Lock()
	defer a.webauthnLoginMu.Unlock()
	if a.webauthnLogin == nil {
		a.webauthnLogin = map[string]webauthnSession{}
	}
	for k, v := range a.webauthnLogin {
		if now.After(v.expiresAt) {
			delete(a.webauthnLogin, k)
		}
	}
	a.webauthnLogin[pending] = webauthnSession{data: session, expiresAt: now.Add(webauthnCeremonyTTL)}
}

func (c *Core) takeWebAuthnLoginSession(pending string) ([]byte, bool) {
	a := c.auth
	a.webauthnLoginMu.Lock()
	defer a.webauthnLoginMu.Unlock()
	s, ok := a.webauthnLogin[pending]
	delete(a.webauthnLogin, pending)
	if !ok || c.Now().After(s.expiresAt) {
		return nil, false
	}
	return s.data, true
}

// BeginPasskeyRegistration starts registering a new passkey or security key on the signed-in account. The
// options it returns go straight to the browser's navigator.credentials.create().
func (c *Core) BeginPasskeyRegistration(ctx context.Context, p Principal, rp RelyingParty) ([]byte, error) {
	if c.WebAuthn == nil {
		return nil, errNoPasskeys()
	}
	if !validRelyingPartyID(rp.ID) {
		return nil, errf(KindConflict, "passkeys need this server to be reached by a domain name, not a bare IP address (%s)", rp.ID)
	}
	if !c.auth.loginUser.Allow("2fa-setup|" + p.User.ID) {
		return nil, errf(KindRateLimited, "too many attempts, wait a minute")
	}
	u, err := c.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		return nil, errf(KindUnauthenticated, "sign in required")
	}
	options, session, err := c.WebAuthn.BeginRegistration(rp, webauthnUser(u))
	if err != nil {
		return nil, errf(KindInvalid, "could not start passkey registration: %v", err)
	}
	c.putWebAuthnRegSession(u.ID, session)
	return options, nil
}

// FinishPasskeyRegistration completes a registration BeginPasskeyRegistration started: response is the
// browser's navigator.credentials.create() result, JSON-encoded exactly as it serialises. name is what the
// person calls this credential in settings; left blank, a generic default is used instead of failing an
// otherwise-successful registration over a missing label.
func (c *Core) FinishPasskeyRegistration(ctx context.Context, p Principal, rp RelyingParty, name string, response []byte) error {
	if c.WebAuthn == nil {
		return errNoPasskeys()
	}
	name = strings.TrimSpace(name)
	if l := len([]rune(name)); l > maxPasskeyName {
		return errf(KindInvalid, "the name is at most %d characters", maxPasskeyName)
	}
	u, err := c.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		return errf(KindUnauthenticated, "sign in required")
	}
	if name == "" {
		name = fmt.Sprintf("Passkey %d", len(u.WebAuthnCredentials)+1)
	}
	// Taken (and thereby spent) only now that the request has cleared every check that does not depend on
	// the browser's response, so a bad name can be corrected and resubmitted against the same challenge
	// instead of forcing the whole create() ceremony to run again.
	session, ok := c.takeWebAuthnRegSession(p.User.ID)
	if !ok {
		return errf(KindConflict, "that registration has expired; start again")
	}
	cred, err := c.WebAuthn.FinishRegistration(rp, webauthnUser(u), session, response)
	if err != nil {
		return errf(KindInvalid, "that passkey could not be registered: %v", err)
	}
	cred.Name, cred.CreatedAt = name, c.Now()
	if err := c.Store.AddWebAuthnCredential(ctx, u.ID, cred); err != nil {
		if errors.Is(err, store.ErrExists) {
			return errf(KindConflict, "that passkey is already registered to this account")
		}
		return err
	}
	c.auditUser(ctx, u, "passkey-added", name, "")
	return nil
}

// BeginPasskeyLogin starts the passkey half of a pending two-factor sign-in (see Login/twoFactorMethods):
// pending is the token Login returned, and must have "webauthn" among its methods.
func (c *Core) BeginPasskeyLogin(ctx context.Context, ip, pending string, rp RelyingParty) ([]byte, error) {
	if c.WebAuthn == nil {
		return nil, errNoPasskeys()
	}
	if !validRelyingPartyID(rp.ID) {
		return nil, errf(KindConflict, "passkeys need this server to be reached by a domain name, not a bare IP address (%s)", rp.ID)
	}
	if !c.auth.loginIP.Allow(LimitKey(ip)) {
		return nil, errf(KindRateLimited, "too many sign-in attempts, wait a minute")
	}
	pl, ok := c.peekPendingLogin(pending)
	if !ok || !slices.Contains(pl.methods, "webauthn") {
		return nil, errf(KindUnauthenticated, "that sign-in has expired; enter your password again")
	}
	u, err := c.Store.GetUser(ctx, pl.userID)
	if err != nil || len(u.WebAuthnCredentials) == 0 {
		return nil, errf(KindUnauthenticated, "that sign-in has expired; enter your password again")
	}
	options, session, err := c.WebAuthn.BeginLogin(rp, webauthnUser(u))
	if err != nil {
		return nil, errf(KindInvalid, "could not start passkey sign-in: %v", err)
	}
	c.putWebAuthnLoginSession(pending, session)
	return options, nil
}

// FinishPasskeyLogin completes a passkey sign-in BeginPasskeyLogin started, opening a session directly - a
// passkey's response is a signed assertion, not a short typed code, so unlike an authenticator app or an
// emailed code it cannot be fed through Login2FA's single code field.
func (c *Core) FinishPasskeyLogin(ctx context.Context, ip, pending string, rp RelyingParty, response []byte) (secret string, u store.User, err error) {
	if c.WebAuthn == nil {
		return "", store.User{}, errNoPasskeys()
	}
	key := LimitKey(ip)
	if !c.auth.loginIP.Allow(key) {
		return "", store.User{}, errf(KindRateLimited, "too many sign-in attempts, wait a minute")
	}
	pl, ok := c.peekPendingLogin(pending)
	if !ok || !slices.Contains(pl.methods, "webauthn") {
		return "", store.User{}, errf(KindUnauthenticated, "that sign-in has expired; enter your password again")
	}
	u, gerr := c.Store.GetUser(ctx, pl.userID)
	if gerr != nil || u.DisabledAt != nil || len(u.WebAuthnCredentials) == 0 {
		return "", store.User{}, errf(KindUnauthenticated, "that sign-in has expired; enter your password again")
	}
	if !c.auth.loginUser.Allow(key + "|" + u.Username) {
		return "", store.User{}, errf(KindRateLimited, "too many sign-in attempts, wait a minute")
	}
	if wait := c.auth.failures.blocked(u.Username, c.Now()); wait > 0 {
		return "", store.User{}, errf(KindRateLimited, "too many failed sign-in attempts for this account; try again in %s", roundWait(wait))
	}
	session, ok := c.takeWebAuthnLoginSession(pending)
	if !ok {
		return "", store.User{}, errf(KindUnauthenticated, "that sign-in has expired; enter your password again")
	}
	credID, signCount, verr := c.WebAuthn.FinishLogin(rp, webauthnUser(u), session, response)
	if verr != nil {
		c.auth.failures.fail(u.Username, c.Now())
		Metrics.authFailures.Add(1)
		c.auditOrg(ctx, "", "anonymous", "login-failed", "user", printable(u.Username, 64), "wrong passkey from "+ip)
		return "", store.User{}, errf(KindUnauthenticated, "that passkey could not be verified")
	}
	c.auth.failures.succeed(u.Username)
	c.consumePendingLogin(pending)
	if err := c.Store.TouchWebAuthnCredential(ctx, u.ID, credID, signCount, c.Now()); err != nil {
		return "", store.User{}, err
	}
	secret, err = c.openSession(ctx, u, ip)
	if err != nil {
		return "", store.User{}, err
	}
	c.auditUser(ctx, u, "login", "with a passkey", ip)
	return secret, u, nil
}

// RenamePasskey changes only a credential's own label.
func (c *Core) RenamePasskey(ctx context.Context, p Principal, credentialIDB64, name string) error {
	if !c.auth.loginUser.Allow("2fa-setup|" + p.User.ID) {
		return errf(KindRateLimited, "too many attempts, wait a minute")
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxPasskeyName {
		return errf(KindInvalid, "name it 1-%d characters", maxPasskeyName)
	}
	credID, err := base64.RawURLEncoding.DecodeString(credentialIDB64)
	if err != nil {
		return errf(KindInvalid, "not a valid passkey id")
	}
	if err := c.Store.RenameWebAuthnCredential(ctx, p.User.ID, credID, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errf(KindNotFound, "no such passkey")
		}
		return err
	}
	return nil
}

// RemovePasskey deletes one credential. Gated on the current password, the same as Disable2FA and
// DisableEmailOTP: removing what may be an account's only second factor deserves the same proof of presence
// as changing what it protects - a passkey has no separate "turn it off but keep it" state the way email does.
func (c *Core) RemovePasskey(ctx context.Context, p Principal, credentialIDB64, password string) error {
	if !c.auth.loginUser.Allow("2fa-setup|" + p.User.ID) {
		return errf(KindRateLimited, "too many attempts, wait a minute")
	}
	u, err := c.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		return errf(KindUnauthenticated, "sign in required")
	}
	ok, verr := c.verifyPassword(ctx, password, u.PasswordHash)
	if verr != nil || !ok {
		return errf(KindInvalid, "your current password is not correct")
	}
	credID, err := base64.RawURLEncoding.DecodeString(credentialIDB64)
	if err != nil {
		return errf(KindInvalid, "not a valid passkey id")
	}
	if err := c.Store.RemoveWebAuthnCredential(ctx, u.ID, credID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errf(KindNotFound, "no such passkey")
		}
		return err
	}
	c.auditUser(ctx, u, "passkey-removed", "", "")
	return nil
}
