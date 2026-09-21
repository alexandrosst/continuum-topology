package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: 64 MiB, 2 passes, 2 lanes. Above the OWASP minimum (19 MiB, 2, 1) and
// still well under a second on small machines. Stored in each hash, so they can be raised later
// without invalidating existing passwords.
// Variables only so tests can use cheap settings; production always runs the values below.
var (
	argonMemoryKiB uint32 = 64 * 1024
	argonTime      uint32 = 2
	argonThreads   uint8  = 2
)

const (
	argonKeyLen  = 32
	argonSaltLen = 16

	MinPasswordLen = 12
	MaxPasswordLen = 128
)

// HashPassword returns a PHC-format argon2id hash with a fresh random salt.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errBadHash = errors.New("unrecognised password hash")

// VerifyPassword checks a password against a hash produced by HashPassword in constant time.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 512*1024 || t == 0 || t > 10 || p == 0 || p > 16 {
		return false, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// CheckPasswordPolicy enforces length (NIST SP 800-63B favours length over composition rules)
// and refuses the obvious: the username itself, or a single repeated character.
func CheckPasswordPolicy(username, password string) error {
	if len(password) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(password) > MaxPasswordLen {
		return fmt.Errorf("password must be at most %d characters", MaxPasswordLen)
	}
	if strings.EqualFold(password, username) {
		return errors.New("password must not be the username")
	}
	if strings.Trim(password, password[:1]) == "" {
		return errors.New("password must not be a single repeated character")
	}
	return nil
}

// randomPassword returns 20 random characters from an alphabet without look-alikes.
func randomPassword() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)] // tiny modulo bias is irrelevant at this length
	}
	return string(b), nil
}
