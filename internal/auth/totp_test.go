package auth

import (
	"testing"
	"time"
)

// TestTOTPKnownAnswers checks the implementation against the RFC 6238 Appendix B
// SHA-1 test vectors (secret = ASCII "12345678901234567890"), truncated to 6 digits.
func TestTOTPKnownAnswers(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		got, err := TOTPCode(secret, time.Unix(c.unix, 0).UTC())
		if err != nil {
			t.Fatalf("unix %d: %v", c.unix, err)
		}
		if got != c.want {
			t.Errorf("unix %d: got %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestValidateTOTPWindow(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	code, _ := TOTPCode(secret, now)
	if !ValidateTOTP(secret, code, now) {
		t.Fatal("the current code must validate")
	}
	if !ValidateTOTP(secret, code, now.Add(25*time.Second)) {
		t.Fatal("the code must still validate within the ±1-step skew window")
	}
	if ValidateTOTP(secret, code, now.Add(5*time.Minute)) {
		t.Fatal("a stale code well outside the window must NOT validate")
	}
	if ValidateTOTP(secret, "12", now) {
		t.Fatal("a malformed (non-6-digit) code must not validate")
	}
}

func TestGenerateTOTPSecretIsRandom(t *testing.T) {
	a, _ := GenerateTOTPSecret()
	b, _ := GenerateTOTPSecret()
	if a == b || len(a) < 16 {
		t.Fatalf("secrets must be random and non-trivial: %q vs %q", a, b)
	}
}
