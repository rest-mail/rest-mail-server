package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// DKIMKeyAgeCollector reads the current key inventory at scrape time. Labels
// are bounded by configured domains (one installed selector per domain), not
// messages or retired keys. Unknown legacy ages are absent, never reported as 0.
// Neither collection nor API listing needs to read or decrypt private keys.
type DKIMKeyAgeCollector struct {
	db  *gorm.DB
	age *prometheus.Desc
}

func NewDKIMKeyAgeCollector(db *gorm.DB) *DKIMKeyAgeCollector {
	return &DKIMKeyAgeCollector{
		db: db,
		age: prometheus.NewDesc("restmail_dkim_key_age_seconds",
			"Age of the current domain DKIM key since installation in seconds (absent when unknown)",
			[]string{"domain", "selector"}, nil),
	}
}

func (c *DKIMKeyAgeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.age
}

func (c *DKIMKeyAgeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var domains []models.Domain
	if err := c.db.WithContext(ctx).Select("name", "dkim_selector", "dkim_created_at").
		Where("dkim_selector != '' AND dkim_private_key != '' AND dkim_created_at IS NOT NULL").
		Find(&domains).Error; err != nil {
		// Keep driver/SQL details out of the public scrape response.
		ch <- prometheus.NewInvalidMetric(c.age, errors.New("cannot collect DKIM key ages"))
		return
	}
	now := time.Now()
	for _, d := range domains {
		if d.DKIMCreatedAt != nil {
			ch <- prometheus.MustNewConstMetric(c.age, prometheus.GaugeValue,
				max(0, now.Sub(*d.DKIMCreatedAt).Seconds()), d.Name, d.DKIMSelector)
		}
	}
}
