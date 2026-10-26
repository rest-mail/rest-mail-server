package main

import (
	"crypto"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/restmail/restmail/internal/acme"
)

// Both files absent means first issuance. Any incomplete or damaged existing
// pair stops renewal, so a lost key never silently invalidates published TLSA.
// The caller holds the issuer lock and has recovered interrupted publication.
func existingACMEServingKey(dir, name string) (crypto.Signer, error) {
	cert, certErr := os.ReadFile(filepath.Join(dir, name+".crt"))
	key, keyErr := os.ReadFile(filepath.Join(dir, name+".key"))
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		return nil, nil
	}
	if certErr != nil || keyErr != nil {
		return nil, fmt.Errorf("read existing ACME serving pair; refusing to replace its key: %w", errors.Join(certErr, keyErr))
	}
	return acme.ParseServingKey(string(cert), string(key))
}
