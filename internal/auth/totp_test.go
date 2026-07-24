package auth

import (
	"strings"
	"testing"
	"time"
)

func newTestSecret(t *testing.T) string {
	t.Helper()
	key, err := GenerateTOTPSecret(DefaultTOTPIssuer, "user@example.test")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if key.Secret() == "" {
		t.Fatal("empty TOTP secret")
	}
	if !strings.HasPrefix(key.URL(), "otpauth://totp/") {
		t.Errorf("otpauth URL = %q, want otpauth://totp/ prefix", key.URL())
	}
	return key.Secret()
}

// TestValidateTOTPCode_AcceptsCurrent: a freshly generated code for "now" passes.
func TestValidateTOTPCode_AcceptsCurrent(t *testing.T) {
	secret := newTestSecret(t)
	code, err := GenerateTOTPCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateTOTPCode: %v", err)
	}
	if !ValidateTOTPCode(code, secret) {
		t.Errorf("current code %q rejected", code)
	}
}

// TestValidateTOTPCode_HonorsSkew: codes from the immediately previous and next
// 30s windows are accepted (±1 step), matching every mainstream authenticator.
func TestValidateTOTPCode_HonorsSkew(t *testing.T) {
	secret := newTestSecret(t)
	now := time.Now()

	prev, err := GenerateTOTPCode(secret, now.Add(-30*time.Second))
	if err != nil {
		t.Fatalf("GenerateTOTPCode(prev): %v", err)
	}
	if !ValidateTOTPCode(prev, secret) {
		t.Errorf("previous-window code %q rejected (skew ±1 not honored)", prev)
	}

	next, err := GenerateTOTPCode(secret, now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("GenerateTOTPCode(next): %v", err)
	}
	if !ValidateTOTPCode(next, secret) {
		t.Errorf("next-window code %q rejected (skew ±1 not honored)", next)
	}
}

// TestValidateTOTPCode_RejectsStale: a code from four steps ago (well outside
// the ±1 window) is rejected.
func TestValidateTOTPCode_RejectsStale(t *testing.T) {
	secret := newTestSecret(t)
	stale, err := GenerateTOTPCode(secret, time.Now().Add(-120*time.Second))
	if err != nil {
		t.Fatalf("GenerateTOTPCode(stale): %v", err)
	}
	if ValidateTOTPCode(stale, secret) {
		t.Errorf("stale code %q accepted, want rejected", stale)
	}
}

// TestValidateTOTPCode_RejectsWrongAndMalformed: a code for a different secret,
// an empty code, and a malformed code are all rejected.
func TestValidateTOTPCode_RejectsWrongAndMalformed(t *testing.T) {
	secret := newTestSecret(t)
	other := newTestSecret(t)

	otherCode, err := GenerateTOTPCode(other, time.Now())
	if err != nil {
		t.Fatalf("GenerateTOTPCode(other): %v", err)
	}
	// Astronomically unlikely two independent secrets share a current code.
	if ValidateTOTPCode(otherCode, secret) {
		t.Errorf("code for a different secret accepted")
	}
	if ValidateTOTPCode("", secret) {
		t.Error("empty code accepted")
	}
	if ValidateTOTPCode("not-a-code", secret) {
		t.Error("malformed code accepted")
	}
	if ValidateTOTPCode("123456", "") {
		t.Error("validation against empty secret accepted")
	}
}

// TestRecoveryCodes_GenerateHashCheck: recovery codes are minted in the
// expected count/shape, hash+verify round-trips, and normalization makes the
// display form, the stripped form, and case/whitespace variants all match.
func TestRecoveryCodes_GenerateHashCheck(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(codes), RecoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Errorf("duplicate recovery code %q", c)
		}
		seen[c] = true
		if !strings.Contains(c, "-") {
			t.Errorf("recovery code %q not in xxxxx-xxxxx form", c)
		}
	}

	code := codes[0]
	hash, err := HashRecoveryCode(code)
	if err != nil {
		t.Fatalf("HashRecoveryCode: %v", err)
	}
	// Display form, dash-stripped form, and an upper/whitespace variant all match.
	for _, variant := range []string{code, strings.ReplaceAll(code, "-", ""), "  " + strings.ToUpper(code) + " "} {
		if !CheckRecoveryCode(variant, hash) {
			t.Errorf("CheckRecoveryCode(%q) = false, want true", variant)
		}
	}
	// A different code does not match.
	if CheckRecoveryCode(codes[1], hash) {
		t.Errorf("a different recovery code matched the hash")
	}
}
