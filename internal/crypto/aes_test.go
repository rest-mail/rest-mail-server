package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"
)

const testKey = "test-master-key-for-unit-tests"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plaintext := []byte("Hello, AES-256-GCM!")

	ciphertext, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Ciphertext must be longer than plaintext (12-byte nonce + 16-byte GCM tag)
	if len(ciphertext) <= len(plaintext) {
		t.Fatalf("ciphertext too short: got %d bytes, want > %d", len(ciphertext), len(plaintext))
	}

	decrypted, err := Decrypt(ciphertext, testKey)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	plaintext := []byte("secret data")

	ciphertext, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	_, err = Decrypt(ciphertext, "wrong-key")
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key, got nil")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	plaintext := []byte("important data")

	ciphertext, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Flip a byte in the ciphertext portion (after the 12-byte nonce)
	if len(ciphertext) > 12 {
		ciphertext[len(ciphertext)-1] ^= 0xff
	}

	_, err = Decrypt(ciphertext, testKey)
	if err == nil {
		t.Fatal("expected error when decrypting tampered ciphertext, got nil")
	}
}

func TestDecryptTooShort(t *testing.T) {
	_, err := Decrypt([]byte("short"), testKey)
	if err == nil {
		t.Fatal("expected error for ciphertext shorter than nonce, got nil")
	}
}

func TestEncryptDecryptEmptyPlaintext(t *testing.T) {
	ciphertext, err := Encrypt([]byte{}, testKey)
	if err != nil {
		t.Fatalf("Encrypt failed on empty plaintext: %v", err)
	}

	decrypted, err := Decrypt(ciphertext, testKey)
	if err != nil {
		t.Fatalf("Decrypt failed on empty plaintext ciphertext: %v", err)
	}

	if len(decrypted) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	plaintext := []byte("same input")

	ct1, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("first Encrypt failed: %v", err)
	}

	ct2, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("second Encrypt failed: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce reuse?)")
	}
}

func TestEncryptStringDecryptStringRoundTrip(t *testing.T) {
	plaintext := "Hello, string encryption!"

	encoded, err := EncryptString(plaintext, testKey)
	if err != nil {
		t.Fatalf("EncryptString failed: %v", err)
	}

	// Verify it's valid base64
	_, err = base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("EncryptString output is not valid base64: %v", err)
	}

	decrypted, err := DecryptString(encoded, testKey)
	if err != nil {
		t.Fatalf("DecryptString failed: %v", err)
	}

	if decrypted != plaintext {
		t.Fatalf("string round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptStringWrongKey(t *testing.T) {
	encoded, err := EncryptString("secret", testKey)
	if err != nil {
		t.Fatalf("EncryptString failed: %v", err)
	}

	_, err = DecryptString(encoded, "wrong-key")
	if err == nil {
		t.Fatal("expected error when decrypting string with wrong key, got nil")
	}
}

func TestDecryptStringInvalidBase64(t *testing.T) {
	_, err := DecryptString("not-valid-base64!!!", testKey)
	if err == nil {
		t.Fatal("expected error for invalid base64 input, got nil")
	}
}

func TestEncryptStringEmptyPlaintext(t *testing.T) {
	encoded, err := EncryptString("", testKey)
	if err != nil {
		t.Fatalf("EncryptString failed on empty string: %v", err)
	}

	decrypted, err := DecryptString(encoded, testKey)
	if err != nil {
		t.Fatalf("DecryptString failed on empty string ciphertext: %v", err)
	}

	if decrypted != "" {
		t.Fatalf("expected empty string, got %q", decrypted)
	}
}
