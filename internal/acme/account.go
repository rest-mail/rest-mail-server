package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"

	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// accountKey returns the API's account key at c.directory, creating and storing
// it on first use, so every order and renewal shares one CA account. Concurrent
// creators all use the stored winner. A stored key that does not decode stops
// issuance: replacing it would silently open a second CA account.
func (c *Client) accountKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	key, err := c.loadAccountKey(ctx)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return key, err
	}

	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to generate account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to encode account key: %w", err)
	}
	keyToStore := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	if c.masterKey != "" {
		if keyToStore, err = crypto.EncryptString(keyToStore, c.masterKey); err != nil {
			return nil, fmt.Errorf("acme: failed to encrypt account key: %w", err)
		}
	} else {
		slog.Warn("acme: account key stored in plaintext: MASTER_KEY not configured")
	}

	account := &models.ACMEAccount{Directory: c.directory, KeyPEM: keyToStore}
	if err := c.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(account).Error; err != nil {
		return nil, fmt.Errorf("acme: failed to store account key: %w", err)
	}
	return c.loadAccountKey(ctx)
}

// loadAccountKey reads the stored account key at c.directory, returning
// gorm.ErrRecordNotFound when there is none. Its errors never quote the key.
func (c *Client) loadAccountKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	var account models.ACMEAccount
	if err := c.db.WithContext(ctx).Where("directory = ?", c.directory).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("acme: failed to load account key: %w", err)
	}

	keyPEM := account.KeyPEM
	if c.masterKey != "" {
		decrypted, err := crypto.DecryptString(keyPEM, c.masterKey)
		if err != nil {
			return nil, errors.New("acme: stored account key does not decrypt with MASTER_KEY; refusing to replace it")
		}
		keyPEM = decrypted
	}
	block, rest := pem.Decode([]byte(keyPEM))
	if block == nil || block.Type != "EC PRIVATE KEY" || len(rest) != 0 {
		return nil, errors.New("acme: invalid stored account key PEM; refusing to replace it")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("acme: invalid stored account private key; refusing to replace it")
	}
	return key, nil
}
