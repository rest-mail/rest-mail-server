package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// challengePathPrefix is where an ACME CA fetches HTTP-01 key authorizations.
const challengePathPrefix = "/.well-known/acme-challenge/"

// HTTP01Responder serves the key authorizations of the HTTP-01 challenges an
// Issue call is answering. Serve it on port 80 of every name being issued for.
type HTTP01Responder struct {
	mu     sync.RWMutex
	tokens map[string]string // token → key authorization
}

// NewHTTP01Responder returns an empty responder.
func NewHTTP01Responder() *HTTP01Responder {
	return &HTTP01Responder{tokens: map[string]string{}}
}

func (r *HTTP01Responder) set(token, keyAuth string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[token] = keyAuth
}

func (r *HTTP01Responder) remove(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tokens, token)
}

// ServeHTTP answers GET /.well-known/acme-challenge/<token> for a pending
// challenge, and 404 for everything else.
func (r *HTTP01Responder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	token, ok := strings.CutPrefix(req.URL.Path, challengePathPrefix)
	if !ok || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
		http.NotFound(w, req)
		return
	}
	r.mu.RLock()
	keyAuth, ok := r.tokens[token]
	r.mu.RUnlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(keyAuth))
}

// IssueConfig is one certificate order.
type IssueConfig struct {
	// Directory is the ACME directory URL; empty means LetsEncryptProduction.
	Directory string
	// Email is the ACME account contact; optional.
	Email string
	// Domains are the certificate's DNS names. The first is the subject.
	Domains []string
	// HTTPClient talks to the CA; nil means http.DefaultClient.
	HTTPClient *http.Client
}

// Issue obtains one certificate covering every name in cfg.Domains by HTTP-01,
// publishing the challenge responses through responder, and returns the chain
// and its fresh ECDSA P-256 key as PEM. It registers a new account each time
// and touches no database or disk: the caller decides where the pair goes.
func Issue(ctx context.Context, cfg IssueConfig, responder *HTTP01Responder) (certPEM, keyPEM string, err error) {
	if len(cfg.Domains) == 0 {
		return "", "", errors.New("acme: no domains to issue for")
	}
	if responder == nil {
		return "", "", errors.New("acme: an HTTP-01 responder is required")
	}
	dir := cfg.Directory
	if dir == "" {
		dir = LetsEncryptProduction
	}

	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("acme: failed to generate account key: %w", err)
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: dir, HTTPClient: cfg.HTTPClient}

	acct := &acme.Account{}
	if cfg.Email != "" {
		acct.Contact = []string{"mailto:" + cfg.Email}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil {
		return "", "", fmt.Errorf("acme: failed to register account: %w", err)
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(cfg.Domains...))
	if err != nil {
		return "", "", fmt.Errorf("acme: failed to create order: %w", err)
	}
	for _, authzURL := range order.AuthzURLs {
		if err := solveHTTP01(ctx, client, authzURL, responder); err != nil {
			return "", "", err
		}
	}
	if order, err = client.WaitOrder(ctx, order.URI); err != nil {
		return "", "", fmt.Errorf("acme: order did not become ready: %w", err)
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("acme: failed to generate certificate key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: cfg.Domains[0]},
		DNSNames: cfg.Domains,
	}, certKey)
	if err != nil {
		return "", "", fmt.Errorf("acme: failed to create CSR: %w", err)
	}
	derChain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return "", "", fmt.Errorf("acme: failed to finalize order: %w", err)
	}
	if certPEM, keyPEM, err = encodeCertAndKey(derChain, certKey); err != nil {
		return "", "", fmt.Errorf("acme: failed to encode cert/key: %w", err)
	}

	// Refuse a chain the CA got wrong rather than hand back a pair that cannot
	// serve every name asked for.
	now := time.Now()
	for _, d := range cfg.Domains {
		if _, err := validateCertificate(d, certPEM, keyPEM, now); err != nil {
			return "", "", err
		}
	}
	slog.Info("acme: certificate issued", "domains", cfg.Domains)
	return certPEM, keyPEM, nil
}

// solveHTTP01 answers one authorization's HTTP-01 challenge and waits for the
// CA to validate it.
func solveHTTP01(ctx context.Context, client *acme.Client, authzURL string, responder *HTTP01Responder) error {
	authz, err := client.GetAuthorization(ctx, authzURL)
	if err != nil {
		return fmt.Errorf("acme: failed to get authorization: %w", err)
	}
	if authz.Status == acme.StatusValid {
		return nil
	}
	var chal *acme.Challenge
	for _, c := range authz.Challenges {
		if c.Type == "http-01" {
			chal = c
			break
		}
	}
	if chal == nil {
		return fmt.Errorf("acme: no http-01 challenge offered for %s", authz.Identifier.Value)
	}
	keyAuth, err := client.HTTP01ChallengeResponse(chal.Token)
	if err != nil {
		return fmt.Errorf("acme: failed to compute http-01 response: %w", err)
	}
	responder.set(chal.Token, keyAuth)
	defer responder.remove(chal.Token)

	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("acme: failed to accept http-01 challenge for %s: %w", authz.Identifier.Value, err)
	}
	if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
		return fmt.Errorf("acme: http-01 validation failed for %s: %w", authz.Identifier.Value, err)
	}
	return nil
}
