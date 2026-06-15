package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func testKey() []byte {
	k := make([]byte, KeyLen)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey()
	pt := []byte("a damaged building photo's raw bytes")
	blob, err := Seal(key, pt)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(blob) {
		t.Fatal("Seal output must carry the magic header")
	}
	if bytes.Contains(blob, pt) {
		t.Fatal("plaintext must not appear in the ciphertext")
	}
	got, err := Open(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

func TestSealNonceIsRandom(t *testing.T) {
	key := testKey()
	a, _ := Seal(key, []byte("same plaintext"))
	b, _ := Seal(key, []byte("same plaintext"))
	if bytes.Equal(a, b) {
		t.Fatal("two Seals of identical plaintext must differ (random nonce)")
	}
}

func TestOpenTamperFails(t *testing.T) {
	key := testKey()
	blob, _ := Seal(key, []byte("secret"))
	blob[len(blob)-1] ^= 0xff // flip an auth-tag byte
	if _, err := Open(key, blob); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	blob, _ := Seal(testKey(), []byte("secret"))
	other := make([]byte, KeyLen)
	other[0] = 0x99
	if _, err := Open(other, blob); err == nil {
		t.Fatal("a different key must fail to open")
	}
}

func TestIsEncryptedRejectsPlaintext(t *testing.T) {
	if IsEncrypted([]byte{0xFF, 0xD8, 0xFF, 'p', 'l', 'a', 'i', 'n'}) {
		t.Fatal("a plaintext JPEG must not be detected as encrypted")
	}
	if IsEncrypted([]byte("BE")) {
		t.Fatal("a too-short buffer must not be detected as encrypted")
	}
}

func TestParseKey(t *testing.T) {
	raw := testKey()
	if k, err := ParseKey(base64.StdEncoding.EncodeToString(raw)); err != nil || len(k) != KeyLen {
		t.Fatalf("base64: err=%v len=%d", err, len(k))
	}
	if k, err := ParseKey(hex.EncodeToString(raw)); err != nil || len(k) != KeyLen {
		t.Fatalf("hex: err=%v len=%d", err, len(k))
	}
	if k, err := ParseKey(""); err != nil || k != nil {
		t.Fatalf("empty must be (nil,nil), got len=%d err=%v", len(k), err)
	}
	if _, err := ParseKey("not-32-bytes"); err == nil {
		t.Fatal("a wrong-length key must error")
	}
}

func TestSealStringRoundTrip(t *testing.T) {
	key := testKey()
	enc, err := SealString(key, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenString(key, enc)
	if err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("round-trip: err=%v got=%q", err, got)
	}
}
