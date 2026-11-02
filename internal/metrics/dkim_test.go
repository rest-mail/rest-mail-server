package metrics

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	clientmodel "github.com/prometheus/client_model/go"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
)

func TestDKIMKeyAgeCollector_DatabaseInventory(t *testing.T) {
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
		testdb.Unreachable(t, "DKIM metric DB test skipped: no database reachable (%v)", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(&models.Domain{}, &models.DKIMRotationKey{}); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	created := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	future := time.Now().Add(time.Hour)
	domainIDs := make(map[string]uint)
	for _, d := range []models.Domain{
		{Name: "known-age.test", DKIMSelector: "mail", DKIMPrivateKey: "private-sentinel", DKIMCreatedAt: &created},
		{Name: "future-age.test", DKIMSelector: "future", DKIMPrivateKey: "private-sentinel", DKIMCreatedAt: &future},
		{Name: "unknown-age.test", DKIMSelector: "legacy", DKIMPrivateKey: "private-sentinel"},
		{Name: "no-key-age.test", DKIMSelector: "empty", DKIMCreatedAt: &created},
	} {
		if err := tx.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
		domainIDs[d.Name] = d.ID
	}
	for _, name := range []string{"known-age.test", "future-age.test", "unknown-age.test"} {
		if err := tx.Model(&models.Domain{}).Where("id = ?", domainIDs[name]).Update("dkim_rotation_interval_days", 90).Error; err != nil {
			t.Fatal(err)
		}
	}
	published := created.Add(time.Hour)
	retire := published.Add(2 * time.Hour)
	for _, key := range []models.DKIMRotationKey{
		{DomainID: domainIDs["known-age.test"], Slot: models.DKIMRotationStaged, Selector: "next", PrivateKey: "unreadable-sentinel", TTLSeconds: 3600, CreatedAt: created},
		{DomainID: domainIDs["future-age.test"], Slot: models.DKIMRotationStaged, Selector: "next", PrivateKey: "unreadable-sentinel", TTLSeconds: 3600, CreatedAt: created, PublishedAt: &published},
		{DomainID: domainIDs["unknown-age.test"], Slot: models.DKIMRotationPrevious, Selector: "old", PrivateKey: "unreadable-sentinel", TTLSeconds: 3600, RetireAfter: &retire},
	} {
		if err := tx.Create(&key).Error; err != nil {
			t.Fatal(err)
		}
	}
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(NewDKIMKeyAgeCollector(tx))
	before := time.Now()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	family := func(name string) *clientmodel.MetricFamily {
		t.Helper()
		for _, candidate := range families {
			if candidate.GetName() == name {
				return candidate
			}
		}
		t.Fatalf("missing metric family %s", name)
		return nil
	}
	ages := family("restmail_dkim_key_age_seconds")
	if len(families) != 4 || len(ages.Metric) != 2 {
		t.Fatalf("expected four gauge families with two known keys, got %v", families)
	}
	for _, metric := range ages.Metric {
		labels := map[string]string{}
		for _, label := range metric.Label {
			labels[label.GetName()] = label.GetValue()
		}
		if len(labels) != 2 {
			t.Fatalf("unexpected labels: %v", labels)
		}
		age := metric.GetGauge().GetValue()
		switch labels["domain"] {
		case "known-age.test":
			if labels["selector"] != "mail" || age < before.Sub(created).Seconds() || age > time.Since(created).Seconds() {
				t.Fatalf("incorrect seeded age: labels %v, seconds %f", labels, age)
			}
		case "future-age.test":
			if age != 0 {
				t.Fatalf("future timestamp age = %f, want 0", age)
			}
		default:
			t.Fatalf("unexpected key metric: %v", labels)
		}
	}
	for name, expected := range map[string]map[string]float64{
		"restmail_dkim_rotation_interval_seconds":     {"known-age.test": 90 * 86400, "future-age.test": 90 * 86400, "unknown-age.test": 90 * 86400},
		"restmail_dkim_rotation_state":                {"known-age.test/awaiting_publication": 1, "future-age.test/waiting_ttl": 1, "unknown-age.test/retiring": 1, "no-key-age.test/stable": 1},
		"restmail_dkim_rotation_step_started_seconds": {"known-age.test": float64(created.Unix()), "future-age.test": float64(published.Unix()), "unknown-age.test": float64(retire.Add(-time.Hour).Unix())},
	} {
		metrics := family(name).Metric
		if len(metrics) != len(expected) {
			t.Fatalf("%s: got %d series, want %d", name, len(metrics), len(expected))
		}
		for _, metric := range metrics {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			key := labels["domain"]
			labelCount := 1
			if name == "restmail_dkim_rotation_state" {
				key += "/" + labels["state"]
				labelCount = 2
			}
			want, exists := expected[key]
			if !exists || len(labels) != labelCount || metric.GetGauge().GetValue() != want {
				t.Fatalf("%s: unexpected metric %v", name, metric)
			}
		}
	}
	// Each scrape reads current inventory; deleted/changed selectors leave no
	// stale series in a GaugeVec, and unknown ages do not become zero.
	if err := tx.Model(&models.Domain{}).Where("name = ?", "known-age.test").Update("dkim_selector", "next").Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&models.Domain{}).Where("name = ?", "future-age.test").Update("dkim_private_key", "").Error; err != nil {
		t.Fatal(err)
	}
	families, err = registry.Gather()
	if err != nil {
		t.Fatalf("collect changed inventory: %v, %v", families, err)
	}
	ages = family("restmail_dkim_key_age_seconds")
	if len(ages.Metric) != 1 {
		t.Fatalf("changed key inventory: %v", ages)
	}
	if got := ages.Metric[0].Label[1].GetValue(); got != "next" {
		t.Fatalf("selector = %q, want next", got)
	}
	if err := tx.Where("domain_id IN ?", []uint{domainIDs["known-age.test"], domainIDs["future-age.test"], domainIDs["unknown-age.test"]}).Delete(&models.DKIMRotationKey{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&models.Domain{}).Where("id > 0").Update("dkim_rotation_interval_days", 0).Error; err != nil {
		t.Fatal(err)
	}
	families, err = registry.Gather()
	if err != nil || len(families) != 2 {
		t.Fatalf("stale policy or step metrics: %v, %v", families, err)
	}
	for _, metric := range family("restmail_dkim_rotation_state").Metric {
		if metric.Label[1].GetValue() != "stable" {
			t.Fatalf("stale rotation state: %v", metric)
		}
	}
	if err := tx.Exec("ALTER TABLE dkim_rotation_keys RENAME TO unavailable_rotation_keys").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Gather(); err == nil || !strings.Contains(err.Error(), "cannot collect DKIM rotation state") {
		t.Fatalf("expected sanitized rotation collection error, got %v", err)
	}
	// Database errors fail the scrape rather than pretending no keys exist.
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Gather(); err == nil || !strings.Contains(err.Error(), "cannot collect DKIM key ages") {
		t.Fatalf("expected sanitized collection error, got %v", err)
	}
}
