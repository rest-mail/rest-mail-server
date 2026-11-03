package metrics

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// CertExpiryCollector reports restmail_cert_expiry_days: the days left on the
// soonest-expiring certificate this installation serves, read from the
// certificate store at scrape time (#1630). Reading at scrape time means the
// value is current after every ACME renewal, upload or deletion without either
// path having to remember to update it.
//
// It is one series with no labels. The old per-`domain` label was unbounded
// (one series per certificate domain); the domain behind a low value is on the
// admin health endpoint and in the renewal log lines. A domain's certificate is
// the newest one stored for it, the one the gateways serve, so an older pair a
// renewal or upload superseded does not count. The value goes negative once
// that certificate has expired. With no certificates stored the series is
// absent: unknown is never reported as 0, which would read as already expired.
//
// The API registers it on its own /metrics; the protocol gateways do not, so
// there is one series per installation, not one per process.
type CertExpiryCollector struct {
	soonest func(ctx context.Context) (time.Time, bool, error)
	now     func() time.Time
	days    *prometheus.Desc
}

// NewCertExpiryCollector reads certificates from db.
func NewCertExpiryCollector(db *gorm.DB) *CertExpiryCollector {
	return newCertExpiryCollector(func(ctx context.Context) (time.Time, bool, error) {
		return SoonestCertificateExpiry(ctx, db)
	}, time.Now)
}

func newCertExpiryCollector(soonest func(ctx context.Context) (time.Time, bool, error), now func() time.Time) *CertExpiryCollector {
	return &CertExpiryCollector{
		soonest: soonest,
		now:     now,
		days: prometheus.NewDesc("restmail_cert_expiry_days",
			"Days until the soonest-expiring served certificate expires (negative once expired; absent with no certificates)",
			nil, nil),
	}
}

// SoonestCertificateExpiry returns the earliest expiry among the newest
// certificate of each domain, and false when no certificate is stored.
func SoonestCertificateExpiry(ctx context.Context, db *gorm.DB) (time.Time, bool, error) {
	var soonest sql.NullTime
	err := db.WithContext(ctx).Raw(
		`SELECT MIN(newest) FROM (
		     SELECT MAX(not_after) AS newest FROM certificates GROUP BY domain_id
		 ) AS per_domain`,
	).Row().Scan(&soonest)
	if err != nil {
		return time.Time{}, false, err
	}
	return soonest.Time, soonest.Valid, nil
}

func (c *CertExpiryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.days
}

func (c *CertExpiryCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notAfter, ok, err := c.soonest(ctx)
	if err != nil {
		// Keep driver/SQL details out of the public scrape response.
		ch <- prometheus.NewInvalidMetric(c.days, errors.New("cannot collect certificate expiry"))
		return
	}
	if !ok {
		return
	}
	days := notAfter.Sub(c.now()).Hours() / 24
	ch <- prometheus.MustNewConstMetric(c.days, prometheus.GaugeValue, days)
}
