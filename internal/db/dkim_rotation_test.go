package db_test

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Use a separate database so migration tests cannot alter another test's schema.
func openDKIMRotationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	env := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DB_PORT", "5432"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DBHost: env("DB_HOST", "localhost"), DBPort: port,
		DBName: env("DB_NAME", "restmail"), DBUser: env("DB_USER", "restmail"), DBPass: env("DB_PASS", "restmail")}
	server, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Skipf("no database reachable: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := server.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	name := fmt.Sprintf("restmail_dkim_storage_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := server.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Exec("DROP DATABASE IF EXISTS " + name).Error; err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	database, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := database.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func rotationFixture(t *testing.T, database *gorm.DB, slot, selector string, domainID uint) models.DKIMRotationKey {
	t.Helper()
	installed := time.Now().UTC().Add(-90 * 24 * time.Hour).Truncate(time.Second)
	published := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	key := models.DKIMRotationKey{
		DomainID: domainID, Slot: slot, Selector: selector,
		PublishedAt: &published, TTLSeconds: 3600,
	}
	if slot == models.DKIMRotationPrevious {
		retire := published.Add(2 * time.Hour)
		key.InstalledAt, key.RetireAfter = &installed, &retire
	}
	if err := key.SetPrivateKey("private-"+selector, "old-master"); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.First(&key, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	return key
}

func TestDKIMRotationStorage_MigrationPersistenceAndCascade(t *testing.T) {
	database := openDKIMRotationTestDB(t)
	// Simulate a deployment before rotation-key storage, with a live key.
	if err := database.Migrator().DropTable(&models.DKIMRotationKey{}); err != nil {
		t.Fatal(err)
	}
	installed := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	domain := models.Domain{Name: "storage.test", DKIMSelector: "current", DKIMPrivateKey: "current-stored-sentinel", DKIMCreatedAt: &installed, DKIMRotationIntervalDays: 90}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.First(&domain, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	var current models.Domain
	if err := database.First(&current, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, domain) {
		t.Fatal("migration changed the existing domain or signing key")
	}
	staged := rotationFixture(t, database, models.DKIMRotationStaged, "restmail-2", domain.ID)
	previous := rotationFixture(t, database, models.DKIMRotationPrevious, "restmail-1", domain.ID)
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	// Reading from a fresh GORM session must preserve all persisted state.
	for _, want := range []models.DKIMRotationKey{staged, previous} {
		var got models.DKIMRotationKey
		if err := database.Session(&gorm.Session{NewDB: true}).First(&got, want.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("rotation metadata did not survive reload")
		}
		if plain, err := models.LoadDKIMPrivateKey(got.PrivateKey, "old-master"); err != nil || plain != "private-"+want.Selector {
			t.Fatal("persisted key cannot be decrypted")
		}
	}
	if err := database.Delete(&domain).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := database.Model(&models.DKIMRotationKey{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("domain deletion left rotation keys behind")
	}
}

func TestDKIMRotationStorage_Constraints(t *testing.T) {
	database := openDKIMRotationTestDB(t)
	domain := models.Domain{Name: "constraints.test"}
	other := models.Domain{Name: "other.test"}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	staged := rotationFixture(t, database, models.DKIMRotationStaged, "restmail-2", domain.ID)
	// Another domain may use the same selector and slot.
	rotationFixture(t, database, models.DKIMRotationStaged, "restmail-2", other.ID)
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, constraint string
		change           func(*models.DKIMRotationKey)
	}{
		{"duplicate slot", "idx_dkim_rotation_domain_slot", func(k *models.DKIMRotationKey) { k.Selector = "another" }},
		{"duplicate selector", "idx_dkim_rotation_domain_selector", func(k *models.DKIMRotationKey) { k.Slot = models.DKIMRotationPrevious; k.RetireAfter = &now }},
		{"duplicate selector case", "idx_dkim_rotation_domain_selector", func(k *models.DKIMRotationKey) {
			k.Slot = models.DKIMRotationPrevious
			k.Selector = strings.ToUpper(k.Selector)
			k.RetireAfter = &now
		}},
		{"invalid slot", "dkim_rotation_slot", func(k *models.DKIMRotationKey) { k.Slot = "active"; k.Selector = "unique" }},
		{"missing domain", "fk_dkim_rotation_keys_domain", func(k *models.DKIMRotationKey) { k.DomainID = 999999 }},
		{"empty selector", "dkim_rotation_selector", func(k *models.DKIMRotationKey) { k.Selector = "" }},
		{"empty private key", "dkim_rotation_private_key", func(k *models.DKIMRotationKey) { k.PrivateKey = "" }},
		{"zero TTL", "dkim_rotation_ttl", func(k *models.DKIMRotationKey) { k.TTLSeconds = 0 }},
		{"negative TTL", "dkim_rotation_ttl", func(k *models.DKIMRotationKey) { k.TTLSeconds = -1 }},
		{"excessive TTL", "dkim_rotation_ttl", func(k *models.DKIMRotationKey) { k.TTLSeconds = 2147483648 }},
		{"previous without retirement", "dkim_rotation_retirement", func(k *models.DKIMRotationKey) { k.Slot = models.DKIMRotationPrevious; k.Selector = "old" }},
		{"staged with retirement", "dkim_rotation_retirement", func(k *models.DKIMRotationKey) { k.RetireAfter = &now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := staged
			key.ID = 0
			tc.change(&key)
			err := database.Transaction(func(tx *gorm.DB) error { return tx.Create(&key).Error })
			if err == nil || !strings.Contains(err.Error(), tc.constraint) {
				t.Fatalf("expected constraint %s, got %v", tc.constraint, err)
			}
		})
	}
}
