// Package pki is the server's internal certificate authority.
//
// Trust model: agents never hold a long-lived credential. They generate their own
// ECDSA P-256 key, send a CSR, and receive a certificate that lives for 24 hours.
// The CA private key never leaves the server's data directory (mode 0600).
package pki

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AgentCertTTL is how long an agent certificate is valid. Short on purpose:
// revocation is enforced by the server on every connection, so a stolen certificate is
// useless the moment an administrator revokes the agent. Expiry only bounds the case where
// nobody notices: a thief holding the private key can keep renewing until revocation, which
// is why revocation, not expiry, is the control. A variable only so tests can shorten it.
var AgentCertTTL = 24 * time.Hour

// RejoinWindow is how long after its certificate expired an agent may still rejoin without
// a new enrollment token (for example after a cluster outage or a node reboot).
const RejoinWindow = 7 * 24 * time.Hour

const (
	serverTTL = 30 * 24 * time.Hour
	caTTL     = 10 * 365 * 24 * time.Hour
	// clockSkew backdates certificates so a slightly slow agent clock still works.
	clockSkew = 5 * time.Minute
)

// CA signs server and agent certificates.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	DER  []byte
}

// Options say how the CA key is protected at rest.
type Options struct {
	// Passphrase, when set, encrypts the key on disk: a new key is written encrypted, and an existing
	// plaintext key is encrypted in place the first time. An encrypted key cannot be opened without it.
	Passphrase []byte
	// Log receives the warning about an unencrypted key; nil means the default logger.
	Log *slog.Logger
}

// LoadOrCreate loads the CA from dir, creating it on first start, with the key stored unencrypted (mode
// 0600). Use LoadOrCreateWith to encrypt it.
func LoadOrCreate(dir string) (*CA, error) { return LoadOrCreateWith(dir, Options{}) }

// LoadOrCreateWith is LoadOrCreate with the key protection of opts.
func LoadOrCreateWith(dir string, opts Options) (*CA, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if len(opts.Passphrase) > 0 && len(opts.Passphrase) < MinPassphraseLen {
		return nil, fmt.Errorf("pki: the CA key passphrase must be at least %d characters", MinPassphraseLen)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	certPEM, cerr := os.ReadFile(certPath)
	keyPEM, kerr := os.ReadFile(keyPath)
	if cerr == nil && kerr == nil {
		ca, encrypted, err := parseCA(certPEM, keyPEM, opts.Passphrase)
		if err != nil {
			return nil, err
		}
		switch {
		case !encrypted && len(opts.Passphrase) > 0:
			// First start with a passphrase: encrypt the key that is already there, and check that it
			// opens again before it replaces the plaintext one.
			if err := writeKey(keyPath, ca.key, opts.Passphrase); err != nil {
				return nil, fmt.Errorf("pki: encrypting the existing CA key: %w", err)
			}
			log.Info("the CA private key was encrypted with the passphrase; the plaintext copy has been replaced", "file", keyPath)
		case !encrypted:
			warnUnencrypted(log, keyPath)
		}
		return ca, nil
	}
	if !errors.Is(cerr, os.ErrNotExist) && cerr != nil {
		return nil, cerr
	}
	if !errors.Is(kerr, os.ErrNotExist) && kerr != nil {
		return nil, kerr
	}
	if (cerr == nil) != (kerr == nil) {
		return nil, errors.New("pki: ca.crt and ca.key must exist together; refusing to create a second CA over one half")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Continuum internal CA", Organization: []string{"Continuum"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(caTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	if err := writeKey(keyPath, key, opts.Passphrase); err != nil {
		return nil, err
	}
	if len(opts.Passphrase) == 0 {
		warnUnencrypted(log, keyPath)
	}
	if err := writeFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, DER: der}, nil
}

func warnUnencrypted(log *slog.Logger, keyPath string) {
	log.Warn("the CA private key is stored UNENCRYPTED on disk (mode 0600). Anyone who can read this file, or a backup of it, can issue certificates that this server trusts. Set --ca-key-passphrase-file (env CONTINUUM_CA_KEY_PASSPHRASE_FILE) to encrypt it", "file", keyPath)
}

// writeKey writes the CA key, encrypted when a passphrase is given, atomically and with mode 0600. An
// encrypted key is read back and decrypted before it replaces the file, so a bug or a full disk cannot
// leave the CA unopenable.
func writeKey(path string, key *ecdsa.PrivateKey, passphrase []byte) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: plainKeyType, Bytes: der})
	if len(passphrase) > 0 {
		if data, err = EncryptKey(der, passphrase); err != nil {
			return err
		}
		blk, _ := pem.Decode(data)
		if blk == nil {
			return errors.New("pki: internal error: the encrypted key is not valid PEM")
		}
		back, err := DecryptKey(blk, passphrase)
		if err != nil || string(back) != string(der) {
			return errors.New("pki: internal error: the encrypted key does not decrypt to the original")
		}
	}
	return writeFile(path, data, 0o600)
}

// parseCA reads the certificate and key files. encrypted says whether the key file was encrypted.
func parseCA(certPEM, keyPEM, passphrase []byte) (ca *CA, encrypted bool, err error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, false, errors.New("pki: ca files are not valid PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, false, err
	}
	keyDER := kb.Bytes
	switch kb.Type {
	case encryptedKeyType:
		encrypted = true
		if keyDER, err = DecryptKey(kb, passphrase); err != nil {
			return nil, true, err
		}
	case plainKeyType:
	default:
		return nil, false, fmt.Errorf("pki: ca.key holds a %q block, which is not a CA key", kb.Type)
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return nil, encrypted, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, encrypted, errors.New("pki: ca.crt does not match ca.key")
	}
	if time.Now().After(cert.NotAfter) {
		return nil, encrypted, errors.New("pki: the CA certificate has expired")
	}
	return &CA{cert: cert, key: key, DER: cb.Bytes}, encrypted, nil
}

// writeFile writes atomically with the given mode: the data goes to a temporary file created with that
// mode (never wider, whatever the umask), is flushed to disk, and only then replaces the target.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil { // a leftover tmp file from a crash may have had other permissions
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// Pin is the SHA-256 of the CA certificate. It is printed in the install command so
// an agent can verify the server on first contact, before it has any certificate.
func (ca *CA) Pin() string { return PinOf(ca.DER) }

// PinOf returns the hex SHA-256 of a DER certificate.
func PinOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Pool returns a pool containing only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// ParseCSR validates a certificate request. Only ECDSA P-256 keys are accepted and the
// self-signature must verify (proof the agent holds the private key). Everything else in
// the request, including any subject or SANs the agent asks for, is ignored: the server
// alone decides the identity written into the certificate.
func ParseCSR(der []byte) (*x509.CertificateRequest, error) {
	if len(der) == 0 || len(der) > 4096 {
		return nil, errors.New("pki: certificate request has an invalid size")
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, fmt.Errorf("pki: malformed certificate request: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: certificate request signature is invalid: %w", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("pki: only ECDSA P-256 keys are accepted")
	}
	// Reject points that are not on the curve (ParseCertificateRequest already checks, this is defence in depth).
	if _, err := ecdh.P256().NewPublicKey(elliptic.Marshal(pub.Curve, pub.X, pub.Y)); err != nil {
		return nil, errors.New("pki: public key is not a valid P-256 point")
	}
	return csr, nil
}

// IssueAgent signs a client certificate whose identity is chosen by the server.
// Subject: CN=<agentID>, O=<orgID>. It is valid for client authentication only.
func (ca *CA) IssueAgent(csr *x509.CertificateRequest, agentID, orgID string, ttl time.Duration) (leafDER []byte, notAfter time.Time, err error) {
	if agentID == "" {
		return nil, time.Time{}, errors.New("pki: agent id required")
	}
	if ttl <= 0 || ttl > AgentCertTTL {
		ttl = AgentCertTTL
	}
	serial, err := newSerial()
	if err != nil {
		return nil, time.Time{}, err
	}
	now := time.Now()
	notAfter = now.Add(ttl)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: agentID, Organization: []string{orgID}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	leafDER, err = x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	return leafDER, notAfter, err
}

// VerifyExpiredAgent checks that der is an agent certificate signed by this CA, valid for client
// authentication, and returns it. Expiry is deliberately ignored here (the caller enforces a
// window): the certificate is only ever used as proof that this key was once issued to this agent.
func (ca *CA) VerifyExpiredAgent(der []byte) (*x509.Certificate, error) {
	if len(der) == 0 || len(der) > 4096 {
		return nil, errors.New("pki: certificate has an invalid size")
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, errors.New("pki: malformed certificate")
	}
	// Verify as of a moment inside the certificate's own validity so only the signature,
	// chain and key usage are being tested.
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CurrentTime: leaf.NotAfter.Add(-time.Second),
	})
	if err != nil {
		return nil, errors.New("pki: certificate was not issued by this server")
	}
	return leaf, nil
}

// ServerCerts serves the TLS certificate for the gRPC and enrollment listener and
// renews it before it expires, so a long-running server never needs a restart.
type ServerCerts struct {
	ca    *CA
	hosts []string

	mu   sync.Mutex
	cert *tls.Certificate
	exp  time.Time
}

// NewServerCerts prepares a provider for the given DNS names and IP addresses.
func NewServerCerts(ca *CA, hosts []string) *ServerCerts {
	return &ServerCerts{ca: ca, hosts: hosts}
}

// GetCertificate implements tls.Config.GetCertificate.
func (s *ServerCerts) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cert != nil && time.Until(s.exp) > serverTTL/3 {
		return s.cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Continuum server", Organization: []string{"Continuum"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(serverTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range s.hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.ca.cert, &key.PublicKey, s.ca.key)
	if err != nil {
		return nil, err
	}
	// The CA certificate rides along so a client that only knows the CA's pin can check it.
	s.cert = &tls.Certificate{Certificate: [][]byte{der, s.ca.DER}, PrivateKey: key}
	s.exp = tmpl.NotAfter
	return s.cert, nil
}
