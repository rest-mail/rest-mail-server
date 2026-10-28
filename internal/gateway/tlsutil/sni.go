package tlsutil

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/restmail/restmail/internal/metrics"
	"github.com/restmail/restmail/internal/tlsfiles"
)

// SNICertLoader provides SNI-based certificate selection by loading
// per-domain certificate/key pairs from a directory. Files are expected
// as {domain}.crt and {domain}.key (e.g. example.test.crt, example.test.key).
type SNICertLoader struct {
	certDir         string
	fallback        *tls.Certificate
	fallbackCurrent func() *tls.Certificate
	primaryName     string
	hosts           func(name string) (bool, error)
	mu              sync.RWMutex
	cache           map[string]*tlsfiles.Loader
	watcher         *fsnotify.Watcher
	stopCh          chan struct{}
}

// SetFallbackCurrent supplies a primary certificate that can change after
// renewal. The original fallback remains the default for existing callers.
func (l *SNICertLoader) SetFallbackCurrent(current func() *tls.Certificate) {
	l.mu.Lock()
	l.fallbackCurrent = current
	l.mu.Unlock()
}

// SetPrimaryPaths identifies the primary pair when it is also in the SNI
// directory. Its names must use the refreshed primary pair, not a cached copy.
func (l *SNICertLoader) SetPrimaryPaths(certPath, keyPath string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if filepath.Dir(filepath.Clean(certPath)) != filepath.Clean(l.certDir) ||
		filepath.Dir(filepath.Clean(keyPath)) != filepath.Clean(l.certDir) {
		return
	}
	name := strings.TrimSuffix(filepath.Base(certPath), ".crt")
	if name != filepath.Base(certPath) && filepath.Base(keyPath) == name+".key" {
		l.primaryName = strings.ToLower(name)
	}
}

func (l *SNICertLoader) currentFallback() *tls.Certificate {
	l.mu.RLock()
	current := l.fallbackCurrent
	fallback := l.fallback
	l.mu.RUnlock()
	if current != nil {
		return current()
	}
	return fallback
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
		cache:    make(map[string]*tlsfiles.Loader),
	}
}

// GetCertificate implements the tls.Config.GetCertificate callback.
//
// A client that asks for a name is answered with that name's certificate or not
// at all: the fallback is never offered under another name, because it fails
// verification anyway and teaches people to click through the warning (#290).
func (l *SNICertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(hello.ServerName)
	fallback := l.currentFallback()
	if name == "" {
		// Other mail servers commonly reach port 25 without SNI. They get this
		// server's own certificate, the one for its MX host name.
		if fallback != nil {
			return fallback, nil
		}
		return nil, fmt.Errorf("no SNI and no fallback certificate")
	}

	// Check cache
	l.mu.RLock()
	loader := l.cache[name]
	hosts := l.hosts
	primary := l.primaryName == name
	l.mu.RUnlock()
	if primary {
		if certCovers(fallback, name) {
			return fallback, nil
		}
		return nil, l.refuse(name, hosts, fmt.Errorf("primary certificate does not cover this name"))
	}
	if loader == nil {
		// Never evict a valid pair while a writer is between its two renames.
		l.mu.Lock()
		loader = l.cache[name]
		if loader == nil {
			var err error
			loader, err = tlsfiles.New(filepath.Join(l.certDir, name+".crt"), filepath.Join(l.certDir, name+".key"), tlsfiles.CheckInterval)
			// Unknown SNI names must not grow the cache without limit.
			if err == nil {
				l.cache[name] = loader
			}
		}
		l.mu.Unlock()
	}
	loaded, err := loader.GetCertificate(hello)
	if err != nil {
		if certCovers(fallback, name) {
			return fallback, nil
		}
		return nil, l.refuse(name, hosts, fmt.Errorf("loading the certificate and key: %w", err))
	}

	return loaded, nil
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

// Invalidate requests a bounded recheck without discarding the last usable pair.
// Polling also discovers changes that do not generate directory watcher events.
func (l *SNICertLoader) Invalidate(domain string) {
	l.mu.RLock()
	loader := l.cache[strings.ToLower(domain)]
	l.mu.RUnlock()
	if loader != nil {
		loader.Current()
	}
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
