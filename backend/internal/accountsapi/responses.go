package accountsapi

import "continuum/internal/accounts"

// The answers the routes write that are not one of the documents above. Each is a type of its own, with its fields in the order of
// their JSON names, the order an encoded map had, so that what is on the wire is byte for byte what it was.

// sessionDoc is the answer to sign-in, sign-up and "who am I".
type sessionDoc struct {
	Orgs []orgDoc `json:"orgs"`
	User userDoc  `json:"user"`
}

// twoFactorSetupDoc is a fresh two-factor secret and the otpauth:// URI for it.
type twoFactorSetupDoc struct {
	OtpauthURL string `json:"otpauthUrl"`
	Secret     string `json:"secret"`
}

// recoveryCodesDoc is the recovery codes of an account, shown once.
type recoveryCodesDoc struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

// createdAPITokenDoc is a new API token: the one answer that carries its secret.
type createdAPITokenDoc struct {
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
}

// invitePreviewDoc is what an invitation is for, shown before it is accepted.
type invitePreviewDoc struct {
	ExpiresAt    string        `json:"expiresAt"`
	Organisation string        `json:"organisation"`
	Role         accounts.Role `json:"role"`
}

// createdInviteDoc is a new invitation: the one answer that carries its secret.
type createdInviteDoc struct {
	Invite inviteDoc `json:"invite"`
	Token  string    `json:"token"`
}

// serverInfoDoc is what the sign-in and sign-up pages need before anyone is signed in.
type serverInfoDoc struct {
	Registration accounts.Registration `json:"registration"`
	SSO          bool                  `json:"sso"`
	Version      string                `json:"version"`
}
