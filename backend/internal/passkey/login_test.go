package passkey

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"

	"ikhnos/internal/accounts"
	"ikhnos/internal/store"
)

// A software authenticator: just enough to drive the real go-webauthn registration and login ceremonies
// through Provider, so flag handling is tested against the library's own validation rather than assumed.

const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08 // backup eligible: set by every synced passkey (iCloud Keychain, Google Password Manager, ...)
	flagBS = 0x10
	flagAT = 0x40
)

var b64 = base64.RawURLEncoding.EncodeToString

type softKey struct {
	priv   *ecdsa.PrivateKey
	cose   []byte
	credID []byte
}

func newSoftKey(t *testing.T) softKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         int64(webauthncose.P256),
		XCoord:        priv.X.FillBytes(make([]byte, 32)),
		YCoord:        priv.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return softKey{priv: priv, cose: cose, credID: []byte("credential-id-0123456789")}
}

func challengeOf(t *testing.T, optionsJSON []byte) string {
	t.Helper()
	var o struct {
		PublicKey struct{ Challenge string } `json:"publicKey"`
	}
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		t.Fatal(err)
	}
	return o.PublicKey.Challenge
}

// register runs a "none"-attestation registration whose authenticator data carries flags, and returns what the
// provider hands to the store.
func (k softKey) register(t *testing.T, flags byte) store.WebAuthnCredential {
	t.Helper()
	p, rp, u := New(), testRP(), testUser()
	opts, sess, err := p.BeginRegistration(rp, u)
	if err != nil {
		t.Fatal(err)
	}
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": challengeOf(t, opts), "origin": rp.Origin})
	rpHash := sha256.Sum256([]byte(rp.ID))
	ad := append(rpHash[:], flags|flagAT)
	ad = binary.BigEndian.AppendUint32(ad, 0)
	ad = append(ad, make([]byte, 16)...) // AAGUID
	ad = binary.BigEndian.AppendUint16(ad, uint16(len(k.credID)))
	ad = append(ad, k.credID...)
	ad = append(ad, k.cose...)
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]any{"id": b64(k.credID), "rawId": b64(k.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(cd), "attestationObject": b64(att)}})
	cred, err := p.FinishRegistration(rp, u, sess, resp)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	return cred
}

// login asserts with flags and counter against the stored credential and returns the provider's answer.
func (k softKey) login(t *testing.T, stored store.WebAuthnCredential, flags byte, counter uint32) (uint32, error) {
	t.Helper()
	p, rp := New(), testRP()
	u := accounts.WebAuthnUser{ID: "user-1", Username: "quinn", Credentials: []store.WebAuthnCredential{stored}}
	opts, sess, err := p.BeginLogin(rp, u)
	if err != nil {
		t.Fatal(err)
	}
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": challengeOf(t, opts), "origin": rp.Origin})
	rpHash := sha256.Sum256([]byte(rp.ID))
	ad := append(rpHash[:], flags)
	ad = binary.BigEndian.AppendUint32(ad, counter)
	cdh := sha256.Sum256(cd)
	sum := sha256.Sum256(append(append([]byte{}, ad...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]any{"id": b64(k.credID), "rawId": b64(k.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(cd), "authenticatorData": b64(ad), "signature": b64(sig)}})
	id, sc, err := p.FinishLogin(rp, u, sess, resp)
	if err == nil && string(id) != string(k.credID) {
		t.Fatalf("FinishLogin returned credential %q", id)
	}
	return sc, err
}

// A synced passkey (backup eligible) registers, goes through the real store's JSON column, and signs in. Before
// the flags were kept, go-webauthn saw BE=false on the stored side and refused every such login.
func TestBackupEligiblePasskeyCanLogInAfterTheStoreRoundTrip(t *testing.T) {
	k := newSoftKey(t)
	cred := k.register(t, flagUP|flagUV|flagBE|flagBS)
	if cred.Flags&(flagBE|flagBS) != flagBE|flagBS {
		t.Fatalf("registered flags = %#x", cred.Flags)
	}

	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(ctx, store.User{ID: "user-1", Username: "quinn", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cred.Name = "iCloud Keychain"
	cred.CreatedAt = time.Now()
	if err := st.AddWebAuthnCredential(ctx, "user-1", cred); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUser(ctx, "user-1")
	if err != nil || len(u.WebAuthnCredentials) != 1 {
		t.Fatalf("GetUser: %v, credentials %d", err, len(u.WebAuthnCredentials))
	}
	stored := u.WebAuthnCredentials[0]
	if stored.Flags != cred.Flags {
		t.Fatalf("flags after the store round trip = %#x, want %#x", stored.Flags, cred.Flags)
	}

	if _, err := k.login(t, stored, flagUP|flagUV|flagBE|flagBS, 0); err != nil {
		t.Fatalf("login with a backup-eligible passkey: %v", err)
	}
	// The BE bit must still never change: a credential registered BE=1 that asserts BE=0 is refused.
	if _, err := k.login(t, stored, flagUP|flagUV, 0); err == nil {
		t.Fatal("login with the backup-eligible bit dropped was accepted")
	}
}

// A device-bound key (BE=0) keeps working, and so does a row written before flags were stored (zero).
func TestDeviceBoundPasskeyStillLogsIn(t *testing.T) {
	k := newSoftKey(t)
	cred := k.register(t, flagUP|flagUV)
	if cred.Flags&flagBE != 0 {
		t.Fatalf("registered flags = %#x", cred.Flags)
	}
	if _, err := k.login(t, cred, flagUP|flagUV, 1); err != nil {
		t.Fatal(err)
	}
	legacy := cred
	legacy.Flags = 0
	if _, err := k.login(t, legacy, flagUP|flagUV, 1); err != nil {
		t.Fatalf("legacy row without flags: %v", err)
	}
}

// go-webauthn leaves the counter unchanged and sets CloneWarning when it does not advance, and returns no
// error; the provider has to turn that into a refusal because the server only looks at the error.
func TestLoginRejectsASignCounterThatDidNotAdvance(t *testing.T) {
	k := newSoftKey(t)
	cred := k.register(t, flagUP|flagUV)
	cred.SignCount = 5
	for name, counter := range map[string]uint32{"lower": 1, "equal": 5} {
		if _, err := k.login(t, cred, flagUP|flagUV, counter); err == nil {
			t.Errorf("counter %s than stored was accepted", name)
		}
	}
	if sc, err := k.login(t, cred, flagUP|flagUV, 6); err != nil || sc != 6 {
		t.Fatalf("advancing counter: sc=%d err=%v", sc, err)
	}
}

// Synced passkeys report a counter of 0 every time; that is not a clone.
func TestLoginAcceptsAZeroCounterOnBothSides(t *testing.T) {
	k := newSoftKey(t)
	cred := k.register(t, flagUP|flagUV|flagBE)
	if sc, err := k.login(t, cred, flagUP|flagUV|flagBE, 0); err != nil || sc != 0 {
		t.Fatalf("sc=%d err=%v", sc, err)
	}
}
