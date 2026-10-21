package accountsapi

import (
	"encoding/json"
	"testing"

	"continuum/internal/wire"
)

func TestEveryDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, ".", WireTypes())
}

// The answers that used to be maps are structs now, with their fields in the order a map's keys were encoded in. Each is checked against
// the bytes the map wrote, so that a client that compares them, or a log that was grepped, sees no difference.
func TestAnswersThatWereMapsAreWrittenAsTheyWere(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"session", sessionDoc{Orgs: []orgDoc{{ID: "o1", Name: "Acme", Role: "owner"}}, User: userDoc{ID: "u1", Username: "alex", Passkeys: []passkeyDoc{}}},
			`{"orgs":[{"id":"o1","name":"Acme","role":"owner"}],"user":{"id":"u1","username":"alex","mustChangePassword":false,"createdAt":"","twoFactorEnabled":false,"emailVerified":false,"emailOtpEnabled":false,"mailConfigured":false,"canManageMail":false,"passkeys":[]}}`},
		{"two-factor setup", twoFactorSetupDoc{OtpauthURL: "otpauth://x", Secret: "S"}, `{"otpauthUrl":"otpauth://x","secret":"S"}`},
		{"recovery codes", recoveryCodesDoc{RecoveryCodes: []string{"a", "b"}}, `{"recoveryCodes":["a","b"]}`},
		{"created token", createdAPITokenDoc{CreatedAt: "t", ID: "i", Name: "n", Token: "k"}, `{"createdAt":"t","id":"i","name":"n","token":"k"}`},
		{"ok", okDoc{OK: true}, `{"ok":true}`},
		{"invite preview", invitePreviewDoc{ExpiresAt: "e", Organisation: "Acme", Role: "viewer"}, `{"expiresAt":"e","organisation":"Acme","role":"viewer"}`},
		{"created invite", createdInviteDoc{Invite: inviteDoc{ID: "i", Role: "viewer", CreatedBy: "alex", CreatedAt: "c", ExpiresAt: "e"}, Token: "k"},
			`{"invite":{"id":"i","role":"viewer","createdBy":"alex","createdAt":"c","expiresAt":"e","used":false},"token":"k"}`},
		{"server info", serverInfoDoc{Registration: "open", SSO: true, Version: "v1"}, `{"registration":"open","sso":true,"version":"v1"}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}
