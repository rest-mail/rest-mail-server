package tlsutil

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SNICertLoader provides SNI-based certificate selection by loading
// per-domain certificate/key pairs from a directory. Files are expected
// as {domain}.crt and {domain}.key (e.g. mail3.test.crt, mail3.test.key).
type SNICertLoader struct {
	certDir     string
	fallback    *tls.Certificate
	mu          sync.RWMutex
	cache       map[string]*tls.Certificate
}

// NewSNICertLoader creates a loader that serves certificates from certDir.
// The fallback certificate is used when no domain-specific cert is found.
func NewSNICertLoader(certDir string, fallback *tls.Certificate) *SNICertLoader {
	return &SNICertLoader{
		certDir:  certDir,
		fallback: fallback,
		cache:    make(map[string]*tls.Certificate),
	}
}

// GetCertificate implements the tls.Config.GetCertificate callback.
func (l *SNICertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(hello.ServerName)
	if name == "" {
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, fmt.Errorf("no SNI and no fallback certificate")
	}

	// Check cache
	l.mu.RLock()
	if cert, ok := l.cache[name]; ok {
		l.mu.RUnlock()
		return cert, nil
	}
	l.mu.RUnlock()

	// Try to load from disk
	certPath := filepath.Join(l.certDir, name+".crt")
	keyPath := filepath.Join(l.certDir, name+".key")

	if _, err := os.Stat(certPath); err != nil {
		slog.Debug("sni: no cert for domain, using fallback", "domain", name)
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, fmt.Errorf("no certificate for %s", name)
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		slog.Error("sni: failed to load cert", "domain", name, "error", err)
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, fmt.Errorf("failed to load cert for %s: %w", name, err)
	}

	// Cache it
	l.mu.Lock()
	l.cache[name] = &cert
	l.mu.Unlock()

	slog.Info("sni: loaded certificate", "domain", name)
	return &cert, nil
}
