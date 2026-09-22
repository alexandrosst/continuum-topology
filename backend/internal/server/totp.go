package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // required by RFC 6238/4226, not used for anything else
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Two-factor authentication is TOTP (RFC 6238) over HOTP (RFC 4226): the same 6-digit-code-from-an-app
// scheme every authenticator app (Google Authenticator, Authy, 1Password, ...) already speaks, implemented
// against the standard library alone rather than pulling in a dependency for forty lines of well-specified
// math. A 30-second step and 6 digits are what every one of those apps assumes; nothing here is configurable.
const (
	totpDigits = 6
	totpStep   = 30 * time.Second
	// totpSecretBytes: 160 bits, the length RFC 4226 recommends and what every authenticator app expects -
	// some accept longer secrets, not all of them.
	totpSecretBytes = 20
	// totpSkew allows the code from one step before or after now, so a phone's clock being a little off (or
	// the round trip to submit the code) does not fail a code that was correct when it was typed.
	totpSkew = 1
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh random secret, base32-encoded the way authenticator apps expect it typed or
// scanned.
func NewTOTPSecret() (string, error) {
	b := make([]byte, totpSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32NoPad.EncodeToString(b), nil
}

// totpAt is HOTP(secret, counter) truncated to totpDigits, the way RFC 4226 §5.3 defines it.
func totpAt(secret string, counter uint64) (string, error) {
	key, err := base32NoPad.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("invalid secret: %w", err)
	}
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(c[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0xf
	code := (uint32(sum[offset]&0x7f) << 24) | (uint32(sum[offset+1]) << 16) | (uint32(sum[offset+2]) << 8) | uint32(sum[offset+3])
	mod := uint32(1)
	for range totpDigits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, code%mod), nil
}

// VerifyTOTP checks a 6-digit code against the secret, allowing ±totpSkew steps of clock drift. An
// unparsable secret or a code that is not exactly totpDigits digits is simply not a match - never an error a
// caller has to remember to check, since a stored secret is always one this package wrote.
func VerifyTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	for _, d := range code {
		if d < '0' || d > '9' {
			return false
		}
	}
	step := uint64(now.Unix()) / uint64(totpStep.Seconds())
	for delta := int64(-totpSkew); delta <= int64(totpSkew); delta++ {
		counter := step
		if delta < 0 && uint64(-delta) > counter {
			continue // never wrap before the epoch
		}
		counter += uint64(delta)
		want, err := totpAt(secret, counter)
		if err == nil && hmac.Equal([]byte(want), []byte(code)) {
			return true
		}
	}
	return false
}

// otpauthURL is the otpauth:// URI an authenticator app's "scan a QR code" flow reads; shown as text (and a
// copyable secret) rather than rendered as an actual QR code image, since every authenticator app also
// accepts typing the secret in by hand and that needs no extra library to draw.
func otpauthURL(issuer, account, secret string) string {
	v := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// recoveryAlphabet excludes look-alikes (0/O, 1/I/L) the same way randomPassword does.
const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewRecoveryCode returns one code in the "xxxxx-xxxxx" shape people can read back to themselves without
// losing their place, from an alphabet with no look-alike characters.
func NewRecoveryCode() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = recoveryAlphabet[int(b[i])%len(recoveryAlphabet)] // tiny modulo bias is irrelevant at this length
	}
	return string(b[:5]) + "-" + string(b[5:]), nil
}

// NewRecoveryCodes returns n fresh codes and their hashes: the codes are shown to the person once (like an
// enrollment token), only the hashes are ever stored.
func NewRecoveryCodes(n int) (codes []string, hashes []string, err error) {
	codes = make([]string, n)
	hashes = make([]string, n)
	for i := range n {
		c, err := NewRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		codes[i] = c
		hashes[i] = recoveryHash(c)
	}
	return codes, hashes, nil
}

// normalizeRecoveryCode undoes the formatting (case, dashes, stray spaces) a person might retype a recovery
// code with, so "ABCDE-FGHJK", "abcde fghjk" and "abcdefghjk" all check the same code.
func normalizeRecoveryCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", " ", "").Replace(s)
}

func recoveryHash(code string) string {
	return fmt.Sprintf("%x", HashSecret(normalizeRecoveryCode(code)))
}

// consumeRecoveryCode reports whether code matches one of hashes, and if so returns the remaining ones (the
// matched one removed) so the caller can store that back - a recovery code works exactly once.
func consumeRecoveryCode(hashes []string, code string) (remaining []string, ok bool) {
	h := recoveryHash(code)
	for i, have := range hashes {
		if hmac.Equal([]byte(have), []byte(h)) {
			remaining = make([]string, 0, len(hashes)-1)
			remaining = append(remaining, hashes[:i]...)
			remaining = append(remaining, hashes[i+1:]...)
			return remaining, true
		}
	}
	return hashes, false
}
