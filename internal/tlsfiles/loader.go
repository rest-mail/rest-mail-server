// Package tlsfiles reloads externally managed PEM certificate/key files.
package tlsfiles

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// CheckInterval bounds filesystem work on new handshakes, including failed loads.
const CheckInterval = 5 * time.Second

// Loader retains the last complete pair during publication. Stat follows symlinks,
// including certbot live links and Kubernetes ..data swaps.
type Loader struct {
	certPath, keyPath string
	mu                sync.Mutex
	cert              *tls.Certificate
	certInfo, keyInfo os.FileInfo
	interval          time.Duration
	nextCheck         time.Time
	now               func() time.Time
	loadErr           error
	warned            bool
}

// New loads the initial pair. A listener must reject an initial load error.
// The loader is non-nil even on error so lazy SNI lookups can retry later.
// A zero interval checks on every call; serving processes use CheckInterval.
func New(certPath, keyPath string, interval time.Duration) (*Loader, error) {
	l := &Loader{certPath: certPath, keyPath: keyPath, interval: interval, now: time.Now}
	_, err := l.load()
	return l, err
}

// Current returns the last usable pair, or nil if none has ever loaded.
func (l *Loader) Current() *tls.Certificate {
	cert, _ := l.load()
	return cert
}

// GetCertificate logs failed renewals but keeps serving the last complete pair.
func (l *Loader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, err := l.load()
	if cert != nil {
		return cert, nil
	}
	return nil, err
}

// GetClientCertificate provides the same reload behavior for an mTLS client.
func (l *Loader) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return l.GetCertificate(nil)
}

func (l *Loader) load() (*tls.Certificate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Before(l.nextCheck) {
		return l.cert, l.loadErr
	}
	l.nextCheck = now.Add(l.interval)
	l.loadErr = l.reload()
	// Initial errors belong to the caller: an SNI name may simply not be ours.
	// Once a pair has loaded, a failed renewal is always an operator error.
	if l.loadErr != nil && l.cert != nil && !l.warned {
		slog.Error("tls: certificate reload failed; retaining last usable pair", "cert", l.certPath, "key", l.keyPath, "error", l.loadErr)
		l.warned = true
	}
	return l.cert, l.loadErr
}

// reload runs under mu. Failed reads never update the cached pair or metadata.
func (l *Loader) reload() error {
	certInfo, err := os.Stat(l.certPath)
	if err != nil {
		return fmt.Errorf("stat TLS certificate: %w", err)
	}
	keyInfo, err := os.Stat(l.keyPath)
	if err != nil {
		return fmt.Errorf("stat TLS private key: %w", err)
	}
	if l.cert != nil && sameFileVersion(certInfo, l.certInfo) && sameFileVersion(keyInfo, l.keyInfo) {
		l.warned = false
		return nil
	}
	cert, err := tls.LoadX509KeyPair(l.certPath, l.keyPath)
	if err != nil {
		return fmt.Errorf("load TLS certificate and key: %w", err)
	}
	// Reject a pair if either path changed while the two files were read.
	newCertInfo, certErr := os.Stat(l.certPath)
	newKeyInfo, keyErr := os.Stat(l.keyPath)
	if certErr != nil || keyErr != nil || !sameFileVersion(certInfo, newCertInfo) || !sameFileVersion(keyInfo, newKeyInfo) {
		return fmt.Errorf("TLS certificate or key changed during reload")
	}
	l.cert, l.certInfo, l.keyInfo = &cert, newCertInfo, newKeyInfo
	l.warned = false
	slog.Info("tls: reloaded certificate", "cert", l.certPath)
	return nil
}

func sameFileVersion(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
}
