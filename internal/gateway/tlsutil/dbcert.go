package tlsutil

import (
	"crypto/tls"
	"log/slog"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
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
func (l *DBCertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := hello.ServerName
	if name == "" {
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, nil
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
		slog.Debug("no DB certificate for domain, using fallback", "domain", name)
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, nil
	}

	// Decrypt private key if master key is set
	keyPEM := cert.KeyPEM
	if l.masterKey != "" {
		decrypted, err := crypto.DecryptString(cert.KeyPEM, l.masterKey)
		if err != nil {
			slog.Error("failed to decrypt certificate key", "domain", name, "error", err)
			if l.fallback != nil {
				return l.fallback, nil
			}
			return nil, nil
		}
		keyPEM = decrypted
	}

	// Parse the certificate
	tlsCert, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(keyPEM))
	if err != nil {
		slog.Error("failed to parse certificate from DB", "domain", name, "error", err)
		if l.fallback != nil {
			return l.fallback, nil
		}
		return nil, nil
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

// Invalidate removes a domain's certificate from the cache, forcing a reload on the next request.
func (l *DBCertLoader) Invalidate(domain string) {
	l.mu.Lock()
	delete(l.cache, domain)
	l.mu.Unlock()
}
