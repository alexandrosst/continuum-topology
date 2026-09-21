// Package approval is the short code that ties an administrator's "approve" to one specific cluster.
//
// The agent makes a random code when it enrolls and prints it in its own log, which only someone with
// access to the cluster can read. It sends the server a hash of the code, never the code. To approve
// the agent, the administrator types the code, and the server checks it against the hash. Approving
// the wrong request (a look-alike cluster name, two clusters waiting at once, a token that leaked)
// then needs a code that only the right cluster's log shows.
package approval

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"strings"

	"golang.org/x/crypto/argon2"
)

// alphabet is Crockford's base32: digits and letters without I, L, O and U. Eight characters carry
// 40 bits. The characters that can still be misread (0/O, 1/I/L) are accepted either way by Normalize.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Length is how many characters the code has, without the dash.
const Length = 8

// HashLen is the size of what Hash returns.
const HashLen = 32

// The hash is deliberately slow (16 MiB, 2 passes): the server holds it, and a stolen copy of the
// database should not let someone find the 40-bit code offline in an afternoon. The server checks at
// most five guesses per pending agent, so the cost to it is negligible.
const (
	argonTime    = 2
	argonMemKiB  = 16 * 1024
	argonThreads = 1
)

// New returns a fresh random code in the form the agent prints: "K7QM-4TXD".
func New() (string, error) {
	var b [5]byte // 40 bits: exactly eight base32 characters, no rejection sampling needed
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	var out [Length]byte
	for i := Length - 1; i >= 0; i-- {
		out[i] = alphabet[v&31]
		v >>= 5
	}
	return Format(string(out[:])), nil
}

// Format writes a normalised code as XXXX-XXXX.
func Format(norm string) string {
	if len(norm) != Length {
		return norm
	}
	return norm[:4] + "-" + norm[4:]
}

// Normalize turns whatever a person typed or pasted into the canonical eight characters: any case,
// spaces and dashes ignored, O read as 0 and I or L as 1. ok is false when it cannot be a code.
func Normalize(s string) (norm string, ok bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == ' ' || r == '-' || r == '_' || r == '\t' || r == '\n' || r == '\r':
			continue
		case r == 'O':
			r = '0'
		case r == 'I' || r == 'L':
			r = '1'
		}
		if r > 0x7f || !strings.ContainsRune(alphabet, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != Length {
		return "", false
	}
	return b.String(), true
}

// KeyID identifies the public key of a certificate request: the SHA-256 of its DER encoding.
func KeyID(csrDER []byte) ([]byte, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	pub, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pub)
	return sum[:], nil
}

// Hash binds a code to the public key of one enrollment request, so a hash copied from another
// enrollment is no use with a different key. code may be typed loosely; it is normalised first.
func Hash(code string, csrDER []byte) ([]byte, error) {
	norm, ok := Normalize(code)
	if !ok {
		return nil, errors.New("not an approval code")
	}
	salt, err := KeyID(csrDER)
	if err != nil {
		return nil, err
	}
	return argon2.IDKey([]byte("continuum-approval-v1:"+norm), salt, argonTime, argonMemKiB, argonThreads, HashLen), nil
}

// Matches reports, in constant time, whether typed is the code behind hash for the request csrDER.
// A string that is not a code at all never matches.
func Matches(typed string, csrDER, hash []byte) bool {
	if len(hash) != HashLen {
		return false
	}
	got, err := Hash(typed, csrDER)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, hash) == 1
}
