package store

import (
	"testing"
	"time"
)

// The authenticator flags live in the existing credentials JSON column, so no migration: a row written before
// they were kept has no "flags" key and reads back as zero.
func TestWebAuthnCredentialFlagsRoundTripAndLegacyRows(t *testing.T) {
	in := []WebAuthnCredential{{CredentialID: []byte("id"), PublicKey: []byte("pk"), Flags: 0x59, Name: "n", CreatedAt: time.UnixMilli(1)}}
	out := parseWebAuthnCredentials(webauthnCredentialsJSON(in))
	if len(out) != 1 || out[0].Flags != 0x59 {
		t.Fatalf("round trip = %+v", out)
	}
	legacy := parseWebAuthnCredentials(`[{"credentialId":"aWQ","publicKey":"cGs","signCount":3,"name":"old","createdAt":1}]`)
	if len(legacy) != 1 || legacy[0].Flags != 0 || legacy[0].SignCount != 3 {
		t.Fatalf("legacy row = %+v", legacy)
	}
}
