package accountsapi

import (
	"ikhnos/internal/accounts"
	"ikhnos/internal/wire"
)

// WireTypes are the shapes this package puts on the wire (or reads from it), for the TypeScript the server writes of them
// (internal/wire). A document the package encodes and that is not listed here is not in that file: the page would then have to write its
// type by hand again.
func WireTypes() []wire.Root {
	return []wire.Root{
		{Value: userDoc{}},
		{Value: passkeyDoc{}},
		{Value: orgDoc{}},
		{Value: apiTokenDoc{}},
		{Value: memberDoc{}},
		{Value: inviteDoc{}},
		{Value: accounts.MailConfigDoc{}},
		{Value: sessionDoc{}},
		{Value: twoFactorSetupDoc{}},
		{Value: recoveryCodesDoc{}},
		{Value: createdAPITokenDoc{}},
		{Value: invitePreviewDoc{}},
		{Value: createdInviteDoc{}},
		{Value: serverInfoDoc{}},
		{Value: loginRequest{}},
		{Value: login2FARequest{}},
		{Value: registerRequest{}},
		{Value: changePasswordRequest{}},
		{Value: enable2FARequest{}},
		{Value: disable2FARequest{}},
		{Value: requestLoginEmailCodeRequest{}},
		{Value: requestEmailVerificationRequest{}},
		{Value: confirmEmailRequest{}},
		{Value: disableEmailOTPRequest{}},
		{Value: finishPasskeyRegistrationRequest{}},
		{Value: beginPasskeyLoginRequest{}},
		{Value: finishPasskeyLoginRequest{}},
		{Value: renamePasskeyRequest{}},
		{Value: removePasskeyRequest{}},
		{Value: createAPITokenRequest{}},
		{Value: createOrgRequest{}},
		{Value: previewInviteRequest{}},
		{Value: acceptInviteRequest{}},
		{Value: renameOrgRequest{}},
		{Value: deleteOrgRequest{}},
		{Value: setMemberRoleRequest{}},
		{Value: createInviteRequest{}},
		{Value: putMailConfigRequest{}},
	}
}
