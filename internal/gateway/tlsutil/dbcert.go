package tlsutil

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/metrics"
	"gorm.io/gorm"
)

// DBCertLoader loads TLS certificates from the database, decrypting private
// keys with the master key. Certificates are cached in memory with a TTL.
type DBCertLoader struct {
	db              *gorm.DB
	masterKey       string
	fallback        *tls.Certificate
	fallbackCurrent func() *tls.Certificate
	mu              sync.RWMutex
	cache           map[string]*dbCertEntry
	cacheTTL        time.Duration
}

// SetFallbackCurrent supplies a primary certificate that can change after renewal.
func (l *DBCertLoader) SetFallbackCurrent(current func() *tls.Certificate) {
	l.fallbackCurrent = current
}

func (l *DBCertLoader) currentFallback() *tls.Certificate {
	if l.fallbackCurrent != nil {
		return l.fallbackCurrent()
	}
	return l.fallback
}

type dbCertEntry struct {
	cert      *tls.Certificate
	expiresAt time.Time
}

// HostedInDatabase answers whether a name is a domain this server is responsible
// for, according to the domains table. It is what a file-backed loader needs to
// tell "a domain of ours whose certificate is broken", which is a fault worth
// alerting on, from "a name that is not ours", which is routine.
func HostedInDatabase(db *gorm.DB) func(name string) (bool, error) {
	return func(name string) (bool, error) {
		_, stored, err := storedNames(name)
		if err != nil {
			// Not a domain name at all, so not one hosted here.
			return false, nil
		}
		var hosted int64
		if err := db.Model(&models.Domain{}).Where("name IN ?", stored).Count(&hosted).Error; err != nil {
			return false, err
		}
		return hosted > 0, nil
	}
}

// storedNames maps the name a client asked for to its A-label, the form SNI and
// certificates carry, and to the names a hosted domain may be stored under. SNI
// only ever carries A-labels (RFC 6066), but a domain is kept as the name it was
// entered with, which for an internationalized domain may be its Unicode form,
// so both are matched (#153).
func storedNames(name string) (string, []string, error) {
	ascii, err := mail.DomainASCII(strings.TrimSuffix(name, "."))
	if err != nil {
		return "", nil, err
	}
	unicode, err := mail.DomainUnicode(ascii)
	if err != nil || unicode == ascii {
		return ascii, []string{ascii}, nil
	}
	return ascii, []string{ascii, unicode}, nil
}

// NewDBCertLoader creates a certificate loader that reads from the database.
func NewDBCertLoader(db *gorm.DB, masterKey string, fallback *tls.Certificate) *DBCertLoader {
	return &DBCertLoader{
		db:        db,
		masterKey: masterKey,
		fallback:  fallback,
		cache:     make(map[string]*dbCertEntry),
		cacheTTL:  5 * time.Minute,
	}
}

// GetCertificate implements the tls.Config.GetCertificate callback.
// It looks up the certificate by SNI server name from the database.
//
// A client that asks for a name is answered with that name's certificate or not
// at all: the fallback is never offered under another name, because it fails
// verification anyway and teaches people to click through the warning (#290).
func (l *DBCertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(hello.ServerName)
	fallback := l.currentFallback()
	if name == "" {
		// Other mail servers commonly reach port 25 without SNI. They get this
		// server's own certificate, the one for its MX host name.
		if fallback != nil {
			return fallback, nil
		}
		return nil, errors.New("no SNI and no fallback certificate")
	}

	ascii, stored, err := storedNames(name)
	if err != nil {
		if certCovers(fallback, name) {
			return fallback, nil
		}
		slog.Debug("not a domain name, refusing the handshake", "server_name", name, "error", err)
		return nil, fmt.Errorf("%w: %s", ErrNotServedHere, name)
	}
	name = ascii

	// Check cache
	l.mu.RLock()
	entry, ok := l.cache[name]
	l.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.cert, nil
	}

	// Look up in database by domain name
	var cert models.Certificate
	err = l.db.Joins("Domain").
		Where("\"Domain\".name IN ? AND certificates.not_after > ?", stored, time.Now()).
		Order("certificates.not_after DESC").
		First(&cert).Error
	if err != nil {
		return l.answerOrRefuse(name, stored, fmt.Errorf("no current certificate: %w", err))
	}

	// Decrypt private key if master key is set
	keyPEM := cert.KeyPEM
	if l.masterKey != "" {
		decrypted, err := crypto.DecryptString(cert.KeyPEM, l.masterKey)
		if err != nil {
			return l.answerOrRefuse(name, stored, fmt.Errorf("decrypting the private key: %w", err))
		}
		keyPEM = decrypted
	}

	// Parse the certificate
	tlsCert, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(keyPEM))
	if err != nil {
		return l.answerOrRefuse(name, stored, fmt.Errorf("parsing the stored certificate: %w", err))
	}

	// Cache it
	l.mu.Lock()
	l.cache[name] = &dbCertEntry{
		cert:      &tlsCert,
		expiresAt: time.Now().Add(l.cacheTTL),
	}
	l.mu.Unlock()

	slog.Info("loaded certificate from DB", "domain", name, "issuer", cert.Issuer, "expires", cert.NotAfter)
	return &tlsCert, nil
}

// answerOrRefuse serves this server's own certificate when it carries the name the
// client asked for, and otherwise refuses. An installation with a single keypair
// whose SANs list its host names is the usual arrangement, and for those names that
// certificate is the right answer rather than a substitute for one (issue #290).
func (l *DBCertLoader) answerOrRefuse(name string, stored []string, cause error) (*tls.Certificate, error) {
	fallback := l.currentFallback()
	if certCovers(fallback, name) {
		return fallback, nil
	}
	return nil, l.refuse(name, stored, cause)
}

// refuse says why name cannot be served and returns the error that ends the
// handshake. A domain this server hosts but cannot serve is a fault on this side:
// it is counted and logged at error level so it is alerted on. Any other name is
// simply not ours, and refusing it is routine. Neither is ever answered with
// another name's certificate (issue #290).
func (l *DBCertLoader) refuse(name string, stored []string, cause error) error {
	var hosted int64
	if err := l.db.Model(&models.Domain{}).Where("name IN ?", stored).Count(&hosted).Error; err != nil {
		slog.Error("cannot tell whether this domain is served here", "domain", name, "error", err)
		return fmt.Errorf("no usable certificate for %s: %w", name, cause)
	}
	if hosted > 0 {
		metrics.TLSCertMissing.Inc()
		slog.Error("a domain served here has no usable certificate, refusing the handshake",
			"domain", name, "error", cause)
		return fmt.Errorf("no usable certificate for %s, which is served here: %w", name, cause)
	}
	slog.Debug("not a domain served here, refusing the handshake", "domain", name)
	return fmt.Errorf("%w: %s", ErrNotServedHere, name)
}

// Invalidate removes a domain's certificate from the cache, forcing a reload on the next request.
// The cache is keyed by A-label, so a domain may be named in either form.
func (l *DBCertLoader) Invalidate(domain string) {
	key := strings.ToLower(domain)
	if ascii, _, err := storedNames(domain); err == nil {
		key = ascii
	}
	l.mu.Lock()
	delete(l.cache, key)
	l.mu.Unlock()
}
