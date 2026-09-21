package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cheapKDF makes key derivation fast for the duration of a test; the format is the same.
func cheapKDF(t *testing.T) {
	t.Helper()
	m, tt, p := kdfMemoryKiB, kdfTime, kdfThreads
	kdfMemoryKiB, kdfTime, kdfThreads = 64, 1, 1
	t.Cleanup(func() { kdfMemoryKiB, kdfTime, kdfThreads = m, tt, p })
}

var pass = []byte("correct horse battery staple")

type logSink struct{ buf bytes.Buffer }

func (l *logSink) logger() *slog.Logger { return slog.New(slog.NewTextHandler(&l.buf, nil)) }

func TestEncryptedKeyRoundTripsAndIsNotPlaintext(t *testing.T) {
	cheapKDF(t)
	dir := t.TempDir()
	a, err := LoadOrCreateWith(dir, Options{Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "ca.key"))
	if !strings.Contains(string(raw), encryptedKeyType) || strings.Contains(string(raw), "EC PRIVATE KEY") {
		t.Fatalf("key file is not encrypted:\n%s", raw)
	}
	st, _ := os.Stat(filepath.Join(dir, "ca.key"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	der, _ := x509.MarshalECPrivateKey(a.key)
	if bytes.Contains(raw, der) {
		t.Fatal("key material is visible in the file")
	}
	b, err := LoadOrCreateWith(dir, Options{Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	if a.Pin() != b.Pin() || !a.key.Equal(b.key) {
		t.Fatal("reload must give the same CA")
	}
	// The reloaded CA can still sign.
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := ParseCSR(csrFor(t, k))
	if _, _, err := b.IssueAgent(csr, "a1", "o1", time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestWrongPassphraseAndMissingPassphraseAreRefused(t *testing.T) {
	cheapKDF(t)
	dir := t.TempDir()
	if _, err := LoadOrCreateWith(dir, Options{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateWith(dir, Options{Passphrase: []byte("another passphrase entirely")}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	_, err := LoadOrCreateWith(dir, Options{})
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("no passphrase must refuse to start, got %v", err)
	}
	if !strings.Contains(err.Error(), "--ca-key-passphrase-file") {
		t.Fatalf("the message should say what to do: %v", err)
	}
	// Refusing must not have touched the file or created a second CA.
	if _, err := LoadOrCreateWith(dir, Options{Passphrase: pass}); err != nil {
		t.Fatalf("the key must still open: %v", err)
	}
}

func TestPlaintextKeyIsMigratedWhenAPassphraseIsFirstGiven(t *testing.T) {
	cheapKDF(t)
	dir := t.TempDir()
	var sink logSink
	a, err := LoadOrCreateWith(dir, Options{Log: sink.logger()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sink.buf.String(), "UNENCRYPTED") {
		t.Fatalf("no warning about the plaintext key: %q", sink.buf.String())
	}
	before, _ := os.ReadFile(filepath.Join(dir, "ca.key"))
	if !strings.Contains(string(before), "EC PRIVATE KEY") {
		t.Fatal("expected a plaintext key first")
	}
	sink.buf.Reset()
	b, err := LoadOrCreateWith(dir, Options{Passphrase: pass, Log: sink.logger()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sink.buf.String(), "UNENCRYPTED") {
		t.Fatal("must not warn once encrypted")
	}
	if a.Pin() != b.Pin() {
		t.Fatal("migration must keep the CA")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "ca.key"))
	if strings.Contains(string(after), "EC PRIVATE KEY") || !strings.Contains(string(after), encryptedKeyType) {
		t.Fatal("the plaintext key was not replaced")
	}
	// No leftover temporary files with key material.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), "tmp") {
			t.Fatalf("leftover file %s", e.Name())
		}
	}
	if _, err := LoadOrCreateWith(dir, Options{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
}

func TestNewCAWithAPassphraseIsBornEncrypted(t *testing.T) {
	cheapKDF(t)
	dir := t.TempDir()
	if _, err := LoadOrCreateWith(dir, Options{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "ca.key"))
	if strings.Contains(string(raw), "EC PRIVATE KEY") {
		t.Fatal("a plaintext key was written first")
	}
}

func TestShortPassphraseIsRefused(t *testing.T) {
	cheapKDF(t)
	if _, err := LoadOrCreateWith(t.TempDir(), Options{Passphrase: []byte("short")}); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("got %v", err)
	}
}

func TestTamperedEncryptedKeyIsDetected(t *testing.T) {
	cheapKDF(t)
	dir := t.TempDir()
	if _, err := LoadOrCreateWith(dir, Options{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.key")
	raw, _ := os.ReadFile(path)
	blk, _ := pem.Decode(raw)
	// Flip a bit in each region: KDF parameters (header, authenticated), salt, ciphertext, tag.
	for _, at := range []int{8, 20, headerLen + 3, len(blk.Bytes) - 1} {
		b := append([]byte(nil), blk.Bytes...)
		b[at] ^= 1
		bad := pem.EncodeToMemory(&pem.Block{Type: encryptedKeyType, Bytes: b})
		if err := os.WriteFile(path, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateWith(dir, Options{Passphrase: pass}); err == nil {
			t.Fatalf("a change at byte %d went unnoticed", at)
		}
	}
}

func TestUnsupportedVersionAndHostileKDFSettingsAreRefused(t *testing.T) {
	cheapKDF(t)
	data, _ := EncryptKey([]byte("0123456789abcdef0123456789abcdef"), pass)
	blk, _ := pem.Decode(data)
	v := append([]byte(nil), blk.Bytes...)
	v[4] = 9
	if _, err := DecryptKey(&pem.Block{Bytes: v}, pass); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version: %v", err)
	}
	// A file that asks for 4 TiB of memory must be refused before any work is done.
	h := append([]byte(nil), blk.Bytes...)
	h[10], h[11], h[12], h[13] = 0xff, 0xff, 0xff, 0xff
	if _, err := DecryptKey(&pem.Block{Bytes: h}, pass); err == nil || !strings.Contains(err.Error(), "range") {
		t.Fatalf("hostile KDF: %v", err)
	}
}

// --- public-key pins ---

func TestSPKIPinSurvivesReissuingTheCertificateButLegacyPinDoesNot(t *testing.T) {
	ca, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Same key, new certificate: what a CA renewal looks like.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "Continuum internal CA", Organization: []string{"Continuum"}},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der2, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &ca.key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	spki := ca.SPKIPin()
	if !strings.HasPrefix(spki, "sha256/") {
		t.Fatal(spki)
	}
	if !PinMatches(spki, ca.DER) || !PinMatches(spki, der2) {
		t.Fatal("the public-key pin must match both certificates")
	}
	if PinMatches(ca.Pin(), der2) {
		t.Fatal("the legacy pin is bound to the certificate")
	}
	if !PinMatches(ca.Pin(), ca.DER) {
		t.Fatal("legacy pin must keep working")
	}
	// A different key never matches.
	other, _ := LoadOrCreate(t.TempDir())
	if PinMatches(spki, other.DER) || PinMatches(ca.Pin(), other.DER) {
		t.Fatal("another CA matched")
	}
}

func TestPinFormsAreAccepted(t *testing.T) {
	ca, _ := LoadOrCreate(t.TempDir())
	c, _ := x509.ParseCertificate(ca.DER)
	spki := ca.SPKIPin()
	b64 := strings.TrimPrefix(spki, "sha256/")
	raw, _ := base64.StdEncoding.DecodeString(b64)
	for name, pin := range map[string]string{
		"legacy hex":       ca.Pin(),
		"legacy prefixed":  "sha256:" + ca.Pin(),
		"legacy upper":     strings.ToUpper(ca.Pin()),
		"legacy padded":    "  " + ca.Pin() + "\n",
		"spki base64":      spki,
		"spki unpadded":    "sha256/" + strings.TrimRight(b64, "="),
		"spki url base64":  "sha256/" + base64.URLEncoding.EncodeToString(raw),
		"spki hex":         "sha256/" + hex.EncodeToString(raw),
		"spki with spaces": " " + spki + " ",
	} {
		if !PinMatches(pin, ca.DER) {
			t.Errorf("%s (%q) was not accepted", name, pin)
		}
	}
	for name, pin := range map[string]string{
		"empty":          "",
		"garbage":        "sha256/not-a-digest",
		"short":          "sha256/" + b64[:10],
		"whole cert hex": "sha256/" + ca.Pin()[:60],
		"flipped":        "sha256/" + flip(b64),
	} {
		if PinMatches(pin, ca.DER) {
			t.Errorf("%s was accepted", name)
		}
	}
	if PinMatches(spki, []byte("not a certificate")) || PinMatches(spki, nil) {
		t.Error("a non-certificate matched")
	}
	_ = c
	// The legacy form is unchanged, which the install commands rely on.
	if NormalizePin("sha256:ABCD") != "abcd" || NormalizePin(spki) != spki {
		t.Errorf("NormalizePin changed: %q %q", NormalizePin("sha256:ABCD"), NormalizePin(spki))
	}
}

func flip(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

func TestClientTLSAcceptsEitherPinForm(t *testing.T) {
	ca, _ := LoadOrCreate(t.TempDir())
	sc := NewServerCerts(ca, []string{"studio.example.com"})
	leaf, err := sc.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, pin := range map[string]string{"legacy": ca.Pin(), "spki": ca.SPKIPin()} {
		v := ClientTLS(pin, "studio.example.com", nil).VerifyPeerCertificate
		if err := v(leaf.Certificate, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if err := ClientTLS(pin, "other.example.com", nil).VerifyPeerCertificate(leaf.Certificate, nil); err == nil {
			t.Errorf("%s: wrong host accepted", name)
		}
	}
	other, _ := LoadOrCreate(t.TempDir())
	if err := ClientTLS(other.SPKIPin(), "studio.example.com", nil).VerifyPeerCertificate(leaf.Certificate, nil); err == nil {
		t.Error("another CA's public-key pin accepted")
	}
}
