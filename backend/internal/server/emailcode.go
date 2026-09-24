package server

import (
	"crypto/hmac"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
)

// emailCodeDigits matches totpDigits: one shape of code to type back, whether it came from an app or an
// inbox.
const emailCodeDigits = 6

// NewEmailCode returns a fresh random 6-digit code, uniform over the full range (000000-999999 including
// the leading zero, so a leaked code's digit count alone reveals nothing).
func NewEmailCode() (string, error) {
	max := big.NewInt(1)
	for range emailCodeDigits {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", emailCodeDigits, n.Int64()), nil
}

// emailCodeHash is what's kept in memory instead of the code itself, the same reasoning as recoveryHash: a
// challenge that only lives for a few minutes and is compared, never reproduced, doesn't need a code an
// attacker who dumps process memory could immediately use, only one they'd have to guess.
func emailCodeHash(code string) string {
	return fmt.Sprintf("%x", HashSecret(strings.TrimSpace(code)))
}

func emailCodeMatches(hash, code string) bool {
	return hmac.Equal([]byte(emailCodeHash(code)), []byte(hash))
}

// emailCodeBody is the message body every mailed code shares: what it is for, the code itself, and how
// long it lasts, so a person opening the email at any point during that window sees an answer rather than
// having to guess whether it is still good.
func emailCodeBody(code, purpose string) string {
	return fmt.Sprintf("Your code to %s is:\n\n    %s\n\nIt expires in %d minutes. If you did not request this, you can ignore this email.",
		purpose, code, int(emailChallengeTTL.Minutes()))
}

// validEmail is deliberately loose (RFC 5322 parsing via the standard library, nothing home-grown) and
// capped in length: this address is never used to prove who someone is, only where to send a code, so the
// only real requirement is that it parses as one address a mail transport would attempt.
func validEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", false
	}
	return addr.Address, true
}
