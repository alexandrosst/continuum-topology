package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const tokenPrefix = "cnt_"

// operatorPrefix marks a regional operator's receiver bearer token - distinct from an enrollment
// token, since an operator never enrolls (see store.Operator's own comment); it is only ever checked
// by the operator's own OTel Collector receiver, never by the enrollment/agent machinery.
const operatorPrefix = "cno_"

// patPrefix marks a personal access token - distinct from tokenPrefix's enrollment tokens and
// sessionPrefix's browser sessions, so a secret's own shape says which kind it is before anything
// looks it up.
const patPrefix = "cnk_"

// gatewayTokenPrefix marks a quick-start gateway token (see store.GatewayToken) - distinct from every
// other secret this package mints, so its own shape says which kind it is. Checked entirely by the Part C
// nginx gateway the admin deploys alongside a quick-start backend, never by this server.
const gatewayTokenPrefix = "cnq_"

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

// NewOperatorReceiverSecret returns a fresh bearer token for a regional operator's OTLP receiver. Only
// HashSecret(secret) is stored - the same rule as every other secret this package mints.
func NewOperatorReceiverSecret() (string, error) {
	s, err := newSecret()
	return operatorPrefix + s, err
}

// NewAPITokenSecret returns a fresh personal access token. Only HashSecret(secret) is stored - the
// same rule as an enrollment token or a session.
func NewAPITokenSecret() (string, error) {
	s, err := newSecret()
	return patPrefix + s, err
}

// NewGatewayTokenSecret returns a fresh quick-start gateway bearer token. Only HashSecret(secret) is
// stored - the same rule as every other secret this package mints.
func NewGatewayTokenSecret() (string, error) {
	s, err := newSecret()
	return gatewayTokenPrefix + s, err
}

// looksLikeAPIToken cheaply rejects malformed input before touching the database.
func looksLikeAPIToken(s string) bool {
	if !strings.HasPrefix(s, patPrefix) || len(s) != len(patPrefix)+43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s[len(patPrefix):])
	return err == nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system CSPRNG failing is unrecoverable
	}
	return hex.EncodeToString(b)
}

func newAgentID() string           { return "ag-" + randHex(6) }
func newTokenID() string           { return "tk-" + randHex(6) }
func newAPITokenID() string        { return "pat-" + randHex(6) }
func newOperatorID() string        { return "op-" + randHex(6) }
func newGatewayTokenID() string    { return "gwt-" + randHex(6) }
func newTelemetryIntentID() string { return "ti-" + randHex(6) }

// ClusterIDFor is stable for a cluster (its kube-system UID), so records keep the same id
// even if the agent is replaced.
func ClusterIDFor(org, fingerprint string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + fingerprint))
	return "cl-" + hex.EncodeToString(sum[:5])
}
