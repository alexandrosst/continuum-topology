package server

import (
	"strings"
	"testing"
	"time"
)

func TestSignDeciderRequestRoundTripsAndRejectsTamperingReplayAndWrongSecret(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"schema":1,"question":"placement"}`)

	ts, sig := signDeciderRequest("s3cr3t-enough-to-pass-validation", body, now)
	if ts == "" || sig == "" {
		t.Fatal("empty timestamp or signature")
	}
	if !verifyDeciderSignature("s3cr3t-enough-to-pass-validation", body, ts, sig, now, time.Minute) {
		t.Fatal("a fresh, correct signature did not verify")
	}
	if verifyDeciderSignature("a-completely-different-secret", body, ts, sig, now, time.Minute) {
		t.Fatal("verified under the wrong secret")
	}
	if verifyDeciderSignature("s3cr3t-enough-to-pass-validation", append(append([]byte{}, body...), '!'), ts, sig, now, time.Minute) {
		t.Fatal("verified after the body changed (the signature must cover the body)")
	}
	if verifyDeciderSignature("s3cr3t-enough-to-pass-validation", body, ts, sig, now.Add(10*time.Minute), 5*time.Minute) {
		t.Fatal("a stale request verified as fresh (no replay protection)")
	}
	if verifyDeciderSignature("s3cr3t-enough-to-pass-validation", body, ts, sig, now.Add(-10*time.Minute), 5*time.Minute) {
		t.Fatal("a signature evaluated well outside its window in the other direction still verified")
	}
	if verifyDeciderSignature("s3cr3t-enough-to-pass-validation", body, "not-a-number", sig, now, time.Minute) {
		t.Fatal("a non-numeric timestamp verified")
	}
	if verifyDeciderSignature("s3cr3t-enough-to-pass-validation", body, ts, "not-hex-at-all", now, time.Minute) {
		t.Fatal("garbage in the signature field verified")
	}

	// Two organisations never produce the same signature for the same body, because each signs with its own secret.
	_, sigA := signDeciderRequest("org-a-secret-well-past-16-chars", body, now)
	_, sigB := signDeciderRequest("org-b-secret-well-past-16-chars", body, now)
	if sigA == sigB {
		t.Fatal("two different secrets produced the same signature")
	}
	if strings.Contains(sig, "s3cr3t") {
		t.Fatal("the secret leaked verbatim into its own signature's encoding")
	}
}
