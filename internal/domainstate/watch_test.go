package domainstate

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/metrics"
	"gorm.io/gorm"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// openWatchTestDB connects to the test database, skipping (never failing) when
// none is reachable, the way the rest of the repo's DB tests do.
func openWatchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, err := strconv.Atoi(envOr("DB_PORT", "5432"))
	if err != nil {
		t.Skipf("domainstate test skipped: bad DB_PORT (%v)", err)
	}
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("domainstate test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Certificate{}); err != nil {
		t.Skipf("domainstate test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// A domain whose certificate has stopped being valid cannot be served: the
// gateways refuse its handshakes rather than answer under another name. Leaving
// it marked live hides that, so the watcher takes it out of service and counts it
// (issue #289).
func TestWatcher_TakesUnservableDomainsOutOfService(t *testing.T) {
	gdb := openWatchTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	unique := time.Now().UnixNano()
	mkDomain := func(t *testing.T, label string, active bool) models.Domain {
		t.Helper()
		d := models.Domain{
			Name:       fmt.Sprintf("%s-%d.test", label, unique),
			ServerType: "traditional",
			Active:     active,
		}
		if err := tx.Create(&d).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		return d
	}
	mkCert := func(t *testing.T, domainID uint, notAfter time.Time) {
		t.Helper()
		if err := tx.Create(&models.Certificate{
			DomainID:  domainID,
			CertPEM:   "-- test certificate --",
			KeyPEM:    "-- test key --",
			Issuer:    "self-signed",
			NotBefore: time.Now().Add(-48 * time.Hour),
			NotAfter:  notAfter,
		}).Error; err != nil {
			t.Fatalf("create certificate: %v", err)
		}
	}

	expired := mkDomain(t, "expired", true)
	mkCert(t, expired.ID, time.Now().Add(-time.Hour))

	none := mkDomain(t, "nocert", true)

	current := mkDomain(t, "current", true)
	mkCert(t, current.ID, time.Now().Add(30*24*time.Hour))

	// Already out of service with no certificate: nothing to do, and it must not
	// be counted again on every pass.
	dormant := mkDomain(t, "dormant", false)

	before := testutil.ToFloat64(metrics.DomainsOutOfService)

	// The watcher sweeps every live domain, which is what it is for, and the test
	// database is shared, so the totals below are "at least ours": the assertions
	// that matter are about these four domains by name.
	w := NewWatcher(tx, time.Hour)
	took, err := w.Check()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if took < 2 {
		t.Errorf("took %d domains out of service, want at least 2 (the expired one and the one with no certificate)", took)
	}

	live := func(t *testing.T, id uint) bool {
		t.Helper()
		var d models.Domain
		if err := tx.First(&d, id).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		return d.Active
	}

	if live(t, expired.ID) {
		t.Error("a domain whose certificate expired is still live")
	}
	if live(t, none.ID) {
		t.Error("a domain with no certificate at all is still live")
	}
	if !live(t, current.ID) {
		t.Error("a domain with a current certificate was taken out of service")
	}
	if live(t, dormant.ID) {
		t.Error("a domain that was already out of service came back")
	}

	afterFirst := testutil.ToFloat64(metrics.DomainsOutOfService)
	if got := afterFirst - before; got < 2 {
		t.Errorf("counted %v domains out of service, want at least 2", got)
	}

	// A second pass has nothing left to do: a domain is counted once, when it
	// leaves service, and not again on every pass.
	took, err = w.Check()
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if took != 0 {
		t.Errorf("second pass took %d domains out of service, want 0", took)
	}
	if got := testutil.ToFloat64(metrics.DomainsOutOfService) - afterFirst; got != 0 {
		t.Errorf("the second pass counted %v more domains out of service, want 0", got)
	}
}
