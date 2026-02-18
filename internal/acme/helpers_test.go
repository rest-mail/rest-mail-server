package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
)

// generateTestKey creates an ECDSA P-256 key for use in tests.
func generateTestKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}
