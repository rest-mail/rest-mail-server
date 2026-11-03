package metrics

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
)

// certExpiryDays collects c and returns restmail_cert_expiry_days, and false
// when the series is absent.
func certExpiryDays(t *testing.T, c prometheus.Collector) (float64, bool) {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "restmail_cert_expiry_days" {
			if n := len(f.GetMetric()); n != 1 {
				t.Fatalf("restmail_cert_expiry_days has %d series, want one", n)
			}
			m := f.GetMetric()[0]
			if len(m.GetLabel()) != 0 {
				t.Fatalf("restmail_cert_expiry_days has labels %v, want none", m.GetLabel())
			}
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

// TestCertExpiryCollector_CertificateExpiringSoon: the CertificateExpiringSoon
// alert (restmail_cert_expiry_days < 7) fires as the soonest certificate nears
// expiry and stays quiet while it is far off.
func TestCertExpiryCollector_CertificateExpiringSoon(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	at := func(notAfter time.Time) *CertExpiryCollector {
		return newCertExpiryCollector(func(context.Context) (time.Time, bool, error) { return notAfter, true, nil }, clock)
	}
	for _, tc := range []struct {
		name     string
		notAfter time.Time
		want     float64
		fires    bool
	}{
		{"fresh certificate", now.Add(90 * 24 * time.Hour), 90, false},
		{"renewal window, not yet urgent", now.Add(8 * 24 * time.Hour), 8, false},
		{"three days left", now.Add(3 * 24 * time.Hour), 3, true},
		{"half a day left", now.Add(12 * time.Hour), 0.5, true},
		{"expired yesterday", now.Add(-24 * time.Hour), -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := certExpiryDays(t, at(tc.notAfter))
			if !ok {
				t.Fatal("restmail_cert_expiry_days absent, want a value")
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("restmail_cert_expiry_days = %v, want %v", got, tc.want)
			}
			if fires := got < 7; fires != tc.fires {
				t.Errorf("CertificateExpiringSoon fires = %v at %v days, want %v", fires, got, tc.fires)
			}
		})
	}
}

// With no certificate the series is absent, not 0: a 0 would read as expiring
// now and fire CertificateExpiringSoon on an installation with nothing to renew.
func TestCertExpiryCollector_NoCertificateIsAbsent(t *testing.T) {
	c := newCertExpiryCollector(func(context.Context) (time.Time, bool, error) { return time.Time{}, false, nil }, time.Now)
	if got, ok := certExpiryDays(t, c); ok {
		t.Fatalf("restmail_cert_expiry_days = %v with no certificates, want absent", got)
	}
}

// A store that cannot be read fails the collection without leaking the
// driver's error into the public scrape.
func TestCertExpiryCollector_StoreErrorIsNotLeaked(t *testing.T) {
	c := newCertExpiryCollector(func(context.Context) (time.Time, bool, error) {
		return time.Time{}, false, errors.New(`pq: relation "certificates" secret-detail`)
	}, time.Now)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	_, err := reg.Gather()
	if err == nil {
		t.Fatal("gather succeeded, want the collection error")
	}
	if strings.Contains(err.Error(), "secret-detail") {
		t.Errorf("scrape error %q carries the driver's error", err)
	}
}

// TestSoonestCertificateExpiry_Database: the gauge follows the newest
// certificate of each domain, so it moves when a certificate nears expiry and
// moves back once a renewal or upload stores its replacement.
func TestSoonestCertificateExpiry_Database(t *testing.T) {
	env := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DB_PORT", "5432"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := rmdb.Connect(&config.Config{
		DBHost: env("DB_HOST", "localhost"), DBPort: port,
		DBName: env("DB_NAME", "restmail"), DBUser: env("DB_USER", "restmail"), DBPass: env("DB_PASS", "restmail"),
	})
	if err != nil {
		testdb.Unreachable(t, "certificate expiry DB test skipped: no database reachable (%v)", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(&models.Domain{}, &models.Certificate{}); err != nil {
		t.Fatal(err)
	}
	// One snapshot for the whole test, so rows other tests commit meanwhile
	// do not move the minimum under it; nothing here is committed.
	tx := database.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	ctx := context.Background()
	soonest := func() (time.Time, bool) {
		t.Helper()
		at, ok, err := SoonestCertificateExpiry(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return at, ok
	}

	// Below whatever the database already holds, and three days out.
	now := time.Now().UTC().Truncate(time.Second)
	soon := now.Add(3 * 24 * time.Hour)
	if before, ok := soonest(); ok && !before.After(soon) {
		soon = before.Add(-24 * time.Hour).UTC().Truncate(time.Second)
	}

	domain := models.Domain{Name: "cert-expiry-1630.test"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	store := func(notAfter time.Time) {
		t.Helper()
		cert := models.Certificate{DomainID: domain.ID, CertPEM: "-", KeyPEM: "-", Issuer: "test",
			NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
		if err := tx.Create(&cert).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A pair this domain no longer serves expires earlier still: it must not count.
	store(soon.Add(-30 * 24 * time.Hour))
	store(soon)
	if got, ok := soonest(); !ok || !got.Equal(soon) {
		t.Fatalf("soonest expiry = %v (%v), want the served certificate's %v", got, ok, soon)
	}
	collector := newCertExpiryCollector(func(ctx context.Context) (time.Time, bool, error) {
		return SoonestCertificateExpiry(ctx, tx)
	}, func() time.Time { return now })
	if days, ok := certExpiryDays(t, collector); !ok || days >= 7 {
		t.Fatalf("restmail_cert_expiry_days = %v (present %v), want under 7 so CertificateExpiringSoon fires", days, ok)
	}

	// A renewal stores the replacement: the domain's value moves 90 days out.
	store(soon.Add(90 * 24 * time.Hour))
	if got, ok := soonest(); ok && got.Equal(soon) {
		t.Fatalf("soonest expiry still %v after the renewal stored its replacement", got)
	}
}
