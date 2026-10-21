package tlsutil

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// FileCertLoader reloads the primary serving pair when either file is replaced.
// It retains the last complete pair while a writer publishes the key and cert
// with separate renames. Every handshake checks file identity so a missed file
// notification cannot leave the gateway serving an old certificate indefinitely.
type FileCertLoader struct {
	certPath string
	keyPath  string
	mu       sync.Mutex
	cert     *tls.Certificate
	certInfo os.FileInfo
	keyInfo  os.FileInfo
	warned   bool
}

func NewFileCertLoader(certPath, keyPath string) (*FileCertLoader, error) {
	l := &FileCertLoader{certPath: certPath, keyPath: keyPath}
	if _, err := l.load(); err != nil {
		return nil, err
	}
	return l, nil
}

// Current returns the latest complete pair. If a replacement is incomplete or
// invalid, the previous pair remains available and the next handshake retries.
func (l *FileCertLoader) Current() *tls.Certificate {
	cert, err := l.load()
	if err != nil {
		l.mu.Lock()
		if !l.warned {
			slog.Warn("tls: primary certificate reload failed; retaining previous pair", "error", err)
			l.warned = true
		}
		l.mu.Unlock()
	}
	return cert
}

// GetCertificate serves the primary pair for connections without SNI and for
// names it covers. It refuses to offer that pair for a different hostname.
func (l *FileCertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := l.Current()
	name := strings.ToLower(hello.ServerName)
	if name == "" || certCovers(cert, name) {
		return cert, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotServedHere, name)
}

func (l *FileCertLoader) load() (*tls.Certificate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	certInfo, certErr := os.Stat(l.certPath)
	keyInfo, keyErr := os.Stat(l.keyPath)
	if certErr == nil && keyErr == nil && l.cert != nil &&
		sameFileVersion(certInfo, l.certInfo) && sameFileVersion(keyInfo, l.keyInfo) {
		return l.cert, nil
	}
	if certErr != nil {
		return l.cert, fmt.Errorf("stat TLS certificate: %w", certErr)
	}
	if keyErr != nil {
		return l.cert, fmt.Errorf("stat TLS private key: %w", keyErr)
	}
	cert, err := tls.LoadX509KeyPair(l.certPath, l.keyPath)
	if err != nil {
		return l.cert, fmt.Errorf("load TLS certificate and key: %w", err)
	}
	// A writer may have replaced either file while LoadX509KeyPair read them.
	newCertInfo, certErr := os.Stat(l.certPath)
	newKeyInfo, keyErr := os.Stat(l.keyPath)
	if certErr != nil || keyErr != nil || !sameFileVersion(certInfo, newCertInfo) || !sameFileVersion(keyInfo, newKeyInfo) {
		return l.cert, fmt.Errorf("TLS certificate or key changed during reload")
	}
	l.cert = &cert
	l.certInfo = newCertInfo
	l.keyInfo = newKeyInfo
	l.warned = false
	slog.Info("tls: reloaded primary certificate", "cert", l.certPath)
	return l.cert, nil
}

func sameFileVersion(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) &&
		a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
}
