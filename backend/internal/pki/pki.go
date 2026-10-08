// Package pki is the server's internal certificate authority.
//
// Trust model: agents never hold a long-lived credential. They generate their own
// ECDSA P-256 key, send a CSR, and receive a certificate that lives for 24 hours.
// The CA private key never leaves the server's data directory (mode 0600).
package pki

import (
	"crypto"
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
// a new enrollment token (for example after a cluster outage, a node reboot or a cluster that was
// switched off for a holiday). Long on purpose: revocation, not expiry, is the control, and a rejoin
// still needs the agent to be approved and not revoked.
const RejoinWindow = 60 * 24 * time.Hour

const (
	serverTTL = 30 * 24 * time.Hour
	caTTL     = 10 * 365 * 24 * time.Hour
	// caRenewBefore is how close to its end a CA certificate is re-signed (with the same key) when the
	// server starts. A server that restarts at least once in nine years never reaches the end of its CA.
	caRenewBefore = 365 * 24 * time.Hour
	// clockSkew backdates certificates so a slightly slow agent clock still works.
	clockSkew = 5 * time.Minute
)

// CA signs server and agent certificates.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	DER  []byte
	// passphrase and log are what the org CA was opened with (a per-operator CA inherits them): the
	// passphrase seals the key of every per-operator CA minted from the org CA, so those keys are
	// protected exactly like the org CA key - see NewOperatorCA.
	passphrase []byte
	log        *slog.Logger
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
		ca.passphrase, ca.log = opts.Passphrase, log
		if left := time.Until(ca.cert.NotAfter); left < caRenewBefore {
			if err := ca.reissueSelf(certPath); err != nil {
				return nil, fmt.Errorf("pki: renewing the CA certificate: %w", err)
			}
			log.Info("the CA certificate had little time left and was re-signed with the same key; its public-key pin (sha256/...) is unchanged", "was_valid_until", ca.cert.NotAfter)
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
	return &CA{cert: cert, key: key, DER: der, passphrase: opts.Passphrase, log: log}, nil
}

func warnUnencrypted(log *slog.Logger, keyPath string) {
	log.Warn("the CA private key is stored UNENCRYPTED on disk (mode 0600). Anyone who can read this file, or a backup of it, can issue certificates that this server trusts. Set --ca-key-passphrase-file (env CONTINUUM_CA_KEY_PASSPHRASE_FILE) to encrypt it", "file", keyPath)
}

// writeKey writes the CA key, encrypted when a passphrase is given, atomically and with mode 0600. An
// encrypted key is read back and decrypted before it replaces the file, so a bug or a full disk cannot
// leave the CA unopenable.
func writeKey(path string, key *ecdsa.PrivateKey, passphrase []byte) error {
	data, err := sealKey(key, passphrase)
	if err != nil {
		return err
	}
	return writeFile(path, data, 0o600)
}

// sealKey is the PEM form a CA key is kept in: encrypted under the passphrase (and read back to prove it
// opens) when one is given, a plain "EC PRIVATE KEY" block when not. The org CA's key file and every
// per-operator CA key stored in the database both come from here, so neither can be protected more
// weakly than the other.
func sealKey(key *ecdsa.PrivateKey, passphrase []byte) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: plainKeyType, Bytes: der})
	if len(passphrase) > 0 {
		if data, err = EncryptKey(der, passphrase); err != nil {
			return nil, err
		}
		blk, _ := pem.Decode(data)
		if blk == nil {
			return nil, errors.New("pki: internal error: the encrypted key is not valid PEM")
		}
		back, err := DecryptKey(blk, passphrase)
		if err != nil || string(back) != string(der) {
			return nil, errors.New("pki: internal error: the encrypted key does not decrypt to the original")
		}
	}
	return data, nil
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
	return &CA{cert: cert, key: key, DER: cb.Bytes}, encrypted, nil
}

// reissueSelf signs a new self-signed certificate for the same key and subject, valid for caTTL from now,
// and replaces ca.crt with it. Same key and same subject mean everything the old certificate signed still
// verifies against the new one, and the public-key pin (SPKIPin) every agent and sidecar holds is unchanged;
// only the legacy whole-certificate pin changes, which is why install commands print the public-key pin.
func (ca *CA) reissueSelf(certPath string) error {
	serial, err := newSerial()
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               ca.cert.Subject,
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(caTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &ca.key.PublicKey, ca.key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	if err := writeFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	ca.cert, ca.DER = cert, der
	return nil
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
	// Reject points that are not on the curve (ParseCertificateRequest already checks, this is defence in
	// depth). PublicKey.ECDH does the same marshal-and-validate internally as the old elliptic.Marshal(pub.Curve,
	// pub.X, pub.Y) call this replaced, without touching the deprecated raw-coordinate accessors.
	if _, err := pub.ECDH(); err != nil {
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

// OperatorTLSTTL is how long a regional operator's receiver certificate, and the client certificate its
// senders present to it, are valid. They renew themselves when OperatorRenewBefore is left (see internal/certrenew), so
// the lifetime is short on purpose: a holder that stops being entitled (an agent that was revoked, a cluster taken off
// the operator) loses its access within this time without anyone doing anything.
var OperatorTLSTTL = 30 * 24 * time.Hour

// LegacyOperatorTLSTTL is what OperatorTLSTTL was before certificates renewed themselves. Operators installed back then
// still hold certificates of this length; it is used to work out when those end.
const LegacyOperatorTLSTTL = 365 * 24 * time.Hour

const (
	// OperatorRenewBefore is how much life an operator certificate has left when its holder renews it: a third
	// of the way through its TTL, so a renewer that is down for a couple of weeks still has time to recover.
	OperatorRenewBefore = 20 * 24 * time.Hour
	// OperatorRenewGrace is how long after expiry the server still renews an operator certificate on proof of
	// the old key (a cluster that was off, or a renewer that crashed). Past it the holder must be given a new
	// certificate by hand ("install again").
	OperatorRenewGrace = 14 * 24 * time.Hour
)

// CertPEM returns this CA's own certificate, PEM-encoded. Unlike Pool (used to verify a presented
// certificate in-process) or Pin (a fingerprint an agent checks against before it has any certificate of
// its own), this hands over the actual certificate bytes to something that never talks to this server at
// all - a regional operator's receiver (client_ca_file, to verify an exporting cluster's client
// certificate) and that cluster's own exporter (ca_file, to verify the operator's server certificate)
// both need a copy of this on disk, not just a hash to compare against.
func (ca *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.DER})
}

// SignLeaf signs a leaf certificate for a public key someone else holds the private half of: the CSR path an
// agent uses, generalised to any subject, usage and hosts. The identity in the certificate is whatever the
// caller passes, never what the key holder asked for. The TTL is capped to what is left of this CA, because a
// leaf valid past its issuer verifies nowhere.
func (ca *CA) SignLeaf(pub crypto.PublicKey, subject pkix.Name, ttl time.Duration, eku x509.ExtKeyUsage, hosts []string) ([]byte, error) {
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if left := ca.cert.NotAfter.Sub(now); left <= 0 {
		return nil, errors.New("pki: the CA certificate has expired")
	} else if ttl > left {
		ttl = left
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(ttl),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{eku},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
}

// issueLeaf generates a key here and signs a leaf for it with SignLeaf. It is the bootstrap path only: the
// first certificate an operator's receiver and each sender get is pasted into a Secret by hand, so there is
// no process yet that could generate a key and send a CSR. Every renewal after that generates its key where
// it is used.
func (ca *CA) issueLeaf(subject pkix.Name, ttl time.Duration, eku x509.ExtKeyUsage, hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := ca.SignLeaf(&key.PublicKey, subject, ttl, eku, hosts)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// IssueOperatorReceiverTLS mints a server certificate for a regional operator's own OTLP receiver, valid
// for the given hosts (the Service DNS name(s) or address it will actually be reached at). See issueLeaf
// for why the key is generated here rather than received as a CSR. Returned once, like the receiver's
// bearer token - nothing here is stored server-side beyond what CreateOperator already keeps.
func (ca *CA) IssueOperatorReceiverTLS(operatorID, orgID string, hosts []string) (certPEM, keyPEM []byte, err error) {
	if operatorID == "" {
		return nil, nil, errors.New("pki: operator id required")
	}
	subject := pkix.Name{CommonName: operatorID, Organization: []string{orgID}}
	return ca.issueLeaf(subject, OperatorTLSTTL, x509.ExtKeyUsageServerAuth, hosts)
}

// IssueOperatorClientTLS mints the client certificate ONE sender presents to that operator's receiver, so its mTLS
// check has something real, signed by the operator's own CA, to verify. The sender (the cluster, or the operator, that
// will hold the key) is named in the certificate's CN, `<operator>-export-<sender>`, so a receiver or an audit can tell
// two senders apart and the server can say which certificate went to whom. The receiver trusts the CA, not a CN, so
// this changes nothing about who is let in; what it gives is attribution and a per-sender record (see store.OperatorCert).
// An empty sender is the operator-level identity, `<operator>-export`.
func (ca *CA) IssueOperatorClientTLS(operatorID, orgID, sender string) (certPEM, keyPEM []byte, err error) {
	if operatorID == "" {
		return nil, nil, errors.New("pki: operator id required")
	}
	subject := pkix.Name{CommonName: OperatorClientCN(operatorID, sender), Organization: []string{orgID}}
	return ca.issueLeaf(subject, OperatorTLSTTL, x509.ExtKeyUsageClientAuth, nil)
}

// OperatorClientCN is the common name of the client certificate sender holds for operatorID. The server uses it
// the other way round at renewal: a certificate whose name is not the one a current sender of that operator would
// be given is not renewed.
func OperatorClientCN(operatorID, sender string) string {
	cn := operatorID + "-export"
	if sender != "" {
		cn += "-" + senderLabel(sender, maxCommonName-len(cn)-1)
	}
	return cn
}

// maxCommonName is the longest CN X.509 allows (RFC 5280 ub-common-name).
const maxCommonName = 64

// senderLabel makes a sender's name safe and short enough for a CN: letters, digits, dot, underscore and hyphen only,
// and at most max characters. A name that had to be changed to fit keeps a short hash of the original at its end, so two
// different senders never share a label.
func senderLabel(sender string, max int) string {
	clean := make([]byte, 0, len(sender))
	changed := false
	for i := 0; i < len(sender); i++ {
		c := sender[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			clean = append(clean, c)
		default:
			clean = append(clean, '-')
			changed = true
		}
	}
	if !changed && len(clean) <= max {
		return string(clean)
	}
	sum := sha256.Sum256([]byte(sender))
	tag := "-" + hex.EncodeToString(sum[:3])
	if len(clean) > max-len(tag) {
		clean = clean[:max-len(tag)]
	}
	return string(clean) + tag
}

// OperatorCATTL is how long a per-operator CA certificate is valid: ten years, as long as the org CA's, so it
// outlives every renewal and nothing a sender or a receiver holds as "the CA" ever has to be replaced in
// normal operation. Operators created before this was ten years keep their five-year CA; when one of those is
// about to expire the operator must be recreated. A variable only so tests can shorten it.
var OperatorCATTL = 10 * 365 * 24 * time.Hour

// NewOperatorCA mints a private issuing CA for one regional operator: a fresh ECDSA P-256 key and a SELF-SIGNED
// certificate (CN "Continuum operator CA <operatorID>", O orgID), NOT chained to this CA. Being its own root is
// the point: a receiver that trusts only this certificate rejects every certificate this org CA, or any other
// operator's CA, ever signed. MaxPathLen 0 lets it sign leaves and nothing else.
//
// It returns an issuer (usable at once: it signs the operator's receiver certificate and its clients'
// certificates), the certificate PEM, and the key PEM sealed exactly as the org CA key is (sealKey: argon2id +
// AES-256-GCM under the same passphrase when one is configured, otherwise a plain key block and the same warning).
// The caller stores certPEM and keyPEM; keyPEM is the only copy of the key and OpenOperatorCA is the only way back.
func (ca *CA) NewOperatorCA(operatorID, orgID string) (issuer *CA, certPEM, keyPEM []byte, err error) {
	if operatorID == "" {
		return nil, nil, nil, errors.New("pki: operator id required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Continuum operator CA " + operatorID, Organization: []string{orgID}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(OperatorCATTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	if keyPEM, err = sealKey(key, ca.passphrase); err != nil {
		return nil, nil, nil, err
	}
	if len(ca.passphrase) == 0 {
		log := ca.log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("a per-operator CA private key is stored UNENCRYPTED in the database, like the org CA key. Set --ca-key-passphrase-file (env CONTINUUM_CA_KEY_PASSPHRASE_FILE) to encrypt operator CA keys at rest", "operator", operatorID)
	}
	issuer = &CA{cert: cert, key: key, DER: der, passphrase: ca.passphrase, log: ca.log}
	return issuer, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

// OpenOperatorCA loads a per-operator CA written by NewOperatorCA, decrypting its key with the passphrase this
// (org) CA was opened with. It refuses a certificate that does not match the key and one that has expired.
func (ca *CA) OpenOperatorCA(certPEM, keyPEM []byte) (*CA, error) {
	op, _, err := parseCA(certPEM, keyPEM, ca.passphrase)
	if err != nil {
		return nil, err
	}
	if time.Now().After(op.cert.NotAfter) {
		return nil, errors.New("pki: the CA certificate has expired")
	}
	op.passphrase, op.log = ca.passphrase, ca.log
	return op, nil
}

// VerifyExpired checks that der is a certificate signed by this CA, valid for the given usage, and returns it.
// Expiry is deliberately ignored here (the caller enforces its own window): the certificate is only ever used
// as proof that this key was once issued to this holder.
func (ca *CA) VerifyExpired(der []byte, eku x509.ExtKeyUsage) (*x509.Certificate, error) {
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
		Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{eku},
		CurrentTime: leaf.NotAfter.Add(-time.Second),
	})
	if err != nil {
		return nil, errors.New("pki: certificate was not issued by this server")
	}
	return leaf, nil
}

// VerifyExpiredAgent is VerifyExpired for an agent's client certificate.
func (ca *CA) VerifyExpiredAgent(der []byte) (*x509.Certificate, error) {
	return ca.VerifyExpired(der, x509.ExtKeyUsageClientAuth)
}

// renewContext is mixed into what a renewal proof signs, so a signature made for this purpose can never be
// replayed as any other signature by the same key.
const renewContext = "continuum telemetry certificate renewal v1\x00"

// ProveRenewal is what the holder of a certificate's private key sends to renew it: an ECDSA signature,
// by that key, over the new certificate request. Together with the old certificate it shows the request comes
// from whoever holds the old key, and binds the proof to this one request so it cannot be reused for another.
func ProveRenewal(oldKey *ecdsa.PrivateKey, csrDER []byte) ([]byte, error) {
	sum := sha256.Sum256(append([]byte(renewContext), csrDER...))
	return ecdsa.SignASN1(rand.Reader, oldKey, sum[:])
}

// VerifyRenewalProof checks a proof made by ProveRenewal against the old certificate's public key.
func VerifyRenewalProof(old *x509.Certificate, csrDER, proof []byte) error {
	pub, ok := old.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("pki: the certificate does not hold an ECDSA key")
	}
	sum := sha256.Sum256(append([]byte(renewContext), csrDER...))
	if !ecdsa.VerifyASN1(pub, sum[:], proof) {
		return errors.New("pki: the renewal proof does not match the certificate's key")
	}
	return nil
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

// NotAfter is when the first certificate in a PEM block stops being valid: what the server records about a
// certificate it issues and then forgets (the private key and the certificate itself are shown once, never kept).
func NotAfter(certPEM []byte) (time.Time, error) {
	c, err := ParseCertificate(certPEM)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}

// ParseCertificate decodes the first PEM certificate in certPEM.
func ParseCertificate(certPEM []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return nil, errors.New("pki: not a PEM certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}
