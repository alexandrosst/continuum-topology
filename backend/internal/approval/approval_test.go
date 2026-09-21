package approval

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"math"
	"regexp"
	"strings"
	"testing"
)

func csrFor(t *testing.T, k *ecdsa.PrivateKey) []byte {
	t.Helper()
	if k == nil {
		k, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestNewCodeShapeAndEntropy(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	seen := map[string]bool{}
	counts := map[rune]int{}
	const n = 4000
	for i := 0; i < n; i++ {
		c, err := New()
		if err != nil || !re.MatchString(c) {
			t.Fatalf("%q %v", c, err)
		}
		if seen[c] {
			t.Fatalf("duplicate code %q in %d", c, i)
		}
		seen[c] = true
		for _, r := range strings.ReplaceAll(c, "-", "") {
			counts[r]++
		}
	}
	if len(alphabet) != 32 || math.Log2(math.Pow(float64(len(alphabet)), Length)) < 40 {
		t.Fatalf("the code must carry at least 40 bits")
	}
	// Every character of the alphabet occurs, and none is far from its fair share (no modulo bias).
	fair := float64(n*Length) / 32
	for _, r := range alphabet {
		if got := float64(counts[r]); got < fair*0.8 || got > fair*1.2 {
			t.Errorf("%c occurs %v times, fair share %v", r, got, fair)
		}
	}
	for _, bad := range "ILOU" {
		if counts[bad] != 0 {
			t.Errorf("the ambiguous character %c was printed", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"k7qm-4txd":    "K7QM4TXD",
		" K7QM 4TXD\n": "K7QM4TXD",
		"K7QM_4TXD":    "K7QM4TXD",
		"k7qm4txd":     "K7QM4TXD",
		"O0O0-1I1L":    "00001111", // ambiguous characters are read the way the alphabet has them
		"\tK7QM–4TXD ": "",         // an en dash from a word processor is not a separator
		"K7QM-4TX":     "",
		"K7QM-4TXDX":   "",
		"K7QU-4TXD":    "",
		"":             "",
		"K7QM-4TXD-Z":  "",
		"ｋ７ｑｍ-4TXD":    "", // full-width characters are refused, not folded
	} {
		got, ok := Normalize(in)
		if want == "" && ok || want != "" && (!ok || got != want) {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestHashIsBoundToTheKeyAndTheNormalisedCode(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c1, c2 := csrFor(t, k), csrFor(t, k) // same key, different signatures
	h1, err := Hash("K7QM-4TXD", c1)
	if err != nil || len(h1) != HashLen {
		t.Fatal(err, len(h1))
	}
	if h2, _ := Hash("k7qm 4txd", c2); string(h1) != string(h2) {
		t.Fatal("the hash must depend on the key and the code only, not on how the code was typed or how the request was signed")
	}
	if !Matches("k7qm-4txd", c2, h1) || Matches("K7QM-4TXE", c1, h1) || Matches("nonsense", c1, h1) || Matches("K7QM-4TXD", c1, nil) || Matches("K7QM-4TXD", c1, h1[:16]) {
		t.Fatal("Matches")
	}
	if Matches("K7QM-4TXD", csrFor(t, nil), h1) {
		t.Fatal("a hash must not verify under another key")
	}
	if _, err := Hash("bad", c1); err == nil {
		t.Fatal("a non-code must not hash")
	}
}
