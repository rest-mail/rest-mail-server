package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dkimrotate"
	"gorm.io/gorm"
)

// DKIMKeyAgeCollector reads the current key inventory at scrape time. Labels
// are bounded by configured domains (one installed selector per domain), not
// messages or retired keys. Unknown legacy ages are absent, never reported as 0.
// Neither collection nor API listing needs to read or decrypt private keys.
type DKIMKeyAgeCollector struct {
	db          *gorm.DB
	age         *prometheus.Desc
	interval    *prometheus.Desc
	state       *prometheus.Desc
	stepStarted *prometheus.Desc
}

func NewDKIMKeyAgeCollector(db *gorm.DB) *DKIMKeyAgeCollector {
	return &DKIMKeyAgeCollector{
		db: db,
		age: prometheus.NewDesc("restmail_dkim_key_age_seconds",
			"Age of the current domain DKIM key since installation in seconds (absent when unknown)",
			[]string{"domain", "selector"}, nil),
		interval: prometheus.NewDesc("restmail_dkim_rotation_interval_seconds",
			"Configured DKIM rotation interval in seconds (absent when disabled)", []string{"domain"}, nil),
		state: prometheus.NewDesc("restmail_dkim_rotation_state",
			"Current DKIM rotation state (1 for the current state)", []string{"domain", "state"}, nil),
		stepStarted: prometheus.NewDesc("restmail_dkim_rotation_step_started_seconds",
			"Unix timestamp when the current DKIM rotation step began (absent when stable)", []string{"domain"}, nil),
	}
}

func (c *DKIMKeyAgeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.age
	ch <- c.interval
	ch <- c.state
	ch <- c.stepStarted
}

func (c *DKIMKeyAgeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var domains []struct {
		models.Domain
		HasKey bool
	}
	if err := c.db.WithContext(ctx).Model(&models.Domain{}).
		Select("id", "name", "dkim_selector", "dkim_created_at", "dkim_rotation_interval_days", "dkim_private_key <> '' AS has_key").
		Find(&domains).Error; err != nil {
		// Keep driver/SQL details out of the public scrape response.
		ch <- prometheus.NewInvalidMetric(c.age, errors.New("cannot collect DKIM key ages"))
		return
	}
	var keys []models.DKIMRotationKey
	if err := c.db.WithContext(ctx).Select("domain_id", "slot", "created_at", "published_at", "retire_after", "ttl_seconds").Find(&keys).Error; err != nil {
		ch <- prometheus.NewInvalidMetric(c.state, errors.New("cannot collect DKIM rotation state"))
		return
	}
	byDomain := make(map[uint][]models.DKIMRotationKey)
	for _, key := range keys {
		byDomain[key.DomainID] = append(byDomain[key.DomainID], key)
	}
	now := time.Now()
	for _, d := range domains {
		if d.HasKey && d.DKIMCreatedAt != nil && d.DKIMSelector != "" {
			ch <- prometheus.MustNewConstMetric(c.age, prometheus.GaugeValue,
				max(0, now.Sub(*d.DKIMCreatedAt).Seconds()), d.Name, d.DKIMSelector)
		}
		if d.DKIMRotationIntervalDays > 0 {
			ch <- prometheus.MustNewConstMetric(c.interval, prometheus.GaugeValue,
				float64(d.DKIMRotationIntervalDays)*24*60*60, d.Name)
		}
		state, started := dkimrotate.Status(byDomain[d.ID])
		ch <- prometheus.MustNewConstMetric(c.state, prometheus.GaugeValue, 1, d.Name, state)
		if started != nil {
			ch <- prometheus.MustNewConstMetric(c.stepStarted, prometheus.GaugeValue, float64(started.Unix()), d.Name)
		}
	}
}
