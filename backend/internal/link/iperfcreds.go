package link

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// The iperf3 credentials of the throughput tests. The Ikhnos server issues them (see server/linkcreds.go) and delivers them in the
// Config the agent already receives; this file is what the agent, the prober and the responder do with them. They are secret
// material, so everything here is built to keep them out of logs, errors, metrics and API answers:
//
//   - a secret value is a Secret, which prints "[redacted]" under every fmt verb, in slog, and in JSON/text marshalling;
//   - the types that hold them redact themselves too, so a stray %v of a Peer or a Config-derived struct shows nothing;
//   - validation errors name the field and the rule, never the value.

// Secret is a string that does not print itself. Reveal is the one way to read it, used where the value must reach iperf3.
type Secret string

const redacted = "[redacted]"

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return "link.Secret(" + redacted + ")" }
func (Secret) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, redacted) }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (s Secret) Reveal() string             { return string(s) }
func (s Secret) Empty() bool                { return s == "" }

// IperfClient is what this cluster needs to run the throughput tests towards one peer: the peer's public key, a user name and a
// password. It is held in memory only.
type IperfClient struct {
	PublicKeyPEM string
	Username     string
	Password     Secret
}

func (c IperfClient) String() string {
	return "IperfClient{user: " + c.Username + ", password: " + redacted + "}"
}
func (c IperfClient) GoString() string           { return c.String() }
func (c IperfClient) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.String()) }
func (c IperfClient) LogValue() slog.Value       { return slog.StringValue(c.String()) }

// IperfServer is what this cluster's iperf3 servers need: the cluster's private key and the authorized users. Held in memory
// only; the responder writes it to a private in-memory directory for iperf3 to read.
type IperfServer struct {
	PrivateKeyPEM Secret
	UsersCSV      Secret
}

func (s IperfServer) String() string             { return "IperfServer{" + redacted + "}" }
func (s IperfServer) GoString() string           { return s.String() }
func (s IperfServer) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }
func (s IperfServer) LogValue() slog.Value       { return slog.StringValue(s.String()) }

// Empty says there is nothing to serve with.
func (s IperfServer) Empty() bool { return s.PrivateKeyPEM == "" || s.UsersCSV == "" }

// Bounds on what a Config may make the agent hold, whatever the server sends.
const (
	maxPEMBytes      = 8 << 10
	maxUsersLines    = 256
	maxUsersBytes    = 64 << 10
	minPasswordBytes = 16
	maxPasswordBytes = 256
	minRSABits       = 2048
	maxRSABits       = 8192
)

var (
	userRe      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	usersLineRe = regexp.MustCompile(`^([A-Za-z0-9._-]{1,64}),([0-9a-f]{64})$`)
)

// ValidUsername says whether name is a user name the throughput tests accept.
func ValidUsername(name string) bool { return userRe.MatchString(name) }

// Validate checks a client credential: the key is an RSA public key of at least 2048 bits, the name and the password are
// plain. The error never contains the values.
func (c IperfClient) Validate() error {
	if _, err := parseRSAPublic(c.PublicKeyPEM); err != nil {
		return err
	}
	if !ValidUsername(c.Username) {
		return errors.New("iperf3 user name must be 1 to 64 letters, digits, dots, dashes or underscores")
	}
	pw := c.Password.Reveal()
	if len(pw) < minPasswordBytes || len(pw) > maxPasswordBytes {
		return fmt.Errorf("iperf3 password must be %d to %d characters", minPasswordBytes, maxPasswordBytes)
	}
	for i := 0; i < len(pw); i++ {
		if pw[i] <= ' ' || pw[i] > '~' {
			return errors.New("iperf3 password must be printable ASCII without spaces")
		}
	}
	return nil
}

// Validate checks a server credential: an RSA private key of at least 2048 bits and a well-formed users file.
func (s IperfServer) Validate() error {
	if _, err := parseRSAPrivate(s.PrivateKeyPEM.Reveal()); err != nil {
		return err
	}
	return validateUsers(s.UsersCSV.Reveal())
}

func validateUsers(csv string) error {
	if len(csv) > maxUsersBytes {
		return errors.New("iperf3 users file is too large")
	}
	lines := 0
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimRight(csv, "\n"), "\n") {
		if l == "" {
			continue
		}
		m := usersLineRe.FindStringSubmatch(l)
		if m == nil {
			return errors.New("iperf3 users file has a line that is not USER,SHA256")
		}
		if seen[m[1]] {
			return errors.New("iperf3 users file names a user twice")
		}
		seen[m[1]] = true
		if lines++; lines > maxUsersLines {
			return errors.New("iperf3 users file has too many users")
		}
	}
	if lines == 0 {
		return errors.New("iperf3 users file is empty")
	}
	return nil
}

func parseRSAPublic(p string) (*rsa.PublicKey, error) {
	if len(p) == 0 || len(p) > maxPEMBytes {
		return nil, errors.New("iperf3 public key is missing or too large")
	}
	b, rest := pem.Decode([]byte(p))
	if b == nil || b.Type != "PUBLIC KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("iperf3 public key is not a PEM PUBLIC KEY")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	pk, ok := k.(*rsa.PublicKey)
	if err != nil || !ok {
		return nil, errors.New("iperf3 public key is not an RSA public key")
	}
	if bits := pk.N.BitLen(); bits < minRSABits || bits > maxRSABits {
		return nil, fmt.Errorf("iperf3 public key must be %d to %d bits", minRSABits, maxRSABits)
	}
	return pk, nil
}

func parseRSAPrivate(p string) (*rsa.PrivateKey, error) {
	if len(p) == 0 || len(p) > maxPEMBytes {
		return nil, errors.New("iperf3 private key is missing or too large")
	}
	b, rest := pem.Decode([]byte(p))
	if b == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("iperf3 private key is not PEM")
	}
	var k *rsa.PrivateKey
	switch b.Type {
	case "RSA PRIVATE KEY":
		k, _ = x509.ParsePKCS1PrivateKey(b.Bytes)
	case "PRIVATE KEY":
		if a, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
			k, _ = a.(*rsa.PrivateKey)
		}
	}
	if k == nil {
		return nil, errors.New("iperf3 private key is not an RSA private key")
	}
	if bits := k.N.BitLen(); bits < minRSABits || bits > maxRSABits {
		return nil, fmt.Errorf("iperf3 private key must be %d to %d bits", minRSABits, maxRSABits)
	}
	return k, nil
}
