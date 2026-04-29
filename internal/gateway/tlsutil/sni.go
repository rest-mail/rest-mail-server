package tlsutil

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// SNICertLoader provides SNI-based certificate selection by loading
// per-domain certificate/key pairs from a directory. Files are expected
// as {domain}.crt and {domain}.key (e.g. mail3.test.crt, mail3.test.key).
type SNICertLoader struct {
	certDir     string
	fallback    *tls.Certificate
	mu          sync.RWMutex
	cache       map[string]*tls.Certificate
	watcher     *fsnotify.Watcher
	stopCh      chan struct{}
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

// Invalidate removes a domain's cached certificate so it reloads from disk
// on the next TLS handshake.
func (l *SNICertLoader) Invalidate(domain string) {
	l.mu.Lock()
	delete(l.cache, domain)
	l.mu.Unlock()
	slog.Info("sni: invalidated cached cert", "domain", domain)
}

// StartWatching begins monitoring the cert directory for file changes.
func (l *SNICertLoader) StartWatching() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("sni: create watcher: %w", err)
	}

	if err := watcher.Add(l.certDir); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("sni: watch %s: %w", l.certDir, err)
	}

	l.watcher = watcher
	l.stopCh = make(chan struct{})

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
					continue
				}
				base := filepath.Base(event.Name)
				ext := filepath.Ext(base)
				if ext != ".crt" && ext != ".key" {
					continue
				}
				domain := strings.TrimSuffix(base, ext)
				domain = strings.ToLower(domain)
				l.Invalidate(domain)

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Error("sni: watcher error", "error", err)

			case <-l.stopCh:
				return
			}
		}
	}()

	slog.Info("sni: watching cert directory", "dir", l.certDir)
	return nil
}

// Stop shuts down the file watcher.
func (l *SNICertLoader) Stop() {
	if l.stopCh != nil {
		close(l.stopCh)
	}
	if l.watcher != nil {
		_ = l.watcher.Close()
	}
}
