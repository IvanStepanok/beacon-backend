package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Time-based One-Time Password (TOTP, RFC 6238) for analyst MFA. SHA-1, 6 digits,
// 30-second period — the universal authenticator-app defaults (Google Authenticator,
// Authy, 1Password, FreeOTP). Implemented on the standard library so no third-party
// dependency enters the build. The per-user secret is stored ENCRYPTED at rest (see
// internal/crypto); only the decrypted secret is fed to these functions.

const (
	totpDigits = 6
	totpPeriod = 30 * time.Second
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a fresh base32 (unpadded) secret — 160 bits, the
// RFC 4226 recommended length — for enrollment in an authenticator app.
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPCode computes the RFC 6238 code for a base32 secret at instant t.
func TOTPCode(secretB32 string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secretB32)))
	if err != nil {
		return "", err
	}
	counter := uint64(t.Unix()) / uint64(totpPeriod.Seconds())
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	val := (uint32(sum[off]&0x7f) << 24) |
		(uint32(sum[off+1]) << 16) |
		(uint32(sum[off+2]) << 8) |
		uint32(sum[off+3])
	return fmt.Sprintf("%0*d", totpDigits, val%1_000_000), nil
}

// ValidateTOTP checks a user-entered code against the secret, accepting the current
// step plus the immediately adjacent steps (±30 s) to tolerate clock skew. The
// comparison is constant-time.
func ValidateTOTP(secretB32, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	for _, skew := range []time.Duration{0, -totpPeriod, totpPeriod} {
		want, err := TOTPCode(secretB32, now.Add(skew))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// TOTPProvisioningURI builds the otpauth:// URI an authenticator app reads from a QR
// code (issuer "Beacon", label "Beacon:<email>").
func TOTPProvisioningURI(secretB32, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", int(totpPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
