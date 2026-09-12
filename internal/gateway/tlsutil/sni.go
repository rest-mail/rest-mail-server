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
	"github.com/restmail/restmail/internal/metrics"
)

// SNICertLoader provides SNI-based certificate selection by loading
// per-domain certificate/key pairs from a directory. Files are expected
// as {domain}.crt and {domain}.key (e.g. example.test.crt, example.test.key).
type SNICertLoader struct {
	certDir  string
	fallback *tls.Certificate
	hosts    func(name string) (bool, error)
	mu       sync.RWMutex
	cache    map[string]*tls.Certificate
	watcher  *fsnotify.Watcher
	stopCh   chan struct{}
}

// SetHostedNames tells the loader which names this server is responsible for, so
// that a name it should be able to serve but cannot is reported as the fault it
// is, rather than as an ordinary request for a name that is not ours.
func (l *SNICertLoader) SetHostedNames(hosts func(name string) (bool, error)) {
	l.mu.Lock()
	l.hosts = hosts
	l.mu.Unlock()
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
//
// A client that asks for a name is answered with that name's certificate or not
// at all: the fallback is never offered under another name, because it fails
// verification anyway and teaches people to click through the warning (#290).
func (l *SNICertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(hello.ServerName)
	if name == "" {
		// Other mail servers commonly reach port 25 without SNI. They get this
		// server's own certificate, the one for its MX host name.
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, fmt.Errorf("no SNI and no fallback certificate")
	}

	// Check cache
	l.mu.RLock()
	cert, cached := l.cache[name]
	hosts := l.hosts
	l.mu.RUnlock()
	if cached {
		return cert, nil
	}

	// Try to load from disk
	certPath := filepath.Join(l.certDir, name+".crt")
	keyPath := filepath.Join(l.certDir, name+".key")

	if _, err := os.Stat(certPath); err != nil {
		// A single keypair whose SANs list this installation's host names is the
		// usual arrangement, and for those names it is the right answer, not a
		// substitute for one.
		if certCovers(l.fallback, name) {
			return l.fallback, nil
		}
		return nil, l.refuse(name, hosts, fmt.Errorf("no certificate file: %w", err))
	}

	loaded, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		if certCovers(l.fallback, name) {
			return l.fallback, nil
		}
		return nil, l.refuse(name, hosts, fmt.Errorf("loading the certificate and key: %w", err))
	}

	// Cache it
	l.mu.Lock()
	l.cache[name] = &loaded
	l.mu.Unlock()

	slog.Info("sni: loaded certificate", "domain", name)
	return &loaded, nil
}

// refuse says why name cannot be served and returns the error that ends the
// handshake. A name this server is responsible for but cannot serve is a fault on
// this side, so it is logged at error level to be alerted on; any other name is
// simply not ours, and refusing it is routine.
func (l *SNICertLoader) refuse(name string, hosts func(string) (bool, error), cause error) error {
	if hosts != nil {
		hosted, err := hosts(name)
		if err != nil {
			slog.Error("sni: cannot tell whether this domain is served here", "domain", name, "error", err)
			return fmt.Errorf("no usable certificate for %s: %w", name, cause)
		}
		if hosted {
			metrics.TLSCertMissing.Inc()
			slog.Error("sni: a domain served here has no usable certificate, refusing the handshake",
				"domain", name, "error", cause)
			return fmt.Errorf("no usable certificate for %s, which is served here: %w", name, cause)
		}
		slog.Debug("sni: not a domain served here, refusing the handshake", "domain", name)
		return fmt.Errorf("%w: %s", ErrNotServedHere, name)
	}
	// Without a hosted-names check the two cases cannot be told apart, so this is
	// reported as the fault it may well be, and counted: a gateway that serves any
	// domain from this directory would otherwise refuse handshakes silently.
	metrics.TLSCertMissing.Inc()
	slog.Error("sni: no usable certificate, refusing the handshake", "domain", name, "error", cause)
	return fmt.Errorf("no usable certificate for %s: %w", name, cause)
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
