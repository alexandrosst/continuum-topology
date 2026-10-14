package pki

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
)

// DeriveKey returns n bytes (at most 64) of key material for one named purpose, derived with HKDF-SHA256 from this CA's private
// key. It is how the server protects secrets it must read back (see server/linkcreds.go) without a second key to look after:
// the key lives and dies with the CA key, so it is protected at rest exactly as the CA key is (encrypted under the CA
// passphrase when one is configured), it is in the backup that holds the CA, and a CA replaced by a new key makes everything
// sealed under the old derivation unreadable, which callers must treat as "gone" and re-issue.
//
// purpose is mixed into the derivation, so two purposes never share key material; it must be a fixed string chosen by the
// caller, never something an outsider supplies. The result is secret: callers hold it in memory only.
func (ca *CA) DeriveKey(purpose string, n int) ([]byte, error) {
	if ca == nil || ca.key == nil {
		return nil, errors.New("pki: no CA key to derive from")
	}
	if purpose == "" || n <= 0 || n > 64 {
		return nil, errors.New("pki: bad key derivation request")
	}
	d := ca.key.D.FillBytes(make([]byte, 32))
	defer clear(d)
	return hkdf.Key(sha256.New, d, []byte("ikhnos-derived-key-v1"), purpose, n)
}
