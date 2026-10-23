package kernel

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
)

const tokenPrefix = "cnt_"

// OperatorPrefix marks a regional operator's receiver bearer token - distinct from an enrollment
// token, since an operator never enrolls (see store.Operator's own comment); it is only ever checked
// by the operator's own OTel Collector receiver, never by the enrollment/agent machinery.
const OperatorPrefix = "cno_"

// HeartbeatPrefix marks a regional operator's heartbeat secret: what its collector presents to this server
// to report that it is alive. A separate credential from OperatorPrefix's receiver token on purpose - that
// one authenticates traffic coming INTO the operator, this one the operator's own call OUT to us - so a
// leak of either opens only its own direction, and the prefix makes a pasted-into-the-wrong-place secret
// recognisable before anything looks it up.
const HeartbeatPrefix = "cnh_"

// PatPrefix marks a personal access token - distinct from tokenPrefix's enrollment tokens and
// sessionPrefix's browser sessions, so a secret's own shape says which kind it is before anything
// looks it up.
const PatPrefix = "cnk_"

// FusionTokenPrefix marks a FUSION access token (see store.FusionToken): a read credential for FUSION's data API,
// distinct from a personal access token so its own shape says which kind it is - and so one can never be pasted where
// the other is expected and be taken for it.
const FusionTokenPrefix = "cnf_"

// NewSecret returns 32 random bytes as URL-safe text.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewTokenSecret returns a fresh enrollment token. Only HashSecret(secret) is stored.
func NewTokenSecret() (string, error) {
	s, err := NewSecret()
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
	s, err := NewSecret()
	return OperatorPrefix + s, err
}

// NewOperatorHeartbeatSecret returns a fresh heartbeat secret for a regional operator. Only
// HashSecret(secret) is stored, shown once like every other secret this package mints.
func NewOperatorHeartbeatSecret() (string, error) {
	s, err := NewSecret()
	return HeartbeatPrefix + s, err
}

// LooksLikeHeartbeatSecret cheaply rejects malformed input before touching the database.
func LooksLikeHeartbeatSecret(s string) bool {
	if !strings.HasPrefix(s, HeartbeatPrefix) || len(s) != len(HeartbeatPrefix)+43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s[len(HeartbeatPrefix):])
	return err == nil
}

// NewAPITokenSecret returns a fresh personal access token. Only HashSecret(secret) is stored - the
// same rule as an enrollment token or a session.
func NewAPITokenSecret() (string, error) {
	s, err := NewSecret()
	return PatPrefix + s, err
}

// NewFusionTokenSecret returns a fresh FUSION access token. Only HashSecret(secret) is stored.
func NewFusionTokenSecret() (string, error) {
	s, err := NewSecret()
	return FusionTokenPrefix + s, err
}

// LooksLikeFusionToken cheaply rejects malformed input before touching the database.
func LooksLikeFusionToken(s string) bool {
	if !strings.HasPrefix(s, FusionTokenPrefix) || len(s) != len(FusionTokenPrefix)+43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s[len(FusionTokenPrefix):])
	return err == nil
}

// LooksLikeAPIToken cheaply rejects malformed input before touching the database.
func LooksLikeAPIToken(s string) bool {
	if !strings.HasPrefix(s, PatPrefix) || len(s) != len(PatPrefix)+43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s[len(PatPrefix):])
	return err == nil
}

func RandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system CSPRNG failing is unrecoverable
	}
	return hex.EncodeToString(b)
}

func NewAgentID() string           { return "ag-" + RandHex(6) }
func NewTokenID() string           { return "tk-" + RandHex(6) }
func NewAPITokenID() string        { return "pat-" + RandHex(6) }
func NewFusionTokenID() string     { return "fk-" + RandHex(6) }
func NewOperatorID() string        { return "op-" + RandHex(6) }
func NewTelemetryIntentID() string { return "ti-" + RandHex(6) }

// ClusterIDFor is stable for a cluster (its kube-system UID), so records keep the same id
// even if the agent is replaced.
func ClusterIDFor(org, fingerprint string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + fingerprint))
	return "cl-" + hex.EncodeToString(sum[:5])
}

// clusterIDRe is what ClusterIDFor makes: "cl-" and ten hex digits.
var clusterIDRe = regexp.MustCompile(`^cl-[0-9a-f]{10}$`)

// ClusterIDForUID is the cluster id of the cluster whose kube-system namespace has this UID, as the agents there get it.
// False when the UID is not one (a lowercase UUID).
func ClusterIDForUID(org, uid string) (string, bool) {
	uid = strings.ToLower(strings.TrimSpace(uid))
	if !FingerprintRe.MatchString(uid) {
		return "", false
	}
	return ClusterIDFor(org, uid), true
}
