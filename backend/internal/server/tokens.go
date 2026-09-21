package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const tokenPrefix = "cnt_"

// newSecret returns 32 random bytes as URL-safe text.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewTokenSecret returns a fresh enrollment token. Only HashSecret(secret) is stored.
func NewTokenSecret() (string, error) {
	s, err := newSecret()
	return tokenPrefix + s, err
}

func HashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// LooksLikeToken cheaply rejects malformed input before touching the database.
func LooksLikeToken(s string) bool {
	if !strings.HasPrefix(s, tokenPrefix) || len(s) != len(tokenPrefix)+43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s[len(tokenPrefix):])
	return err == nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system CSPRNG failing is unrecoverable
	}
	return hex.EncodeToString(b)
}

func newAgentID() string { return "ag-" + randHex(6) }
func newTokenID() string { return "tk-" + randHex(6) }

// ClusterIDFor is stable for a cluster (its kube-system UID), so records keep the same id
// even if the agent is replaced.
func ClusterIDFor(org, fingerprint string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + fingerprint))
	return "cl-" + hex.EncodeToString(sum[:5])
}
