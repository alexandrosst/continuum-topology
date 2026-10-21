package accountsapi

import (
	"encoding/json"

	"continuum/internal/accounts"
)

// The bodies the routes read, one type to a route, so that the page can be given the shape it must send (internal/wire).

// loginRequest is the body of login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// login2FARequest is the body of login2FA.
type login2FARequest struct {
	Pending string `json:"pending"`
	Code    string `json:"code"`
}

// registerRequest is the body of register.
type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Invite   string `json:"invite"`
	Org      string `json:"org"`
}

// changePasswordRequest is the body of changePassword.
type changePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// enable2FARequest is the body of enable2FA.
type enable2FARequest struct {
	Code string `json:"code"`
}

// disable2FARequest is the body of disable2FA.
type disable2FARequest struct {
	Password string `json:"password"`
}

// requestLoginEmailCodeRequest is the body of requestLoginEmailCode.
type requestLoginEmailCodeRequest struct {
	Pending string `json:"pending"`
}

// requestEmailVerificationRequest is the body of requestEmailVerification.
type requestEmailVerificationRequest struct {
	Email string `json:"email"`
}

// confirmEmailRequest is the body of confirmEmail.
type confirmEmailRequest struct {
	Code string `json:"code"`
}

// disableEmailOTPRequest is the body of disableEmailOTP.
type disableEmailOTPRequest struct {
	Password string `json:"password"`
}

// finishPasskeyRegistrationRequest is the body of finishPasskeyRegistration.
type finishPasskeyRegistrationRequest struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// beginPasskeyLoginRequest is the body of beginPasskeyLogin.
type beginPasskeyLoginRequest struct {
	Pending string `json:"pending"`
}

// finishPasskeyLoginRequest is the body of finishPasskeyLogin.
type finishPasskeyLoginRequest struct {
	Pending  string          `json:"pending"`
	Response json.RawMessage `json:"response"`
}

// renamePasskeyRequest is the body of renamePasskey.
type renamePasskeyRequest struct {
	Name string `json:"name"`
}

// removePasskeyRequest is the body of removePasskey.
type removePasskeyRequest struct {
	Password string `json:"password"`
}

// createAPITokenRequest is the body of createAPIToken.
type createAPITokenRequest struct {
	Name string `json:"name"`
}

// createOrgRequest is the body of createOrg.
type createOrgRequest struct {
	Name string `json:"name"`
}

// previewInviteRequest is the body of previewInvite.
type previewInviteRequest struct {
	Token string `json:"token"`
}

// acceptInviteRequest is the body of acceptInvite.
type acceptInviteRequest struct {
	Token string `json:"token"`
}

// renameOrgRequest is the body of renameOrg.
type renameOrgRequest struct {
	Name string `json:"name"`
}

// deleteOrgRequest is the body of deleteOrg.
type deleteOrgRequest struct {
	Confirm string `json:"confirm"`
}

// setMemberRoleRequest is the body of setMemberRole.
type setMemberRoleRequest struct {
	Role string `json:"role"`
}

// createInviteRequest is the body of createInvite.
type createInviteRequest struct {
	Role  string `json:"role"`
	Label string `json:"label"`
}

// putMailConfigRequest is the body of putMailConfig.
type putMailConfigRequest struct {
	accounts.MailConfig
	ClearPassword bool `json:"clearPassword"`
}
