// Package acme provides ACME (Let's Encrypt) certificate auto-provisioning.
// It uses golang.org/x/crypto/acme and autocert to obtain and renew TLS
// certificates, storing them in the database via the Certificate model.
package acme

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const (
	// LetsEncryptProduction is the Let's Encrypt production directory URL.
	LetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"

	// LetsEncryptStaging is the Let's Encrypt staging directory URL.
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

	// RenewalWindow is how far before expiry we begin attempting renewal.
	RenewalWindow = 30 * 24 * time.Hour // 30 days
)

// Client wraps autocert.Manager and stores obtained certificates in the database.
type Client struct {
	db        *gorm.DB
	masterKey string
	email     string
	directory string
	manager   *autocert.Manager
	mu        sync.Mutex
}

// ClientConfig holds configuration for creating a new ACME client.
type ClientConfig struct {
	DB        *gorm.DB
	MasterKey string
	Email     string
	Directory string // ACME directory URL; defaults to LetsEncryptProduction
	Staging   bool   // if true, overrides Directory to use staging
}

// NewClient creates a new ACME client configured with the given options.
func NewClient(cfg ClientConfig) *Client {
	dir := cfg.Directory
	if cfg.Staging {
		dir = LetsEncryptStaging
	}
	if dir == "" {
		dir = LetsEncryptProduction
	}

	c := &Client{
		db:        cfg.DB,
		masterKey: cfg.MasterKey,
		email:     cfg.Email,
		directory: dir,
	}

	c.manager = &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Email:  cfg.Email,
		Client: &acme.Client{
			DirectoryURL: dir,
		},
		// We implement our own cache to store certificates in the database.
		Cache: &dbCache{client: c},
	}

	return c
}

// HTTPHandler returns an http.Handler that serves ACME HTTP-01 challenge
// responses on /.well-known/acme-challenge/. Any non-challenge request
// is passed through to the fallback handler.
func (c *Client) HTTPHandler(fallback http.Handler) http.Handler {
	return c.manager.HTTPHandler(fallback)
}

// ChallengeHandler returns an http.HandlerFunc specifically for serving
// ACME HTTP-01 challenge tokens. Wire this into the router at
// /.well-known/acme-challenge/{token}.
func (c *Client) ChallengeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The autocert Manager HTTPHandler handles challenge paths itself.
		// We delegate to it, using nil as fallback which returns 403 for
		// non-challenge paths.
		c.manager.HTTPHandler(nil).ServeHTTP(w, r)
	}
}

// ObtainCertificate requests a new certificate for the given domain via ACME
// and stores it in the database. It uses the autocert Manager to perform the
// challenge and obtain the certificate.
func (c *Client) ObtainCertificate(ctx context.Context, domain string) (*models.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	slog.Info("acme: obtaining certificate", "domain", domain)

	// Generate a new ECDSA private key for this certificate.
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to generate private key: %w", err)
	}

	// Create a new ACME client for the actual certificate request.
	acmeClient := &acme.Client{
		DirectoryURL: c.directory,
	}

	// Generate an account key if we do not already have one.
	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to generate account key: %w", err)
	}
	acmeClient.Key = accountKey

	// Register the account.
	acct := &acme.Account{
		Contact: []string{"mailto:" + c.email},
	}
	if _, err := acmeClient.Register(ctx, acct, autocert.AcceptTOS); err != nil {
		return nil, fmt.Errorf("acme: failed to register account: %w", err)
	}

	// Begin the order.
	order, err := acmeClient.AuthorizeOrder(ctx, acme.DomainIDs(domain))
	if err != nil {
		return nil, fmt.Errorf("acme: failed to authorize order: %w", err)
	}

	// Process authorizations - handle HTTP-01 challenges.
	for _, authzURL := range order.AuthzURLs {
		authz, err := acmeClient.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("acme: failed to get authorization: %w", err)
		}

		if authz.Status == acme.StatusValid {
			continue
		}

		// Find and accept the HTTP-01 challenge.
		var challenge *acme.Challenge
		for _, ch := range authz.Challenges {
			if ch.Type == "http-01" {
				challenge = ch
				break
			}
		}
		if challenge == nil {
			return nil, fmt.Errorf("acme: no http-01 challenge available for %s", domain)
		}

		// Accept the challenge.
		if _, err := acmeClient.Accept(ctx, challenge); err != nil {
			return nil, fmt.Errorf("acme: failed to accept challenge: %w", err)
		}

		// Wait for authorization to complete.
		if _, err := acmeClient.WaitAuthorization(ctx, authzURL); err != nil {
			return nil, fmt.Errorf("acme: authorization failed: %w", err)
		}
	}

	// Create a CSR.
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		DNSNames: []string{domain},
	}, privateKey)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to create CSR: %w", err)
	}

	// Finalize the order and get the certificate.
	derChain, _, err := acmeClient.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to finalize order: %w", err)
	}

	// Convert to PEM.
	certPEM, keyPEM, err := encodeCertAndKey(derChain, privateKey)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to encode cert/key: %w", err)
	}

	// Store in database.
	cert, err := c.StoreCertificate(domain, certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("acme: failed to store certificate: %w", err)
	}

	slog.Info("acme: certificate obtained successfully", "domain", domain, "not_after", cert.NotAfter)
	return cert, nil
}

// StoreCertificate stores a PEM-encoded certificate and key in the database,
// encrypting the private key with the master key. It refuses, without writing,
// a key that does not match the certificate, a certificate not valid for
// domain, and an expired one.
func (c *Client) StoreCertificate(domain, certPEM, keyPEM string) (*models.Certificate, error) {
	// Refuse anything that could not be served for domain before touching the
	// database, so a bad issuance or renewal never adds a row that would
	// supplant the working certificate.
	x509Cert, err := validateCertificate(domain, certPEM, keyPEM, time.Now())
	if err != nil {
		return nil, err
	}

	// Look up the domain in the database.
	var dbDomain models.Domain
	if err := c.db.Where("name = ?", domain).First(&dbDomain).Error; err != nil {
		return nil, fmt.Errorf("acme: domain %q not found in database: %w", domain, err)
	}

	// Encrypt the private key.
	keyToStore := keyPEM
	if c.masterKey != "" {
		encrypted, err := crypto.EncryptString(keyPEM, c.masterKey)
		if err != nil {
			return nil, fmt.Errorf("acme: failed to encrypt private key: %w", err)
		}
		keyToStore = encrypted
	} else {
		slog.Warn("acme: private key stored in plaintext: MASTER_KEY not configured")
	}

	cert := &models.Certificate{
		DomainID:  dbDomain.ID,
		CertPEM:   certPEM,
		KeyPEM:    keyToStore,
		Issuer:    "letsencrypt",
		NotBefore: x509Cert.NotBefore,
		NotAfter:  x509Cert.NotAfter,
		AutoRenew: true,
	}

	if err := c.db.Create(cert).Error; err != nil {
		return nil, fmt.Errorf("acme: failed to create certificate record: %w", err)
	}

	return cert, nil
}

// validateCertificate checks that certPEM is a certificate chain and keyPEM its
// one matching private key, and that the leaf is valid for domain at now. It
// returns the leaf. Its errors never quote the PEM input, which carries the
// private key.
func validateCertificate(domain, certPEM, keyPEM string, now time.Time) (*x509.Certificate, error) {
	// certPEM is stored in plaintext, so it must hold certificates and nothing
	// else; a stray private key block there would be kept unencrypted.
	if countPEMBlocks(certPEM, func(t string) bool { return t == "CERTIFICATE" }) == 0 {
		return nil, errors.New("acme: certificate PEM must hold only CERTIFICATE blocks")
	}
	if countPEMBlocks(keyPEM, func(t string) bool { return strings.HasSuffix(t, "PRIVATE KEY") }) != 1 {
		return nil, errors.New("acme: private key PEM must hold exactly one private key block")
	}

	// X509KeyPair parses the chain and the RSA, ECDSA or Ed25519 key, and checks
	// that the key belongs to the leaf certificate.
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, errors.New("acme: certificate and private key are not a valid matching pair")
	}
	leaf := pair.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return nil, errors.New("acme: failed to parse certificate")
		}
	}

	if err := leaf.VerifyHostname(domain); err != nil {
		return nil, fmt.Errorf("acme: certificate is not valid for domain %q", domain)
	}
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("acme: certificate for %q expired at %s", domain, leaf.NotAfter.UTC().Format(time.RFC3339))
	}

	return leaf, nil
}

// countPEMBlocks returns how many PEM blocks data holds, or 0 if it holds
// anything else: a block of a type allowed refuses, or text outside the blocks.
func countPEMBlocks(data string, allowed func(blockType string) bool) int {
	n := 0
	rest := bytes.TrimSpace([]byte(data))
	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN ")) {
			return 0
		}
		block, next := pem.Decode(rest)
		if block == nil || !allowed(block.Type) {
			return 0
		}
		n++
		rest = bytes.TrimSpace(next)
	}
	return n
}

// NeedsRenewal returns true if the certificate expires within RenewalWindow.
func NeedsRenewal(cert *models.Certificate) bool {
	return time.Until(cert.NotAfter) < RenewalWindow
}

// CertificatesNeedingRenewal queries the database for certificates that are
// auto-renew enabled and expire within the renewal window.
func (c *Client) CertificatesNeedingRenewal() ([]models.Certificate, error) {
	var certs []models.Certificate
	threshold := time.Now().Add(RenewalWindow)
	err := c.db.Preload("Domain").
		Where("auto_renew = ? AND not_after < ? AND issuer = ?", true, threshold, "letsencrypt").
		Find(&certs).Error
	if err != nil {
		return nil, fmt.Errorf("acme: failed to query certificates: %w", err)
	}
	return certs, nil
}

// RenewCertificate obtains a new certificate for the given domain and updates
// the existing certificate record.
func (c *Client) RenewCertificate(ctx context.Context, cert *models.Certificate) error {
	if cert.Domain.Name == "" {
		// Load the domain if not preloaded.
		var domain models.Domain
		if err := c.db.First(&domain, cert.DomainID).Error; err != nil {
			return fmt.Errorf("acme: failed to load domain for cert %d: %w", cert.ID, err)
		}
		cert.Domain = domain
	}

	slog.Info("acme: renewing certificate", "domain", cert.Domain.Name, "cert_id", cert.ID, "expires", cert.NotAfter)

	newCert, err := c.ObtainCertificate(ctx, cert.Domain.Name)
	if err != nil {
		return fmt.Errorf("acme: renewal failed for %s: %w", cert.Domain.Name, err)
	}

	// Delete the old certificate record now that we have the new one.
	if err := c.db.Delete(cert).Error; err != nil {
		slog.Warn("acme: failed to delete old certificate after renewal",
			"cert_id", cert.ID, "domain", cert.Domain.Name, "error", err)
		// Non-fatal: the new certificate is stored, old one will just be extra.
	}

	slog.Info("acme: certificate renewed successfully",
		"domain", cert.Domain.Name, "new_cert_id", newCert.ID, "not_after", newCert.NotAfter)

	return nil
}

// encodeCertAndKey encodes a DER certificate chain and ECDSA private key to PEM.
func encodeCertAndKey(derChain [][]byte, key *ecdsa.PrivateKey) (certPEM string, keyPEM string, err error) {
	var certBuf []byte
	for _, der := range derChain {
		block := &pem.Block{Type: "CERTIFICATE", Bytes: der}
		certBuf = append(certBuf, pem.EncodeToMemory(block)...)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal private key: %w", err)
	}

	keyBlock := &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}
	keyBuf := pem.EncodeToMemory(keyBlock)

	return string(certBuf), string(keyBuf), nil
}

// dbCache implements the autocert.Cache interface, storing certificates in the
// database via the ACME Client.
type dbCache struct {
	client *Client
}

// certificateCacheKey accepts only autocert's canonical DNS certificate keys:
// <domain> (ECDSA) and <domain>+rsa. Account and challenge keys are separate
// namespaces and must never address certificate rows.
func certificateCacheKey(key string) (domain string, algorithm x509.PublicKeyAlgorithm, err error) {
	domain, algorithm = key, x509.ECDSA
	if strings.HasSuffix(key, "+rsa") {
		domain, algorithm = strings.TrimSuffix(key, "+rsa"), x509.RSA
	}
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return "", 0, errors.New("acme: unsupported certificate cache key")
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", 0, errors.New("acme: unsupported certificate cache key")
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return "", 0, errors.New("acme: unsupported certificate cache key")
			}
		}
	}
	return domain, algorithm, nil
}

// cacheCertificates selects the requested algorithm from existing certificate
// PEM, preserving StoreCertificate's schema and direct-store semantics. Filter
// before choosing the newest row: another algorithm may expire later.
func (c *dbCache) cacheCertificates(ctx context.Context, key string) ([]models.Certificate, error) {
	domain, algorithm, err := certificateCacheKey(key)
	if err != nil {
		return nil, err
	}
	var certs []models.Certificate
	if err := c.client.db.WithContext(ctx).
		Joins("JOIN domains ON domains.id = certificates.domain_id").
		Where("domains.name = ? AND certificates.issuer = ?", domain, "letsencrypt").
		Order("certificates.not_after DESC, certificates.id DESC").
		Find(&certs).Error; err != nil {
		return nil, err
	}
	matching := certs[:0]
	for _, cert := range certs {
		block, _ := pem.Decode([]byte(cert.CertPEM))
		if block == nil || block.Type != "CERTIFICATE" {
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err == nil && leaf.PublicKeyAlgorithm == algorithm {
			matching = append(matching, cert)
		}
	}
	return matching, nil
}

func (c *dbCache) Get(ctx context.Context, key string) ([]byte, error) {
	certs, err := c.cacheCertificates(ctx, key)
	if err != nil || len(certs) == 0 {
		return nil, autocert.ErrCacheMiss
	}
	cert := certs[0]

	// Decrypt private key if needed.
	keyPEM := cert.KeyPEM
	if c.client.masterKey != "" {
		decrypted, err := crypto.DecryptString(cert.KeyPEM, c.client.masterKey)
		if err != nil {
			// May be stored in plaintext.
			slog.Warn("acme: cache get: key decryption failed, using as-is", "domain", key)
		} else {
			keyPEM = decrypted
		}
	}

	// Return the key followed by the chain: autocert reads the private key from
	// the first PEM block, and treats anything else as a cache miss.
	return []byte(keyPEM + cert.CertPEM), nil
}

func (c *dbCache) Put(ctx context.Context, key string, data []byte) error {
	domain, algorithm, err := certificateCacheKey(key)
	if err != nil {
		return err
	}
	// autocert stores the private key followed by the certificate chain.
	certPEM, keyPEM, err := splitCertKey(data)
	if err != nil {
		return fmt.Errorf("acme: cache put: failed to split cert/key: %w", err)
	}
	leaf, err := validateCertificate(domain, certPEM, keyPEM, time.Now())
	if err != nil {
		return err
	}
	if leaf.PublicKeyAlgorithm != algorithm {
		return errors.New("acme: certificate algorithm does not match cache key")
	}

	_, err = c.client.StoreCertificate(domain, certPEM, keyPEM)
	return err
}

func (c *dbCache) Delete(ctx context.Context, key string) error {
	// Unsupported keys have no certificate entry to delete.
	if _, _, err := certificateCacheKey(key); err != nil {
		return nil
	}
	certs, err := c.cacheCertificates(ctx, key)
	if err != nil {
		return err
	}
	if len(certs) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(certs))
	for _, cert := range certs {
		ids = append(ids, cert.ID)
	}
	return c.client.db.WithContext(ctx).Delete(&models.Certificate{}, ids).Error
}

// splitCertKey splits concatenated PEM data into certificate and key parts.
func splitCertKey(data []byte) (certPEM, keyPEM string, err error) {
	var certBlocks, keyBlocks []byte
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		encoded := pem.EncodeToMemory(block)
		switch block.Type {
		case "CERTIFICATE":
			certBlocks = append(certBlocks, encoded...)
		case "EC PRIVATE KEY", "RSA PRIVATE KEY", "PRIVATE KEY":
			keyBlocks = append(keyBlocks, encoded...)
		}
	}

	if len(certBlocks) == 0 {
		return "", "", fmt.Errorf("no certificate found in PEM data")
	}
	if len(keyBlocks) == 0 {
		return "", "", fmt.Errorf("no private key found in PEM data")
	}

	return string(certBlocks), string(keyBlocks), nil
}
