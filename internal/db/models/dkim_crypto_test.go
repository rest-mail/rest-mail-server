package models

import (
	"errors"
	"strings"
	"testing"

	dkim "github.com/rest-mail/go-dkim"
	restcrypto "github.com/restmail/restmail/internal/crypto"
)

const testMasterKey = "unit-test-master-key"

// genDKIMPEM returns a fresh RSA DKIM private key in PEM form for round-trip and
// signability assertions.
func genDKIMPEM(t *testing.T) string {
	t.Helper()
	priv, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(priv), "-----BEGIN") {
		t.Fatalf("generated key is not PEM: %q", priv[:min(len(priv), 40)])
	}
	return priv
}

// TestEncryptLoadRoundTrip: a key encrypted at rest carries the version prefix,
// is not stored as plaintext, and decrypts back to the exact original PEM.
func TestEncryptLoadRoundTrip(t *testing.T) {
	pemKey := genDKIMPEM(t)

	stored, err := EncryptDKIMPrivateKey(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}
	if !strings.HasPrefix(stored, DKIMKeyCipherPrefix) {
		t.Fatalf("stored value missing version prefix %q: %q", DKIMKeyCipherPrefix, stored[:min(len(stored), 40)])
	}
	if strings.Contains(stored, "PRIVATE KEY") {
		t.Fatal("stored value appears to contain plaintext PEM")
	}
	if !DKIMKeyIsEncrypted(stored) {
		t.Fatal("DKIMKeyIsEncrypted false for an encrypted value")
	}

	loaded, err := LoadDKIMPrivateKey(stored, testMasterKey)
	if err != nil {
		t.Fatalf("LoadDKIMPrivateKey: %v", err)
	}
	if loaded != pemKey {
		t.Fatal("round-trip mismatch: loaded key != original PEM")
	}
}

// TestLoadRoundTripKeyIsSignable proves the decrypted key is still a usable DKIM
// signing key: it parses and produces a non-empty signature after the at-rest
// encrypt/decrypt cycle.
func TestLoadRoundTripKeyIsSignable(t *testing.T) {
	pemKey := genDKIMPEM(t)
	stored, err := EncryptDKIMPrivateKey(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}
	loaded, err := LoadDKIMPrivateKey(stored, testMasterKey)
	if err != nil {
		t.Fatalf("LoadDKIMPrivateKey: %v", err)
	}

	signer, err := dkim.ParsePrivateKey(loaded)
	if err != nil {
		t.Fatalf("ParsePrivateKey after round-trip: %v", err)
	}
	raw := "From: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	sig, err := dkim.Sign([]byte(raw), dkim.SignOptions{
		Domain:     "example.com",
		Selector:   "sel",
		PrivateKey: signer,
		Time:       1,
	})
	if err != nil {
		t.Fatalf("Sign after round-trip: %v", err)
	}
	if strings.TrimSpace(sig) == "" {
		t.Fatal("Sign produced an empty signature")
	}
}

// TestLoadFailsClosedWrongKey: a key encrypted under one MASTER_KEY must FAIL
// (not silently return ciphertext-as-plaintext) when loaded with another.
func TestLoadFailsClosedWrongKey(t *testing.T) {
	pemKey := genDKIMPEM(t)
	stored, err := EncryptDKIMPrivateKey(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}

	loaded, err := LoadDKIMPrivateKey(stored, "a-different-master-key")
	if err == nil {
		t.Fatal("expected fail-closed error with wrong MASTER_KEY, got nil")
	}
	if !errors.Is(err, ErrDKIMKeyUndecryptable) {
		t.Fatalf("expected ErrDKIMKeyUndecryptable, got %v", err)
	}
	if loaded != "" {
		t.Fatalf("expected empty key on fail-closed, got %q", loaded[:min(len(loaded), 20)])
	}
	// The raw ciphertext must never leak through as if it were the key.
	if strings.Contains(loaded, DKIMKeyCipherPrefix) {
		t.Fatal("fail-closed leaked the stored ciphertext")
	}
}

// TestLoadEncryptedWithoutMasterKeyFailsClosed: a versioned ciphertext cannot be
// read without any MASTER_KEY, so it must fail closed rather than pass through.
func TestLoadEncryptedWithoutMasterKeyFailsClosed(t *testing.T) {
	pemKey := genDKIMPEM(t)
	stored, err := EncryptDKIMPrivateKey(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}
	if _, err := LoadDKIMPrivateKey(stored, ""); !errors.Is(err, ErrDKIMKeyUndecryptable) {
		t.Fatalf("expected ErrDKIMKeyUndecryptable with empty MASTER_KEY, got %v", err)
	}
}

// TestLoadLegacyPlaintextPEM: a legacy plaintext key (no prefix) is returned
// as-is so signing keeps working until the migration upgrades it — a plaintext
// key is NOT rejected merely for being plaintext.
func TestLoadLegacyPlaintextPEM(t *testing.T) {
	pemKey := genDKIMPEM(t)
	loaded, err := LoadDKIMPrivateKey(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("LoadDKIMPrivateKey(plaintext): %v", err)
	}
	if loaded != pemKey {
		t.Fatal("legacy plaintext key not returned verbatim")
	}
}

// TestLoadLegacyBareBase64Ciphertext: a key encrypted before the version prefix
// existed (bare base64 from crypto.EncryptString) still decrypts, and still
// fails closed under the wrong key.
func TestLoadLegacyBareBase64Ciphertext(t *testing.T) {
	pemKey := genDKIMPEM(t)
	bare, err := restcrypto.EncryptString(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	if DKIMKeyIsEncrypted(bare) {
		t.Fatal("bare ciphertext should not carry the version prefix")
	}

	loaded, err := LoadDKIMPrivateKey(bare, testMasterKey)
	if err != nil {
		t.Fatalf("LoadDKIMPrivateKey(bare): %v", err)
	}
	if loaded != pemKey {
		t.Fatal("legacy bare ciphertext did not decrypt to the original key")
	}

	if _, err := LoadDKIMPrivateKey(bare, "wrong-key"); !errors.Is(err, ErrDKIMKeyUndecryptable) {
		t.Fatalf("expected fail-closed on legacy bare ciphertext with wrong key, got %v", err)
	}
}

// TestLoadEmptyStored: an empty stored value is an error (no key), never a
// silent success.
func TestLoadEmptyStored(t *testing.T) {
	if _, err := LoadDKIMPrivateKey("", testMasterKey); err == nil {
		t.Fatal("expected error for empty stored key")
	}
}

// TestMigratePlaintextThenIdempotent: plaintext migrates to versioned ciphertext
// once (changed=true) and a second run is a no-op (changed=false) — never
// double-encrypted, and the result still decrypts to the original key.
func TestMigratePlaintextThenIdempotent(t *testing.T) {
	pemKey := genDKIMPEM(t)

	upgraded, changed, err := MigrateDKIMKeyAtRest(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("first MigrateDKIMKeyAtRest: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true migrating a plaintext key")
	}
	if !DKIMKeyIsEncrypted(upgraded) {
		t.Fatal("migrated value is not versioned ciphertext")
	}
	if got, err := LoadDKIMPrivateKey(upgraded, testMasterKey); err != nil || got != pemKey {
		t.Fatalf("migrated key did not decrypt to original: err=%v equal=%v", err, got == pemKey)
	}

	// Second run must be a no-op (idempotent), and byte-identical (no re-encrypt).
	again, changed2, err := MigrateDKIMKeyAtRest(upgraded, testMasterKey)
	if err != nil {
		t.Fatalf("second MigrateDKIMKeyAtRest: %v", err)
	}
	if changed2 {
		t.Fatal("expected changed=false on re-running migration (idempotency)")
	}
	if again != upgraded {
		t.Fatal("re-run mutated an already-encrypted value")
	}
}

// TestMigrateBareBase64Normalizes: a legacy bare-base64 ciphertext is normalized
// to the versioned form by prefixing the SAME ciphertext (not re-encrypted), and
// the re-run is idempotent.
func TestMigrateBareBase64Normalizes(t *testing.T) {
	pemKey := genDKIMPEM(t)
	bare, err := restcrypto.EncryptString(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}

	upgraded, changed, err := MigrateDKIMKeyAtRest(bare, testMasterKey)
	if err != nil {
		t.Fatalf("MigrateDKIMKeyAtRest(bare): %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true normalizing bare ciphertext")
	}
	if upgraded != DKIMKeyCipherPrefix+bare {
		t.Fatal("normalization should prefix the same ciphertext, not re-encrypt it")
	}
	if got, err := LoadDKIMPrivateKey(upgraded, testMasterKey); err != nil || got != pemKey {
		t.Fatalf("normalized key did not decrypt to original: err=%v equal=%v", err, got == pemKey)
	}

	if _, changed2, err := MigrateDKIMKeyAtRest(upgraded, testMasterKey); err != nil || changed2 {
		t.Fatalf("re-run not idempotent: changed=%v err=%v", changed2, err)
	}
}

// TestMigrateWrongKeyBareLeavesUntouched: a bare value that fails to decrypt
// under the current key is left byte-for-byte untouched (never mangled) and
// surfaces ErrDKIMKeyUndecryptable so the caller can skip it.
func TestMigrateWrongKeyBareLeavesUntouched(t *testing.T) {
	pemKey := genDKIMPEM(t)
	bare, err := restcrypto.EncryptString(pemKey, "some-other-key")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}

	out, changed, err := MigrateDKIMKeyAtRest(bare, testMasterKey)
	if !errors.Is(err, ErrDKIMKeyUndecryptable) {
		t.Fatalf("expected ErrDKIMKeyUndecryptable, got %v", err)
	}
	if changed {
		t.Fatal("expected changed=false for an undecryptable bare value")
	}
	if out != bare {
		t.Fatal("undecryptable value was mutated")
	}
}

// TestMigrateNoMasterKeyNoOp: with no MASTER_KEY, migration is disabled and
// leaves values as-is.
func TestMigrateNoMasterKeyNoOp(t *testing.T) {
	pemKey := genDKIMPEM(t)
	out, changed, err := MigrateDKIMKeyAtRest(pemKey, "")
	if err != nil || changed || out != pemKey {
		t.Fatalf("expected no-op with empty master key: changed=%v err=%v", changed, err)
	}
}
