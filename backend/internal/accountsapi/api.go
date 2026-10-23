// Package accountsapi is the HTTP face of the accounts: sign-in and sign-out, two-factor authentication, passkeys, personal API tokens,
// organisations, members and invitations. It is a package of its own, and not part of accounts, because the HTTP kit stands on
// accounts (who is calling) and the accounts package stands on nothing of the server.
//
// What only the server knows reaches it as the Edge: the address a request came from, how the session cookie is set, the relying party a
// browser's address implies, the budget for failed sign-ins, and what ends when a person's standing changes.
package accountsapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"ikhnos/internal/accounts"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/kernel"
	"ikhnos/internal/store"
)

// API is the accounts' routes.
type API struct {
	// Accounts is the platform-wide accounts: sign-in, registration, a person's own settings. For is the accounts of the organisation a
	// request is for (the shell finds it from what its guard put in the request).
	Accounts *accounts.Service
	For      func(r *http.Request) *accounts.Service
	Edge     Edge
}

// Edge is what the shell supplies: the parts of a request and of the server's configuration the accounts' routes depend on and cannot know.
type Edge struct {
	// ClientIP is the address the request came from, as the server's proxy settings read it.
	ClientIP func(r *http.Request) string
	// SetCookie sets the session cookie (a negative maxAge clears it); SessionCookie is the secret a request carries in one.
	SetCookie     func(w http.ResponseWriter, r *http.Request, value string, maxAge int)
	SessionCookie func(r *http.Request) (string, bool)
	// RelyingParty is the WebAuthn relying party the address the browser has loaded implies.
	RelyingParty func(r *http.Request) accounts.RelyingParty
	// AllowFailure counts one more failed sign-in from the request's address and says whether it is still within the budget.
	AllowFailure func(r *http.Request) bool
	// EndPages ends every page session a person has opened (their sign-in or password changed). EndPagesIn does so when what changed was
	// their standing in the organisation the request is for, and only if that organisation is the one the pages belong to.
	// ClearPageCookie tells the browser to drop the page cookie.
	EndPages        func(userID string)
	EndPagesIn      func(r *http.Request, userID string)
	ClearPageCookie func(w http.ResponseWriter, r *http.Request)
	// Version is the server's version, and SSOHeader the name of the trusted header that turns single sign-on on (empty: off).
	Version   string
	SSOHeader string
}

func (h *API) fail(w http.ResponseWriter, err error) { httpkit.Fail(w, h.Accounts.Log, err) }

// Mount puts the routes on r.
func (h *API) Mount(r httpkit.Router) {
	// The server's mail settings are not an organisation's: the guard asks only that the person has settled in one, and the handlers
	// that they own the default organisation specifically (requireDefaultOwner).
	r.Handle("GET /api/v1/mail", httpkit.Settled, h.getMailConfig)
	r.Handle("PUT /api/v1/mail", httpkit.Settled, h.putMailConfig)
	r.Open("GET /api/v1/server", h.serverInfo)
	r.Open("POST /api/v1/auth/login", h.login)
	if h.Edge.SSOHeader != "" {
		r.Open("GET /api/v1/auth/sso", h.ssoLogin)
	}
	r.Open("POST /api/v1/auth/login/2fa", h.login2FA)
	r.Open("POST /api/v1/auth/login/2fa/email", h.requestLoginEmailCode)
	r.Open("POST /api/v1/auth/login/2fa/webauthn/begin", h.beginPasskeyLogin)
	r.Open("POST /api/v1/auth/login/2fa/webauthn/finish", h.finishPasskeyLogin)
	r.Open("POST /api/v1/auth/register", h.register)
	r.Open("POST /api/v1/auth/logout", h.logout)
	r.Open("POST /api/v1/invites/preview", h.previewInvite)
	r.Handle("GET /api/v1/auth/me", httpkit.AnySession, h.me)
	r.Handle("POST /api/v1/auth/password", httpkit.AnySession, h.changePassword)
	r.Handle("POST /api/v1/auth/2fa/setup", httpkit.AnySession, h.setup2FA)
	r.Handle("POST /api/v1/auth/2fa/enable", httpkit.AnySession, h.enable2FA)
	r.Handle("POST /api/v1/auth/2fa/disable", httpkit.AnySession, h.disable2FA)
	r.Handle("POST /api/v1/auth/email/request", httpkit.AnySession, h.requestEmailVerification)
	r.Handle("POST /api/v1/auth/email/confirm", httpkit.AnySession, h.confirmEmail)
	r.Handle("POST /api/v1/auth/email-otp/enable", httpkit.AnySession, h.enableEmailOTP)
	r.Handle("POST /api/v1/auth/email-otp/disable", httpkit.AnySession, h.disableEmailOTP)
	r.Handle("POST /api/v1/auth/webauthn/register/begin", httpkit.AnySession, h.beginPasskeyRegistration)
	r.Handle("POST /api/v1/auth/webauthn/register/finish", httpkit.AnySession, h.finishPasskeyRegistration)
	r.Handle("POST /api/v1/auth/webauthn/{id}/rename", httpkit.AnySession, h.renamePasskey)
	r.Handle("POST /api/v1/auth/webauthn/{id}/remove", httpkit.AnySession, h.removePasskey)
	r.Handle("GET /api/v1/auth/tokens", httpkit.AnySession, h.listAPITokens)
	r.Handle("POST /api/v1/auth/tokens", httpkit.AnySession, h.createAPIToken)
	r.Handle("POST /api/v1/auth/tokens/{id}/revoke", httpkit.AnySession, h.revokeAPIToken)
	r.Handle("GET /api/v1/orgs", httpkit.Settled, h.listOrgs)
	r.Handle("POST /api/v1/orgs", httpkit.Settled, h.createOrg)
	r.Handle("POST /api/v1/invites/accept", httpkit.Settled, h.acceptInvite)

	o := httpkit.OrgPath
	r.Handle("POST "+o+"/rename", httpkit.OwnerRole, h.renameOrg)
	r.Handle("POST "+o+"/delete", httpkit.OwnerRole, h.deleteOrg)
	r.Handle("POST "+o+"/leave", httpkit.MemberRole, h.leaveOrg)
	r.Handle("GET "+o+"/members", httpkit.MemberRole, h.listMembers)
	r.Handle("POST "+o+"/members/{id}/role", httpkit.AdminRole, h.setMemberRole)
	r.Handle("POST "+o+"/members/{id}/remove", httpkit.AdminRole, h.removeMember)
	r.Handle("GET "+o+"/invites", httpkit.AdminRole, h.listInvites)
	r.Handle("POST "+o+"/invites", httpkit.AdminRole, h.createInvite)
	r.Handle("DELETE "+o+"/invites/{id}", httpkit.AdminRole, h.revokeInvite)
}

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
	// CanManageMail is true only for an owner of the default organisation specifically (see
	// requireDefaultOwner) - the same eligibility /api/v1/mail itself enforces, exposed here so Settings
	// knows whether to show that section at all rather than showing it and letting the request 403.
	CanManageMail bool `json:"canManageMail"`
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

type orgDoc struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	Role accounts.Role `json:"role"`
}

// apiTokenDoc never carries a secret or hash - only what settings needs to show a person their own
// tokens and let them tell one apart from another before revoking it.
type apiTokenDoc struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
}

type memberDoc struct {
	ID       string        `json:"id"`
	Username string        `json:"username"`
	Role     accounts.Role `json:"role"`
	JoinedAt string        `json:"joinedAt"`
	LastSeen string        `json:"lastLogin,omitempty"`
	You      bool          `json:"you,omitempty"`
}

type inviteDoc struct {
	ID        string        `json:"id"`
	Role      accounts.Role `json:"role"`
	Label     string        `json:"label,omitempty"`
	CreatedBy string        `json:"createdBy"`
	CreatedAt string        `json:"createdAt"`
	ExpiresAt string        `json:"expiresAt"`
	Used      bool          `json:"used"`
	UsedBy    string        `json:"usedBy,omitempty"`
	Expired   bool          `json:"expired,omitempty"`
}

// writeOptions hands back a WebAuthn options object exactly as the provider built it - already valid JSON,
// so unlike writeJSON this never re-encodes it.
func writeOptions(w http.ResponseWriter, options []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(options)
}

func toUserDoc(u store.User, mailConfigured, canManageMail bool) userDoc {
	passkeys := make([]passkeyDoc, len(u.WebAuthnCredentials))
	for i, cr := range u.WebAuthnCredentials {
		passkeys[i] = passkeyDoc{ID: base64.RawURLEncoding.EncodeToString(cr.CredentialID), Name: cr.Name, CreatedAt: kernel.RFC(cr.CreatedAt), LastUsedAt: kernel.RFCP(cr.LastUsedAt)}
	}
	return userDoc{
		ID: u.ID, Username: u.Username, MustChangePassword: u.MustChange, CreatedAt: kernel.RFC(u.CreatedAt), LastLogin: kernel.RFCP(u.LastLogin),
		TwoFactorEnabled: u.TOTPEnabledAt != nil,
		Email:            u.Email, EmailVerified: u.EmailVerifiedAt != nil, EmailOTPEnabled: u.EmailOTPEnabledAt != nil,
		MailConfigured: mailConfigured,
		CanManageMail:  canManageMail,
		Passkeys:       passkeys,
	}
}

func toAPITokenDoc(t store.APIToken) apiTokenDoc {
	return apiTokenDoc{ID: t.ID, Name: t.Name, CreatedAt: kernel.RFC(t.CreatedAt), LastUsedAt: kernel.RFCP(t.LastUsed)}
}

func (h *API) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req loginRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	secret, u, err := h.Accounts.Login(r.Context(), h.Edge.ClientIP(r), req.Username, req.Password)
	if err != nil {
		// A password that checks out but needs a TOTP code next comes back as an ordinary error (a
		// KindTwoFactorRequired one, its "pending" token merged into this same JSON body by a.fail) rather
		// than a special-cased response: the UI tells it apart from a wrong password by that field.
		h.fail(w, err)
		return
	}
	h.Edge.SetCookie(w, r, secret, int(accounts.SessionMax.Seconds()))
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// login2FA finishes a sign-in login left pending on a two-factor code.
func (h *API) login2FA(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req login2FARequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	secret, u, err := h.Accounts.Login2FA(r.Context(), h.Edge.ClientIP(r), req.Pending, req.Code)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.Edge.SetCookie(w, r, secret, int(accounts.SessionMax.Seconds()))
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// ssoLogin trades a trusted proxy's identity header for a session - see SSOHeaderName's doc comment for
// what "trusted" has to mean here. Registered only when SSOHeaderName is set, so the endpoint does not
// even exist on a server that never configured it.
func (h *API) ssoLogin(w http.ResponseWriter, r *http.Request) {
	secret, u, err := h.Accounts.LoginSSO(r.Context(), h.Edge.ClientIP(r), r.Header.Get(h.Edge.SSOHeader))
	if err != nil {
		if !h.Edge.AllowFailure(r) {
			httpkit.WriteErr(w, http.StatusTooManyRequests, "too many failed attempts, wait a minute")
			return
		}
		h.fail(w, err)
		return
	}
	h.Edge.SetCookie(w, r, secret, int(accounts.SessionMax.Seconds()))
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

func (h *API) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req registerRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	secret, u, err := h.Accounts.Register(r.Context(), h.Edge.ClientIP(r), req.Username, req.Password, strings.TrimSpace(req.Invite), req.Org)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.Edge.SetCookie(w, r, secret, int(accounts.SessionMax.Seconds()))
	httpkit.WriteJSON(w, 201, h.session(r.Context(), u))
}

func (h *API) logout(w http.ResponseWriter, r *http.Request) {
	if secret, ok := h.Edge.SessionCookie(r); ok {
		if p, err := h.Accounts.Authenticate(r.Context(), secret); err == nil {
			h.Accounts.Logout(r.Context(), p)
			h.Edge.EndPages(p.User.ID) // the pages opened in other tabs end with the sign-in that opened them
		}
	}
	h.Edge.SetCookie(w, r, "", -1)
	h.Edge.ClearPageCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) me(w http.ResponseWriter, r *http.Request) {
	httpkit.WriteJSON(w, 200, h.session(r.Context(), httpkit.Principal(r).User))
}

func (h *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.ChangePassword(r.Context(), httpkit.Principal(r), req.Current, req.New); err != nil {
		h.fail(w, err)
		return
	}
	// Every other browser is signed out by a new password (ChangePassword); the pages they opened go with them.
	h.Edge.EndPages(httpkit.Principal(r).User.ID)
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// setup2FA starts turning two-factor authentication on: it returns the fresh secret and the otpauth://
// URI for it, shown as text and a copyable key rather than a QR code (every authenticator app also
// accepts typing a secret in by hand).
func (h *API) setup2FA(w http.ResponseWriter, r *http.Request) {
	secret, uri, err := h.Accounts.Setup2FA(r.Context(), httpkit.Principal(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, twoFactorSetupDoc{OtpauthURL: uri, Secret: secret})
}

// enable2FA confirms a setup with one code from it, turns two-factor authentication on, and hands back
// this account's recovery codes - shown once, since only their hashes are kept from here on.
func (h *API) enable2FA(w http.ResponseWriter, r *http.Request) {
	var req enable2FARequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	codes, err := h.Accounts.Enable2FA(r.Context(), httpkit.Principal(r), req.Code)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, recoveryCodesDoc{RecoveryCodes: codes})
}

func (h *API) disable2FA(w http.ResponseWriter, r *http.Request) {
	var req disable2FARequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.Disable2FA(r.Context(), httpkit.Principal(r), req.Password); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// requestLoginEmailCode is called from the pending-2FA screen when a person picks "email me a code"
// instead of typing one from an authenticator app; login2FA then accepts the mailed code back exactly like
// a TOTP one. No session exists yet at this point, same as login and login2FA themselves.
func (h *API) requestLoginEmailCode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req requestLoginEmailCodeRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.RequestLoginEmailCode(r.Context(), h.Edge.ClientIP(r), req.Pending); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestEmailVerification starts confirming a new email address on the signed-in account.
func (h *API) requestEmailVerification(w http.ResponseWriter, r *http.Request) {
	var req requestEmailVerificationRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.RequestEmailVerification(r.Context(), httpkit.Principal(r), req.Email); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// confirmEmail finishes requestEmailVerification with the code that was mailed.
func (h *API) confirmEmail(w http.ResponseWriter, r *http.Request) {
	var req confirmEmailRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.ConfirmEmail(r.Context(), httpkit.Principal(r), req.Code); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// enableEmailOTP turns on email as a second sign-in factor for the already-verified address on the account.
func (h *API) enableEmailOTP(w http.ResponseWriter, r *http.Request) {
	if err := h.Accounts.EnableEmailOTP(r.Context(), httpkit.Principal(r)); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

func (h *API) disableEmailOTP(w http.ResponseWriter, r *http.Request) {
	var req disableEmailOTPRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.DisableEmailOTP(r.Context(), httpkit.Principal(r), req.Password); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// beginPasskeyRegistration starts registering a new passkey or security key on the signed-in account. The
// response is the CredentialCreationOptions object navigator.credentials.create() takes directly.
func (h *API) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	options, err := h.Accounts.BeginPasskeyRegistration(r.Context(), httpkit.Principal(r), h.Edge.RelyingParty(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeOptions(w, options)
}

// finishPasskeyRegistration completes a registration beginPasskeyRegistration started.
func (h *API) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	var req finishPasskeyRegistrationRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.FinishPasskeyRegistration(r.Context(), httpkit.Principal(r), h.Edge.RelyingParty(r), req.Name, req.Response); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

// beginPasskeyLogin starts the passkey half of a pending two-factor sign-in. No session exists yet, same as
// login and login2FA.
func (h *API) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req beginPasskeyLoginRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	options, err := h.Accounts.BeginPasskeyLogin(r.Context(), h.Edge.ClientIP(r), req.Pending, h.Edge.RelyingParty(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeOptions(w, options)
}

// finishPasskeyLogin completes a passkey sign-in and, unlike login2FA, opens the session itself: a passkey's
// response is a signed assertion object, not a short code that fits login2FA's single field.
func (h *API) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	// Unlike finishPasskeyRegistration this has no session yet for guard() to bound the body of, so it needs
	// its own limit the same way its sibling beginPasskeyLogin, login and login2FA do - otherwise an
	// unauthenticated caller could hand this a response of any size before WebAuthn verification ever runs.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req finishPasskeyLoginRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	secret, u, err := h.Accounts.FinishPasskeyLogin(r.Context(), h.Edge.ClientIP(r), req.Pending, h.Edge.RelyingParty(r), req.Response)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.Edge.SetCookie(w, r, secret, int(accounts.SessionMax.Seconds()))
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

func (h *API) renamePasskey(w http.ResponseWriter, r *http.Request) {
	var req renamePasskeyRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.RenamePasskey(r.Context(), httpkit.Principal(r), r.PathValue("id"), req.Name); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

func (h *API) removePasskey(w http.ResponseWriter, r *http.Request) {
	var req removePasskeyRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Accounts.RemovePasskey(r.Context(), httpkit.Principal(r), r.PathValue("id"), req.Password); err != nil {
		h.fail(w, err)
		return
	}
	u, _ := h.Accounts.Store.GetUser(r.Context(), httpkit.Principal(r).User.ID)
	httpkit.WriteJSON(w, 200, h.session(r.Context(), u))
}

func (h *API) listAPITokens(w http.ResponseWriter, r *http.Request) {
	toks, err := h.Accounts.ListAPITokens(r.Context(), httpkit.Principal(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]apiTokenDoc, len(toks))
	for i, t := range toks {
		out[i] = toAPITokenDoc(t)
	}
	httpkit.WriteJSON(w, 200, out)
}

// createAPIToken is the one response that ever carries the raw secret: the caller must save it now,
// exactly like a freshly created invitation link.
func (h *API) createAPIToken(w http.ResponseWriter, r *http.Request) {
	// A token that could mint more tokens would let one stolen secret outlive its own revocation.
	if _, ok := h.Edge.SessionCookie(r); !ok {
		httpkit.WriteErr(w, http.StatusForbidden, "create a token from a signed-in browser session, not with another token")
		return
	}
	var req createAPITokenRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	secret, tok, err := h.Accounts.CreateAPIToken(r.Context(), httpkit.Principal(r), req.Name)
	if err != nil {
		h.fail(w, err)
		return
	}
	doc := toAPITokenDoc(tok)
	httpkit.WriteJSON(w, 200, createdAPITokenDoc{CreatedAt: doc.CreatedAt, ID: doc.ID, Name: doc.Name, Token: secret})
}

func (h *API) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if err := h.Accounts.RevokeAPIToken(r.Context(), httpkit.Principal(r), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, httpkit.OKDoc{OK: true})
}

func (h *API) listOrgs(w http.ResponseWriter, r *http.Request) {
	httpkit.WriteJSON(w, 200, h.session(r.Context(), httpkit.Principal(r).User).Orgs)
}

func (h *API) createOrg(w http.ResponseWriter, r *http.Request) {
	var req createOrgRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	o, err := h.Accounts.CreateOrg(r.Context(), httpkit.Principal(r), req.Name)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 201, orgDoc{ID: o.ID, Name: o.Name, Role: accounts.RoleOwner})
}

func (h *API) previewInvite(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req previewInviteRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	inv, name, err := h.Accounts.PreviewInvite(r.Context(), h.Edge.ClientIP(r), strings.TrimSpace(req.Token))
	if err != nil {
		h.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, invitePreviewDoc{ExpiresAt: kernel.RFC(inv.ExpiresAt), Organisation: name, Role: accounts.Role(inv.Role)})
}

func (h *API) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptInviteRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	o, role, err := h.Accounts.AcceptInvite(r.Context(), h.Edge.ClientIP(r), httpkit.Principal(r), strings.TrimSpace(req.Token))
	if err != nil {
		h.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, orgDoc{ID: o.ID, Name: o.Name, Role: accounts.Role(role)})
}

func (h *API) renameOrg(w http.ResponseWriter, r *http.Request) {
	var req renameOrgRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.For(r).RenameOrg(r.Context(), httpkit.Principal(r), req.Name); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) deleteOrg(w http.ResponseWriter, r *http.Request) {
	var req deleteOrgRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.For(r).DeleteOrg(r.Context(), httpkit.Principal(r), req.Confirm); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) leaveOrg(w http.ResponseWriter, r *http.Request) {
	p := httpkit.Principal(r)
	if err := h.For(r).RemoveMember(r.Context(), p, p.User.ID); err != nil {
		h.fail(w, err)
		return
	}
	h.Edge.EndPagesIn(r, p.User.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := h.Accounts.Store.ListMembers(r.Context(), h.For(r).OrgID)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []memberDoc{}
	for _, m := range ms {
		out = append(out, memberDoc{ID: m.User.ID, Username: m.Username, Role: accounts.Role(m.Role), JoinedAt: kernel.RFC(m.JoinedAt), LastSeen: kernel.RFCP(m.LastLogin), You: m.User.ID == httpkit.Principal(r).User.ID})
	}
	httpkit.WriteJSON(w, 200, out)
}

func (h *API) setMemberRole(w http.ResponseWriter, r *http.Request) {
	var req setMemberRoleRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.For(r).SetMemberRole(r.Context(), httpkit.Principal(r), r.PathValue("id"), string(req.Role)); err != nil {
		h.fail(w, err)
		return
	}
	if !accounts.Allows(accounts.Standing(string(req.Role)), accounts.ActAdminister) { // a downgrade below what the pages need; a promotion changes nothing for them
		h.Edge.EndPagesIn(r, r.PathValue("id"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) removeMember(w http.ResponseWriter, r *http.Request) {
	if err := h.For(r).RemoveMember(r.Context(), httpkit.Principal(r), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	h.Edge.EndPagesIn(r, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) listInvites(w http.ResponseWriter, r *http.Request) {
	is, err := h.Accounts.Store.ListInvites(r.Context(), h.For(r).OrgID)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []inviteDoc{}
	for _, i := range is {
		out = append(out, h.inviteDoc(r.Context(), i))
	}
	httpkit.WriteJSON(w, 200, out)
}

func (h *API) createInvite(w http.ResponseWriter, r *http.Request) {
	var req createInviteRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	secret, inv, err := h.For(r).CreateInvite(r.Context(), httpkit.Principal(r), string(req.Role), req.Label)
	if err != nil {
		h.fail(w, err)
		return
	}
	// The secret appears here once and is never retrievable again.
	httpkit.WriteJSON(w, 201, createdInviteDoc{Invite: h.inviteDoc(r.Context(), inv), Token: secret})
}

func (h *API) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := h.For(r).RevokeInvite(r.Context(), httpkit.Principal(r), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) inviteDoc(ctx context.Context, i store.Invite) inviteDoc {
	d := inviteDoc{ID: i.ID, Role: accounts.Role(i.Role), Label: i.Label, CreatedBy: h.Accounts.NameOf(ctx, i.CreatedBy), CreatedAt: kernel.RFC(i.CreatedAt), ExpiresAt: kernel.RFC(i.ExpiresAt), Used: i.UsedAt != nil, Expired: i.UsedAt == nil && h.Accounts.Now().After(i.ExpiresAt)}
	if i.UsedBy != "" {
		d.UsedBy = h.Accounts.NameOf(ctx, i.UsedBy)
	}
	return d
}

// session is the answer to sign-in, sign-up and "who am I": the person and every organisation they
// belong to, so the UI needs no second call to know where it may go.
func (h *API) session(ctx context.Context, u store.User) sessionDoc {
	orgs := []orgDoc{}
	canManageMail := false
	if mine, err := h.Accounts.Store.ListMyOrgs(ctx, u.ID); err == nil {
		for _, o := range mine {
			orgs = append(orgs, orgDoc{ID: o.ID, Name: o.Name, Role: accounts.Role(o.Role)})
			if o.ID == h.Accounts.DefaultOrg && accounts.Allows(accounts.Standing(o.Role), accounts.ActOwn) {
				canManageMail = true
			}
		}
	}
	return sessionDoc{User: toUserDoc(u, h.Accounts.Mailer().Enabled(), canManageMail), Orgs: orgs}
}

// serverInfo is public: what the sign-in and sign-up pages need before anyone is signed in.
func (h *API) serverInfo(w http.ResponseWriter, r *http.Request) {
	mode := h.Accounts.EffectiveRegMode()
	httpkit.WriteJSON(w, 200, serverInfoDoc{Registration: accounts.Registration(mode), SSO: h.Edge.SSOHeader != "", Version: h.Edge.Version})
}
