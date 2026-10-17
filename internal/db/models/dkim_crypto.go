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
// makes the stored form self-identifying, so no separate is_encrypted column is
// needed and there is no risk of a column/value disagreement.
const DKIMKeyCipherPrefix = "dkim:v1:"

// ErrDKIMKeyUndecryptable is returned when a stored DKIM private key cannot be
// read with the configured MASTER_KEY: it is encrypted under another key, its
// ciphertext is corrupt, or it is not in the encrypted form at all. Callers MUST
// fail closed — refuse to sign / temp-fail the message — rather than fall back
// to signing with the raw value or sending the mail unsigned.
var ErrDKIMKeyUndecryptable = errors.New("dkim: stored private key cannot be decrypted with MASTER_KEY")

// DKIMKeyIsEncrypted reports whether stored is in the versioned encrypted-at-rest
// form (carries DKIMKeyCipherPrefix).
func DKIMKeyIsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, DKIMKeyCipherPrefix)
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

// LoadDKIMPrivateKey returns the plaintext PEM for a stored DKIM private key, the
// inverse of EncryptDKIMPrivateKey:
//
//   - with a masterKey, the stored value must be the versioned ciphertext
//     (dkim:v1:…), which is decrypted. Anything else, or a decrypt failure,
//     returns ErrDKIMKeyUndecryptable so the caller fails closed and never signs.
//   - with an empty masterKey (encryption disabled, dev) the value is the
//     plaintext PEM and is returned as-is — except a versioned ciphertext, which
//     cannot be read without the key and therefore fails closed.
func LoadDKIMPrivateKey(stored, masterKey string) (string, error) {
	if stored == "" {
		return "", errors.New("dkim: no private key configured")
	}
	if masterKey == "" {
		if DKIMKeyIsEncrypted(stored) {
			return "", fmt.Errorf("%w: MASTER_KEY not configured", ErrDKIMKeyUndecryptable)
		}
		return stored, nil
	}
	if !DKIMKeyIsEncrypted(stored) {
		return "", fmt.Errorf("%w: not encrypted at rest", ErrDKIMKeyUndecryptable)
	}
	pem, err := restcrypto.DecryptString(strings.TrimPrefix(stored, DKIMKeyCipherPrefix), masterKey)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDKIMKeyUndecryptable, err)
	}
	return pem, nil
}
