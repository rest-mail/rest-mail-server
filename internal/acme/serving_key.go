package acme

import (
	"crypto"
	"crypto/tls"
	"errors"
	"strings"

	rmcrypto "github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
)

// ParseServingKey returns the key matching the current certificate. Expired
// certificates are accepted here: renewal replaces the certificate, while
// retaining its public key preserves published DANE-EE SPKI hashes. Invalid or
// incomplete pairs are refused rather than silently rotating a published key.
// Errors never include PEM material.
func ParseServingKey(certPEM, keyPEM string) (crypto.Signer, error) {
	if countPEMBlocks(certPEM, func(t string) bool { return t == "CERTIFICATE" }) == 0 ||
		countPEMBlocks(keyPEM, func(t string) bool { return strings.HasSuffix(t, "PRIVATE KEY") }) != 1 {
		return nil, errors.New("acme: invalid stored serving pair; refusing to replace its key")
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, errors.New("acme: stored serving certificate and key do not match; refusing to replace the key")
	}
	key, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("acme: unsupported stored serving key; refusing to replace it")
	}
	return key, nil
}

func (c *Client) renewalKey(cert *models.Certificate) (crypto.Signer, error) {
	keyPEM := cert.KeyPEM
	// Older installations may have stored a plaintext PEM before configuring a
	// master key. Accept that form, but never interpret failed ciphertext as a
	// missing key and generate a replacement.
	if c.masterKey != "" && !strings.HasPrefix(strings.TrimSpace(keyPEM), "-----BEGIN ") {
		var err error
		keyPEM, err = rmcrypto.DecryptString(keyPEM, c.masterKey)
		if err != nil {
			return nil, errors.New("acme: stored serving key cannot be decrypted; refusing to replace it")
		}
	}
	return ParseServingKey(cert.CertPEM, keyPEM)
}
