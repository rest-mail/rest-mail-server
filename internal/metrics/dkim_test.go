package metrics

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
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
		t.Skipf("DKIM metric DB test skipped: no database reachable (%v)", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := database.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	created := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	future := time.Now().Add(time.Hour)
	for _, d := range []models.Domain{
		{Name: "known-age.test", DKIMSelector: "mail", DKIMPrivateKey: "private-sentinel", DKIMCreatedAt: &created},
		{Name: "future-age.test", DKIMSelector: "future", DKIMPrivateKey: "private-sentinel", DKIMCreatedAt: &future},
		{Name: "unknown-age.test", DKIMSelector: "legacy", DKIMPrivateKey: "private-sentinel"},
		{Name: "no-key-age.test", DKIMSelector: "empty", DKIMCreatedAt: &created},
	} {
		if err := tx.Create(&d).Error; err != nil {
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
	if len(families) != 1 || families[0].GetName() != "restmail_dkim_key_age_seconds" || len(families[0].Metric) != 2 {
		t.Fatalf("expected one gauge family with two known keys, got %v", families)
	}
	for _, metric := range families[0].Metric {
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
	// Each scrape reads current inventory; deleted/changed selectors leave no
	// stale series in a GaugeVec, and unknown ages do not become zero.
	if err := tx.Model(&models.Domain{}).Where("name = ?", "known-age.test").Update("dkim_selector", "next").Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&models.Domain{}).Where("name = ?", "future-age.test").Update("dkim_private_key", "").Error; err != nil {
		t.Fatal(err)
	}
	families, err = registry.Gather()
	if err != nil || len(families) != 1 || len(families[0].Metric) != 1 {
		t.Fatalf("collect changed inventory: %v, %v", families, err)
	}
	if got := families[0].Metric[0].Label[1].GetValue(); got != "next" {
		t.Fatalf("selector = %q, want next", got)
	}
	// Database errors fail the scrape rather than pretending no keys exist.
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Gather(); err == nil || !strings.Contains(err.Error(), "cannot collect DKIM key ages") {
		t.Fatalf("expected sanitized collection error, got %v", err)
	}
}
