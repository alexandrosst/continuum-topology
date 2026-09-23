package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"time"
)

// signDeciderRequest signs a decision request the way Stripe and GitHub sign outgoing webhooks: HMAC-SHA256 over
// "<unix timestamp>.<body>", keyed by the organisation's own decider secret (Settings.DeciderSecret). The decider
// can then refuse anything that does not carry a valid signature under its copy of the secret, or whose
// timestamp is too old to be the request it was sent - i.e. it can tell "this came from a Continuum server that
// knows the secret, moments ago" from a guess at the address, a network path that can read the body, or a
// captured request being replayed later. The timestamp is folded into what is signed (not left as an
// unauthenticated header) specifically so it cannot be changed without also invalidating the signature.
//
// This is optional and additive: a decider that does not check the signature works exactly as it did before one
// was configured (see docs/decider-webhook.openapi.yaml for the full contract, including these headers).
func signDeciderRequest(secret string, body []byte, now time.Time) (timestamp, signature string) {
	timestamp = strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return timestamp, hex.EncodeToString(mac.Sum(nil))
}

// verifyDeciderSignature is not called by this server (it signs; it never receives a decision request), but is
// exported in spirit through the OpenAPI doc's description of the scheme - kept here, and tested, as the
// reference implementation a decider author can port line for line into whatever language they write in.
func verifyDeciderSignature(secret string, body []byte, timestamp, signature string, now time.Time, maxAge time.Duration) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if age > maxAge {
		return false
	}
	_, want := signDeciderRequest(secret, body, time.Unix(ts, 0))
	return subtle.ConstantTimeCompare([]byte(signature), []byte(want)) == 1
}
