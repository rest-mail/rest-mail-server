package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP (RFC 6238) parameters for optional two-factor auth (OSI-19). These are
// the interoperable defaults every mainstream authenticator app (Google
// Authenticator, Authy, 1Password, …) expects, so a provisioned secret works
// without the user tuning anything:
//   - 30-second time step
//   - 6-digit codes
//   - SHA-1 (the RFC 6238 / de-facto authenticator default)
//   - ±1 step of clock skew accepted on verification, so a code from the
//     immediately previous or next window still passes (covers minor
//     client/server clock drift). The same skew is used everywhere a code is
//     checked, so generation and verification stay symmetric.
const (
	totpPeriodSeconds = 30
	totpDigits        = otp.DigitsSix
	totpAlgorithm     = otp.AlgorithmSHA1
	// totpSkew is the number of adjacent time steps (before AND after the
	// current one) a submitted code may come from. 1 == accept the previous,
	// current, or next 30-second window (the standard ±1 tolerance).
	totpSkew = 1

	// DefaultTOTPIssuer labels the provisioning URI shown in the authenticator
	// app. Matches the JWT issuer so an account's entry reads "restmail".
	DefaultTOTPIssuer = "restmail"

	// RecoveryCodeCount is how many one-time recovery codes are minted at
	// enrollment for use when the authenticator device is lost.
	RecoveryCodeCount = 10
)

// GenerateTOTPSecret mints a fresh random TOTP secret bound to accountName
// (e.g. the mailbox address or admin username) under issuer. The returned
// *otp.Key exposes both Secret() (the base32 secret to persist, encrypted at
// rest) and URL() (the otpauth:// provisioning URI / QR payload handed to the
// user's authenticator app).
func GenerateTOTPSecret(issuer, accountName string) (*otp.Key, error) {
	if issuer == "" {
		issuer = DefaultTOTPIssuer
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
		Period:      totpPeriodSeconds,
		Digits:      totpDigits,
		Algorithm:   totpAlgorithm,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate TOTP secret: %w", err)
	}
	return key, nil
}

// ValidateTOTPCode reports whether code is a currently-valid TOTP for secret
// (the base32 secret), accepting the standard ±1 time-step skew. secret is the
// decrypted per-account secret; the caller decrypts it from storage first.
func ValidateTOTPCode(code, secret string) bool {
	code = strings.TrimSpace(code)
	if code == "" || secret == "" {
		return false
	}
	ok, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period:    totpPeriodSeconds,
		Skew:      totpSkew,
		Digits:    totpDigits,
		Algorithm: totpAlgorithm,
	})
	if err != nil {
		return false
	}
	return ok
}

// GenerateTOTPCode returns the TOTP for secret at time t. It exists so tests
// (and any future server-side flow) can produce a valid code with exactly the
// same parameters ValidateTOTPCode enforces.
func GenerateTOTPCode(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{
		Period:    totpPeriodSeconds,
		Skew:      totpSkew,
		Digits:    totpDigits,
		Algorithm: totpAlgorithm,
	})
}

// GenerateRecoveryCodes returns RecoveryCodeCount fresh single-use recovery
// codes in human-friendly "xxxxx-xxxxx" form (10 hex chars, dash-grouped).
// These are the ONLY time the plaintext exists — the caller shows them to the
// user once and persists only their bcrypt hashes (see HashRecoveryCode).
func GenerateRecoveryCodes() ([]string, error) {
	codes := make([]string, RecoveryCodeCount)
	for i := range codes {
		b := make([]byte, 5) // 5 bytes -> 10 hex chars
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("failed to generate recovery code: %w", err)
		}
		h := hex.EncodeToString(b)
		codes[i] = h[:5] + "-" + h[5:]
	}
	return codes, nil
}

// NormalizeRecoveryCode canonicalises user-entered recovery codes so cosmetic
// differences (case, surrounding whitespace, the display dash) don't cause a
// legitimate code to miss. Both hashing and verification run through this, so a
// code typed "ABCDE-FGHIJ", "abcdefghij", or " abcde-fghij " all match.
func NormalizeRecoveryCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// HashRecoveryCode returns the bcrypt hash of a recovery code (normalized
// first), reusing the same {BLF-CRYPT} password-hash helper the rest of the
// auth layer uses, so recovery codes are stored hashed exactly like passwords.
func HashRecoveryCode(code string) (string, error) {
	return HashPassword(NormalizeRecoveryCode(code))
}

// CheckRecoveryCode reports whether a submitted recovery code matches a stored
// hash. Constant-time within bcrypt; the caller iterates the account's unused
// hashes.
func CheckRecoveryCode(code, hash string) bool {
	return CheckPassword(NormalizeRecoveryCode(code), hash) == nil
}
