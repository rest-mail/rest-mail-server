package models

import (
	"errors"
	"fmt"
	"strings"

	restcrypto "github.com/restmail/restmail/internal/crypto"
)

// DKIMKeyCipherPrefix marks a DKIM private key value that is encrypted at rest
// with the MASTER_KEY-derived key (OSI-8). The remainder after the prefix is the
// base64 ciphertext produced by crypto.EncryptString (AES-256-GCM). The prefix
// makes the stored form self-identifying so callers can tell an encrypted key
// (which MUST decrypt before use) apart from a legacy plaintext PEM (which must
// be migrated) without guessing — no separate is_encrypted column is needed, and
// there is no risk of a column/value disagreement. base64 output never begins
// with a colon or a dash, so the prefix can never collide with a plaintext PEM
// ("-----BEGIN…") or with a legacy bare-base64 ciphertext.
const DKIMKeyCipherPrefix = "dkim:v1:"

// ErrDKIMKeyUndecryptable is returned when a stored DKIM private key is
// encrypted at rest but cannot be decrypted with the configured MASTER_KEY
// (wrong/missing key, or corrupted ciphertext). Callers MUST fail closed —
// refuse to sign / temp-fail the message — rather than fall back to signing with
// the raw ciphertext or sending the mail unsigned.
var ErrDKIMKeyUndecryptable = errors.New("dkim: stored private key cannot be decrypted with MASTER_KEY")

// DKIMKeyIsEncrypted reports whether stored is in the versioned encrypted-at-rest
// form (carries DKIMKeyCipherPrefix).
func DKIMKeyIsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, DKIMKeyCipherPrefix)
}

// dkimKeyLooksLikePEM reports whether stored is an unencrypted PEM block (a
// legacy plaintext-at-rest key). base64 ciphertext never begins with '-', so a
// leading "-----BEGIN" unambiguously means plaintext.
func dkimKeyLooksLikePEM(stored string) bool {
	return strings.HasPrefix(strings.TrimSpace(stored), "-----BEGIN")
}

// EncryptDKIMPrivateKey returns the at-rest form of a plaintext DKIM private key
// PEM: encrypted with the MASTER_KEY-derived key and tagged with
// DKIMKeyCipherPrefix. With an empty masterKey (encryption disabled, e.g. a dev
// stack) the plaintext is returned unchanged — production refuses to boot without
// MASTER_KEY (see config.Load), so unencrypted-at-rest is a dev-only path.
func EncryptDKIMPrivateKey(pemPlaintext, masterKey string) (string, error) {
	if masterKey == "" {
		return pemPlaintext, nil
	}
	enc, err := restcrypto.EncryptString(pemPlaintext, masterKey)
	if err != nil {
		return "", err
	}
	return DKIMKeyCipherPrefix + enc, nil
}

// LoadDKIMPrivateKey returns the plaintext PEM for a stored DKIM private key,
// choosing fail-closed vs. plaintext-compat purely from the stored form:
//
//   - versioned ciphertext (dkim:v1:…): decrypted; a decrypt failure returns
//     ErrDKIMKeyUndecryptable so the caller fails closed and never signs.
//   - legacy plaintext PEM (-----BEGIN…): returned as-is. A key is not rejected
//     merely for being plaintext — the startup migration upgrades it to
//     ciphertext, and signing must keep working in the meantime.
//   - legacy bare-base64 ciphertext (written before the version prefix existed):
//     decrypted; a decrypt failure returns ErrDKIMKeyUndecryptable — it is
//     ciphertext that SHOULD decrypt, so a failure means a wrong/missing key.
//
// With an empty masterKey the value is returned as-is, EXCEPT a versioned
// ciphertext, which cannot be read without the key and therefore fails closed.
func LoadDKIMPrivateKey(stored, masterKey string) (string, error) {
	if stored == "" {
		return "", errors.New("dkim: no private key configured")
	}
	if DKIMKeyIsEncrypted(stored) {
		if masterKey == "" {
			return "", fmt.Errorf("%w: MASTER_KEY not configured", ErrDKIMKeyUndecryptable)
		}
		pem, err := restcrypto.DecryptString(strings.TrimPrefix(stored, DKIMKeyCipherPrefix), masterKey)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrDKIMKeyUndecryptable, err)
		}
		return pem, nil
	}
	if masterKey == "" {
		// Encryption disabled (dev): keys are stored as plaintext PEM.
		return stored, nil
	}
	if dkimKeyLooksLikePEM(stored) {
		return stored, nil
	}
	// Legacy bare-base64 ciphertext (pre-version-prefix, already encrypted).
	pem, err := restcrypto.DecryptString(stored, masterKey)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDKIMKeyUndecryptable, err)
	}
	return pem, nil
}

// MigrateDKIMKeyAtRest returns the encrypted-at-rest form of a stored DKIM
// private key and reports whether the value changed. It is idempotent and never
// double-encrypts:
//
//   - already-versioned ciphertext (dkim:v1:…): unchanged (changed=false), so a
//     re-run is a no-op.
//   - plaintext PEM: encrypted and version-tagged (changed=true).
//   - legacy bare-base64 ciphertext that decrypts under masterKey: normalized to
//     the versioned form by prefixing the SAME ciphertext (changed=true), not
//     re-encrypted.
//   - a bare value that fails to decrypt: returns ErrDKIMKeyUndecryptable so the
//     caller can skip it — a wrong-MASTER_KEY row is left byte-for-byte
//     untouched rather than mangled.
//
// A no-op (changed=false, nil error) when stored or masterKey is empty.
func MigrateDKIMKeyAtRest(stored, masterKey string) (string, bool, error) {
	if stored == "" || masterKey == "" {
		return stored, false, nil
	}
	if DKIMKeyIsEncrypted(stored) {
		return stored, false, nil
	}
	if dkimKeyLooksLikePEM(stored) {
		enc, err := EncryptDKIMPrivateKey(stored, masterKey)
		if err != nil {
			return stored, false, err
		}
		return enc, true, nil
	}
	// Legacy bare-base64 ciphertext: confirm it decrypts under the current key
	// before adopting it, then normalize by prefixing (no re-encryption).
	if _, err := restcrypto.DecryptString(stored, masterKey); err != nil {
		return stored, false, fmt.Errorf("%w: %v", ErrDKIMKeyUndecryptable, err)
	}
	return DKIMKeyCipherPrefix + stored, true, nil
}
