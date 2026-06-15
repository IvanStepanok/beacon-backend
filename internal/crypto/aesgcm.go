// Package crypto provides authenticated symmetric encryption (AES-256-GCM) for
// Beacon's data at rest: report photos on the volume and the stored TOTP secrets.
//
// A 4-byte magic header ("BEC1") prefixes every ciphertext so readers can tell an
// encrypted blob from legacy plaintext and migrate a store in place — no flag day,
// no separate "is this encrypted?" column. The key is supplied via the
// DATA_ENCRYPTION_KEY env var (see internal/config); in prod the server refuses to
// boot without it.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// KeyLen is the required key size — AES-256.
const KeyLen = 32

// magic prefixes every encrypted blob: "BEC1" = Beacon Encrypted, format v1.
var magic = []byte{'B', 'E', 'C', '1'}

// ParseKey decodes a 32-byte key from a base64 or hex string (env-supplied).
// Returns (nil, nil) for an empty string so callers can treat "unset" as a mode,
// and a descriptive error for a present-but-wrong-length value.
func ParseKey(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, dec := range decoders {
		if b, err := dec(s); err == nil && len(b) == KeyLen {
			return b, nil
		}
	}
	return nil, fmt.Errorf("DATA_ENCRYPTION_KEY must decode (base64 or hex) to exactly %d bytes (AES-256)", KeyLen)
}

// IsEncrypted reports whether a blob carries the Beacon encryption magic header.
func IsEncrypted(blob []byte) bool {
	return len(blob) >= len(magic) && subtle.ConstantTimeCompare(blob[:len(magic)], magic) == 1
}

// Seal encrypts plaintext with AES-256-GCM. Output layout: magic || nonce || (ciphertext+tag).
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(magic)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, magic...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// Open decrypts a blob produced by Seal. Returns an error for non-Beacon input or a
// failed authentication tag (tampering / wrong key).
func Open(key, blob []byte) ([]byte, error) {
	if !IsEncrypted(blob) {
		return nil, errors.New("not a beacon-encrypted blob")
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	body := blob[len(magic):]
	ns := gcm.NonceSize()
	if len(body) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := body[:ns], body[ns:]
	return gcm.Open(nil, nonce, ct, nil)
}

// SealString encrypts a short secret (e.g. a TOTP key) to a base64 text value
// suitable for a DB text column.
func SealString(key []byte, plaintext string) (string, error) {
	b, err := Seal(key, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// OpenString reverses SealString.
func OpenString(key []byte, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	b, err := Open(key, raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
