package pki

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// autoPassphraseFile lives inside the same directory as ca.crt and ca.key (see LoadOrCreateWith) and
// holds a passphrase this server minted for itself - see AutoPassphrase. A name distinct from either so
// nothing confuses it for key or certificate material lying around in the same directory listing.
const autoPassphraseFile = "ca.passphrase"

// AutoPassphrase returns the passphrase that should encrypt the CA key in dir (the same directory
// LoadOrCreateWith is given) when the operator wants encryption at rest but has not supplied their own
// passphrase with --ca-key-passphrase-file. On first call it mints 32 random bytes from crypto/rand and
// encodes them URL-safe base64, unpadded - the exact generator and encoding every other secret this
// codebase hands out already uses (see server.newSecret) - rather than lean on a template engine's
// randomness, which carries no such guarantee. The result is written next to ca.crt/ca.key, mode 0600,
// so a restart opens the same encrypted key instead of minting a fresh passphrase that cannot read it;
// a later call simply reads that file back.
//
// This keeps the passphrase in the same place as the key it protects, which trades away the separation
// ROTATION.md recommends (back up the passphrase somewhere other than the data directory) for needing no
// Kubernetes permissions at all. The alternative - this process creating or updating a Secret object
// itself - needs API access the continuum-server chart deliberately never grants it (see
// templates/serviceaccount.yaml: "the server never talks to the Kubernetes API"). An operator who wants
// that separation back supplies their own passphrase instead (--ca-key-passphrase-file, or
// pki.caKeyPassphraseSecret.name in the chart); AutoPassphrase is never consulted when that is given.
func AutoPassphrase(dir string) ([]byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, autoPassphraseFile)
	if b, err := os.ReadFile(path); err == nil {
		b = []byte(strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"))
		if len(b) < MinPassphraseLen {
			return nil, fmt.Errorf("pki: %s holds a passphrase shorter than %d characters; it may be damaged", path, MinPassphraseLen)
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	passphrase := []byte(base64.RawURLEncoding.EncodeToString(raw))
	if err := writeFile(path, passphrase, 0o600); err != nil {
		return nil, err
	}
	return passphrase, nil
}
