package pki

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// spkiPrefix starts the public-key pin form: "sha256/<base64 or hex of SHA-256(SubjectPublicKeyInfo)>".
// It survives re-issuing the CA certificate with the same key, which the legacy form (a hash of the whole
// certificate) does not.
const spkiPrefix = "sha256/"

// NormalizePin accepts the legacy forms "sha256:abcd..." and bare hex, and returns bare lower-case hex.
// A public-key pin ("sha256/...") is case-sensitive (base64) and is returned trimmed but otherwise unchanged.
func NormalizePin(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, spkiPrefix) {
		return p
	}
	return strings.ToLower(strings.TrimPrefix(p, "sha256:"))
}

// SPKIPin returns the public-key pin of a DER certificate: "sha256/" plus the standard base64 of the
// SHA-256 of its SubjectPublicKeyInfo (the same value HPKP and curl's --pinnedpubkey use).
func SPKIPin(der []byte) string {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return spkiPrefix + base64.StdEncoding.EncodeToString(sum[:])
}

// SPKIPin is the public-key pin of this CA.
func (ca *CA) SPKIPin() string { return SPKIPin(ca.DER) }

// PinMatches reports whether the CA certificate der is the one pin names. pin may be the legacy
// certificate fingerprint (hex, optionally "sha256:"-prefixed) or a public-key pin
// ("sha256/<base64 or hex>"). The comparison is constant-time.
func PinMatches(pin string, der []byte) bool {
	pin = NormalizePin(pin)
	if pin == "" || len(der) == 0 {
		return false
	}
	if rest, ok := strings.CutPrefix(pin, spkiPrefix); ok {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return false
		}
		want := sha256.Sum256(c.RawSubjectPublicKeyInfo)
		got, ok := decodeDigest(rest)
		return ok && subtle.ConstantTimeCompare(got, want[:]) == 1
	}
	return subtle.ConstantTimeCompare([]byte(PinOf(der)), []byte(pin)) == 1
}

// decodeDigest reads a 32-byte digest written in hex or in base64 (padded or not, standard or URL alphabet).
func decodeDigest(s string) ([]byte, bool) {
	if b, err := hex.DecodeString(s); err == nil && len(b) == sha256.Size {
		return b, true
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == sha256.Size {
			return b, true
		}
	}
	return nil, false
}

// ClientTLS builds the agent's TLS configuration. The agent trusts exactly one CA,
// identified by its SHA-256 pin from the install command, and checks that the server
// certificate chains to it and matches host. No system roots are involved, so a
// public CA cannot vouch for the server. cert is nil during enrollment and set once
// the agent holds a certificate.
func ClientTLS(pin, host string, cert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: host,
		// Standard verification is replaced by the pinned check below; it is stricter
		// (one trusted root) not weaker.
		InsecureSkipVerify:    true, //nolint:gosec
		VerifyPeerCertificate: pinnedVerifier(pin, host),
	}
	if cert != nil {
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
	}
	return cfg
}

func pinnedVerifier(pin, host string) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) < 2 {
			return errors.New("server did not present its CA certificate")
		}
		ca, err := x509.ParseCertificate(raw[len(raw)-1])
		if err != nil {
			return err
		}
		if !PinMatches(pin, ca.Raw) {
			return fmt.Errorf("server CA does not match the pinned fingerprint")
		}
		leaf, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return err
		}
		roots := x509.NewCertPool()
		roots.AddCert(ca)
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return err
	}
}
