package pki

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// The CA key can be encrypted at rest with a passphrase. The file is a PEM block of type
// encryptedKeyType whose body is:
//
//	magic "CCK1" | version (1) | kdf (1 = argon2id) | time (u32) | memory KiB (u32) | threads (u8) |
//	salt (16) | nonce (12) | AES-256-GCM(ciphertext of the SEC 1 / DER private key) with the header as additional data
//
// Everything that decides how the key is unlocked is in the header and covered by the GCM tag, so a
// changed parameter is detected rather than silently derived differently, and the parameters can be
// raised later without invalidating keys already written. Version 1 is the only one defined; a
// reader refuses any other rather than guess.
const (
	encryptedKeyType = "CONTINUUM ENCRYPTED CA KEY"
	plainKeyType     = "EC PRIVATE KEY"

	keyMagic   = "CCK1"
	keyVersion = 1
	kdfArgon2  = 1

	headerLen = 4 + 1 + 1 + 4 + 4 + 1 + 16 + 12

	// MinPassphraseLen is the shortest passphrase accepted for a new or migrated key.
	MinPassphraseLen = 12

	// What a key file may ask of the machine that opens it (it is read from disk before it is trusted).
	maxKDFMemoryKiB = 1 << 20 // 1 GiB
	maxKDFTime      = 10
	maxKDFThreads   = 16
)

// KDF cost used when writing: 64 MiB, 3 passes, 4 lanes. Variables only so tests can use cheap ones.
var (
	kdfMemoryKiB uint32 = 64 * 1024
	kdfTime      uint32 = 3
	kdfThreads   uint8  = 4
)

// ErrPassphraseRequired means the key is encrypted and no passphrase was given.
var ErrPassphraseRequired = errors.New("pki: the CA key is encrypted; give the passphrase with --ca-key-passphrase-file (or CONTINUUM_CA_KEY_PASSPHRASE_FILE)")

// ErrWrongPassphrase means decryption failed: the passphrase is wrong or the file was altered.
var ErrWrongPassphrase = errors.New("pki: the CA key could not be decrypted: wrong passphrase, or the key file is damaged")

// EncryptKey seals the DER encoding of a private key under the passphrase and returns the PEM file.
func EncryptKey(keyDER, passphrase []byte) ([]byte, error) {
	if len(passphrase) < MinPassphraseLen {
		return nil, fmt.Errorf("pki: the CA key passphrase must be at least %d characters", MinPassphraseLen)
	}
	hdr := make([]byte, headerLen)
	copy(hdr, keyMagic)
	hdr[4], hdr[5] = keyVersion, kdfArgon2
	binary.BigEndian.PutUint32(hdr[6:], kdfTime)
	binary.BigEndian.PutUint32(hdr[10:], kdfMemoryKiB)
	hdr[14] = kdfThreads
	if _, err := rand.Read(hdr[15:]); err != nil { // salt then nonce
		return nil, err
	}
	gcm, err := gcmFor(hdr, passphrase)
	if err != nil {
		return nil, err
	}
	nonce := hdr[31:headerLen]
	body := gcm.Seal(append([]byte(nil), hdr...), nonce, keyDER, hdr)
	return pem.EncodeToMemory(&pem.Block{Type: encryptedKeyType, Bytes: body}), nil
}

// DecryptKey opens a file written by EncryptKey.
func DecryptKey(block *pem.Block, passphrase []byte) ([]byte, error) {
	b := block.Bytes
	if len(b) < headerLen+16 || string(b[:4]) != keyMagic {
		return nil, errors.New("pki: the encrypted CA key file is not in a format this version understands")
	}
	if b[4] != keyVersion || b[5] != kdfArgon2 {
		return nil, fmt.Errorf("pki: the encrypted CA key uses format version %d, KDF %d, which this version does not support", b[4], b[5])
	}
	if len(passphrase) == 0 {
		return nil, ErrPassphraseRequired
	}
	hdr := b[:headerLen]
	gcm, err := gcmFor(hdr, passphrase)
	if err != nil {
		return nil, err
	}
	out, err := gcm.Open(nil, hdr[31:headerLen], b[headerLen:], hdr)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return out, nil
}

func gcmFor(hdr, passphrase []byte) (cipher.AEAD, error) {
	t, m, p := binary.BigEndian.Uint32(hdr[6:]), binary.BigEndian.Uint32(hdr[10:]), hdr[14]
	if t == 0 || t > maxKDFTime || m < 8 || m > maxKDFMemoryKiB || p == 0 || p > maxKDFThreads {
		return nil, errors.New("pki: the encrypted CA key asks for key-derivation settings outside the accepted range")
	}
	key := argon2.IDKey(passphrase, hdr[15:31], t, m, p, 32)
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}
