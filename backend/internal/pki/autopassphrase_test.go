package pki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutoPassphraseIsHighEntropy checks that a freshly minted passphrase comes from the same
// generator and encoding every other secret in this codebase uses (32 random bytes, URL-safe base64,
// unpadded - see server.newSecret) rather than from a character-class-limited template helper: 43
// characters long, drawn from the base64 URL alphabet, and different every time it is minted fresh.
func TestAutoPassphraseIsHighEntropy(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		b, err := AutoPassphrase(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 43 {
			t.Fatalf("passphrase length = %d, want 43 (32 random bytes, base64 raw URL encoding)", len(b))
		}
		if strings.Trim(string(b), alphabet) != "" {
			t.Fatalf("passphrase %q contains characters outside the base64 URL alphabet", b)
		}
		if seen[string(b)] {
			t.Fatalf("minted the same passphrase twice: %q", b)
		}
		seen[string(b)] = true
	}
}

// TestAutoPassphrasePersistsAcrossRestarts proves the point of writing it to disk at all: a second call
// for the same directory (a restart) returns the exact bytes the first call minted, written next to
// ca.crt/ca.key at mode 0600, and those bytes can still open a CA key that was encrypted under them -
// the scenario a real server restart depends on (see internal/pki/ROTATION.md: a new passphrase on
// every start would brick the server the moment it can no longer open its own key).
func TestAutoPassphrasePersistsAcrossRestarts(t *testing.T) {
	cheapKDF(t)
	dir := t.TempDir()

	first, err := AutoPassphrase(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, autoPassphraseFile))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}

	// First boot: the server encrypts the CA key under the minted passphrase.
	ca, err := LoadOrCreateWith(dir, Options{Passphrase: first})
	if err != nil {
		t.Fatal(err)
	}

	// Restart: AutoPassphrase must hand back the same bytes, not mint a new ones.
	second, err := AutoPassphrase(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("passphrase changed across restarts: %q != %q", first, second)
	}

	// ... and that's what actually matters: the restarted server can still open its own CA key with it.
	reopened, err := LoadOrCreateWith(dir, Options{Passphrase: second})
	if err != nil {
		t.Fatalf("the persisted passphrase could not reopen the CA key: %v", err)
	}
	if ca.Pin() != reopened.Pin() || !ca.key.Equal(reopened.key) {
		t.Fatal("reload must give the same CA")
	}
}

// TestAutoPassphraseRefusesADamagedFile guards against silently encrypting (or trying to decrypt) under
// a truncated leftover, the same defensive posture readPassphraseFile/EncryptKey already take elsewhere.
func TestAutoPassphraseRefusesADamagedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, autoPassphraseFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AutoPassphrase(dir); err == nil || !strings.Contains(err.Error(), "shorter than") {
		t.Fatalf("got %v", err)
	}
}
