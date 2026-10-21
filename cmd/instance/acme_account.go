package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/restmail/restmail/internal/acme"
)

// loadACMEAccountKey keeps the account identity across instance command runs.
// Scope it to the CA directory, so staging and production never share a key.
// Publish a complete 0600 file with a hard link: concurrent creators must all
// use the winning key, and must never overwrite an existing account identity.
func loadACMEAccountKey(dir, directory string) (*ecdsa.PrivateKey, error) {
	if directory == "" {
		directory = acme.LetsEncryptProduction
	}
	path := filepath.Join(dir, fmt.Sprintf(".acme-account-%x.key", sha256.Sum256([]byte(directory))))
	key, err := readACMEAccountKey(path)
	if !errors.Is(err, os.ErrNotExist) {
		return key, err
	}

	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ACME account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode ACME account key: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".acme-account-tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create ACME account key: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write ACME account key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close ACME account key: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readACMEAccountKey(path)
		}
		return nil, fmt.Errorf("persist ACME account key: %w", err)
	}
	return key, nil
}

func readACMEAccountKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ACME account key: %w", err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "EC PRIVATE KEY" || len(rest) != 0 {
		return nil, errors.New("invalid ACME account key PEM; refusing to replace it")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid ACME account private key; refusing to replace it")
	}
	return key, nil
}
