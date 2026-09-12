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
	"github.com/restmail/restmail/internal/metrics"
	"gorm.io/gorm"
)

// DBCertLoader loads TLS certificates from the database, decrypting private
// keys with the master key. Certificates are cached in memory with a TTL.
type DBCertLoader struct {
	db        *gorm.DB
	masterKey string
	fallback  *tls.Certificate
	mu        sync.RWMutex
	cache     map[string]*dbCertEntry
	cacheTTL  time.Duration
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
		var hosted int64
		if err := db.Model(&models.Domain{}).Where("name = ?", strings.ToLower(name)).Count(&hosted).Error; err != nil {
			return false, err
		}
		return hosted > 0, nil
	}
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
	if name == "" {
		// Other mail servers commonly reach port 25 without SNI. They get this
		// server's own certificate, the one for its MX host name.
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, errors.New("no SNI and no fallback certificate")
	}

	// Check cache
	l.mu.RLock()
	entry, ok := l.cache[name]
	l.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.cert, nil
	}

	// Look up in database by domain name
	var cert models.Certificate
	err := l.db.Joins("Domain").
		Where("\"Domain\".name = ? AND certificates.not_after > ?", name, time.Now()).
		Order("certificates.not_after DESC").
		First(&cert).Error
	if err != nil {
		return l.answerOrRefuse(name, fmt.Errorf("no current certificate: %w", err))
	}

	// Decrypt private key if master key is set
	keyPEM := cert.KeyPEM
	if l.masterKey != "" {
		decrypted, err := crypto.DecryptString(cert.KeyPEM, l.masterKey)
		if err != nil {
			return l.answerOrRefuse(name, fmt.Errorf("decrypting the private key: %w", err))
		}
		keyPEM = decrypted
	}

	// Parse the certificate
	tlsCert, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(keyPEM))
	if err != nil {
		return l.answerOrRefuse(name, fmt.Errorf("parsing the stored certificate: %w", err))
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
func (l *DBCertLoader) answerOrRefuse(name string, cause error) (*tls.Certificate, error) {
	if certCovers(l.fallback, name) {
		return l.fallback, nil
	}
	return nil, l.refuse(name, cause)
}

// refuse says why name cannot be served and returns the error that ends the
// handshake. A domain this server hosts but cannot serve is a fault on this side:
// it is counted and logged at error level so it is alerted on. Any other name is
// simply not ours, and refusing it is routine. Neither is ever answered with
// another name's certificate (issue #290).
func (l *DBCertLoader) refuse(name string, cause error) error {
	var hosted int64
	if err := l.db.Model(&models.Domain{}).Where("name = ?", name).Count(&hosted).Error; err != nil {
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
func (l *DBCertLoader) Invalidate(domain string) {
	l.mu.Lock()
	delete(l.cache, domain)
	l.mu.Unlock()
}
