package models

import (
	"errors"
	"strings"
	"testing"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
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

// TestLoadRefusesUnencryptedWithMasterKey: with a MASTER_KEY configured, only
// the versioned ciphertext is a key. A plaintext PEM or an unprefixed base64
// ciphertext fails closed instead of being signed with.
func TestLoadRefusesUnencryptedWithMasterKey(t *testing.T) {
	pemKey := genDKIMPEM(t)
	bare, err := restcrypto.EncryptString(pemKey, testMasterKey)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	for name, stored := range map[string]string{"plaintext PEM": pemKey, "unprefixed ciphertext": bare} {
		loaded, err := LoadDKIMPrivateKey(stored, testMasterKey)
		if !errors.Is(err, ErrDKIMKeyUndecryptable) {
			t.Errorf("%s: expected ErrDKIMKeyUndecryptable, got %v", name, err)
		}
		if loaded != "" {
			t.Errorf("%s: expected no key on fail-closed, got one", name)
		}
	}
}

// TestLoadWithoutMasterKeyReturnsPlaintext: with encryption disabled (dev) the
// key was stored as plaintext by EncryptDKIMPrivateKey and is read back as-is.
func TestLoadWithoutMasterKeyReturnsPlaintext(t *testing.T) {
	pemKey := genDKIMPEM(t)
	stored, err := EncryptDKIMPrivateKey(pemKey, "")
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}
	loaded, err := LoadDKIMPrivateKey(stored, "")
	if err != nil {
		t.Fatalf("LoadDKIMPrivateKey: %v", err)
	}
	if loaded != pemKey {
		t.Fatal("plaintext key not returned verbatim with encryption disabled")
	}
}

// TestLoadEmptyStored: an empty stored value is an error (no key), never a
// silent success.
func TestLoadEmptyStored(t *testing.T) {
	if _, err := LoadDKIMPrivateKey("", testMasterKey); err == nil {
		t.Fatal("expected error for empty stored key")
	}
}
