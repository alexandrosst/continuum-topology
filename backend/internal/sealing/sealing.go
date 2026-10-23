// Package sealing keeps the few secrets this server must be able to read back (a second-factor secret, a mail password, a decider's
// signing secret) unreadable to anyone who has only the database: a copy of the file, a backup, a disk image.
//
// A password is stored as a hash, because nothing ever needs it again. These cannot be: a login has to reproduce a live code from the
// TOTP secret, the mail relay needs its password in the clear to be given it. They are encrypted instead, with AES-256-GCM, under a key that
// is not in the database and not in the data directory: it is a file the operator mounts (a Kubernetes Secret). A sealed value is bound to
// the place it belongs to (the user, the organisation), so copying one row's value into another does not open there.
//
// The key file holds one key per line. The first seals; the others only open, so a key is replaced by putting the new one on top and
// restarting, and taking the old one off once nothing is sealed under it any longer (the server re-seals what it finds at startup).
//
// This package knows nothing of the server: it stands on the standard library.
package sealing

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// MinKeyLen is the shortest key accepted, in characters of the file's line. `openssl rand -base64 48` gives 64.
const MinKeyLen = 32

const (
	prefix = "sealed:v1:"
	salt   = "ikhnos-sealing-v1"
)

var (
	// ErrNoKey is a sealed value met by a server that was given no key.
	ErrNoKey = errors.New("the value is sealed and this server has no sealing key")
	// ErrCorrupt is a sealed value that does not open under the key it names: another place's value, or one that was changed.
	ErrCorrupt = errors.New("the sealed value does not open (it belongs to another place, or it was changed)")
)

// UnknownKeyError is a sealed value whose key is not among the ones the server was given.
type UnknownKeyError struct{ ID string }

func (e *UnknownKeyError) Error() string {
	return fmt.Sprintf("the value is sealed under key %s, which is not in the sealing key file", e.ID)
}

type key struct {
	id  string
	ikm []byte
}

// Sealer seals and opens. The zero value, and nil, are a sealer that is off: Seal returns what it is given and Open refuses a sealed value.
type Sealer struct {
	keys []key // keys[0] seals
}

// ParseKeys reads the content of a key file: one key per line, blank lines and lines starting with # ignored, surrounding spaces dropped.
func ParseKeys(content []byte) ([][]byte, error) {
	var out [][]byte
	for _, line := range bytes.Split(content, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		out = append(out, append([]byte(nil), line...))
	}
	if len(out) == 0 {
		return nil, errors.New("the sealing key file holds no key")
	}
	return out, nil
}

// New is a sealer for the keys, the first of which seals. Each must be at least MinKeyLen long and no two may be the same.
func New(keys ...[]byte) (*Sealer, error) {
	if len(keys) == 0 {
		return nil, errors.New("a sealer needs a key")
	}
	s := &Sealer{}
	seen := map[string]bool{}
	for i, k := range keys {
		if len(k) < MinKeyLen {
			return nil, fmt.Errorf("sealing key %d is %d characters: it must be at least %d (openssl rand -base64 48 makes one)", i+1, len(k), MinKeyLen)
		}
		id, err := keyID(k)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, fmt.Errorf("sealing key %d is the same as an earlier one", i+1)
		}
		seen[id] = true
		s.keys = append(s.keys, key{id: id, ikm: append([]byte(nil), k...)})
	}
	return s, nil
}

func keyID(ikm []byte) (string, error) {
	b, err := hkdf.Key(sha256.New, ikm, []byte(salt), "key-id", 4)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Enabled is whether this sealer seals.
func (s *Sealer) Enabled() bool { return s != nil && len(s.keys) > 0 }

// KeyID names the key that seals: eight hex characters that identify it without giving it away. Empty for a sealer that is off.
func (s *Sealer) KeyID() string {
	if !s.Enabled() {
		return ""
	}
	return s.keys[0].id
}

// KeyIDs names every key the sealer holds, the sealing one first.
func (s *Sealer) KeyIDs() []string {
	if !s.Enabled() {
		return nil
	}
	out := make([]string, len(s.keys))
	for i, k := range s.keys {
		out[i] = k.id
	}
	return out
}

// IsSealed is whether a stored value is in the sealed form. An empty value is not: a secret that was never set stays empty.
func IsSealed(stored string) bool { return strings.HasPrefix(stored, prefix) }

// SealedUnder is the key a sealed value names ("" when the value is not sealed).
func SealedUnder(stored string) string {
	if !IsSealed(stored) {
		return ""
	}
	id, _, ok := strings.Cut(stored[len(prefix):], ":")
	if !ok {
		return ""
	}
	return id
}

// Current is whether a stored value needs nothing done to it: empty, or sealed under the sealing key. A value that is plain, or sealed under
// an older key, is not current; the server seals it again at startup.
func (s *Sealer) Current(stored string) bool {
	if stored == "" {
		return true
	}
	if !s.Enabled() {
		return !IsSealed(stored)
	}
	return SealedUnder(stored) == s.keys[0].id
}

func (k key) aead(purpose string) (cipher.AEAD, error) {
	b, err := hkdf.Key(sha256.New, k.ikm, []byte(salt), purpose, 32)
	if err != nil {
		return nil, err
	}
	defer clear(b)
	blk, err := aes.NewCipher(b)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func aad(purpose, where string) []byte { return []byte(purpose + "\x00" + where) }

// Seal returns plain in the sealed form, bound to purpose (what kind of secret it is) and where (which one: a user's id, an organisation's).
// An empty value stays empty. A sealer that is off returns plain unchanged.
func (s *Sealer) Seal(purpose, where, plain string) (string, error) {
	if plain == "" || !s.Enabled() {
		return plain, nil
	}
	k := s.keys[0]
	g, err := k.aead(purpose)
	if err != nil {
		return "", err
	}
	n := make([]byte, g.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	ct := g.Seal(n, n, []byte(plain), aad(purpose, where))
	return prefix + k.id + ":" + base64.RawURLEncoding.EncodeToString(ct), nil
}

// Open returns the value a stored one holds. A value that is not sealed is returned as it is (it was stored before sealing was on).
func (s *Sealer) Open(purpose, where, stored string) (string, error) {
	if !IsSealed(stored) {
		return stored, nil
	}
	if !s.Enabled() {
		return "", ErrNoKey
	}
	id, body, ok := strings.Cut(stored[len(prefix):], ":")
	if !ok {
		return "", ErrCorrupt
	}
	var k *key
	for i := range s.keys {
		if s.keys[i].id == id {
			k = &s.keys[i]
		}
	}
	if k == nil {
		return "", &UnknownKeyError{ID: id}
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", ErrCorrupt
	}
	g, err := k.aead(purpose)
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize()+g.Overhead() {
		return "", ErrCorrupt
	}
	out, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], aad(purpose, where))
	if err != nil {
		return "", ErrCorrupt
	}
	return string(out), nil
}

// Reseal returns the stored value in the form the sealing key gives it, and whether that is a change. A value already current is left as it is.
func (s *Sealer) Reseal(purpose, where, stored string) (string, bool, error) {
	if s.Current(stored) {
		return stored, false, nil
	}
	plain, err := s.Open(purpose, where, stored)
	if err != nil {
		return stored, false, err
	}
	out, err := s.Seal(purpose, where, plain)
	return out, true, err
}
