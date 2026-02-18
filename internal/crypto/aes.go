package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// deriveKey returns a 32-byte AES-256 key by SHA-256 hashing the master key.
func deriveKey(masterKey string) []byte {
	h := sha256.Sum256([]byte(masterKey))
	return h[:]
}

// Encrypt encrypts plaintext using AES-256-GCM with a key derived from masterKey.
// It returns nonce (12 bytes) + ciphertext concatenated.
func Encrypt(plaintext []byte, masterKey string) ([]byte, error) {
	key := deriveKey(masterKey)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}

	// Seal appends the ciphertext to nonce, so the result is nonce+ciphertext.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts data produced by Encrypt. The first 12 bytes are the nonce,
// the remainder is the GCM ciphertext+tag.
func Decrypt(ciphertext []byte, masterKey string) ([]byte, error) {
	key := deriveKey(masterKey)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("crypto: ciphertext too short")
	}

	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decryption failed: %w", err)
	}

	return plaintext, nil
}

// EncryptString encrypts the plaintext string and returns base64-encoded ciphertext.
func EncryptString(plaintext, masterKey string) (string, error) {
	data, err := Encrypt([]byte(plaintext), masterKey)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// DecryptString decodes the base64-encoded ciphertext and decrypts it, returning the plaintext string.
func DecryptString(encoded, masterKey string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to decode base64: %w", err)
	}
	plaintext, err := Decrypt(data, masterKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
