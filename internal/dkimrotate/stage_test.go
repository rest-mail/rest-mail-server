package dkimrotate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dkimrotate"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

const stageMasterKey = "dkim-rotation-stage-test-master-key-0123456789"

// openStageTestDB migrates a scratch database of its own, and skips when no
// database is reachable, as the other database-backed tests do.
func openStageTestDB(t *testing.T) *gorm.DB {
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
	name := fmt.Sprintf("restmail_dkim_stage_%d_%d", os.Getpid(), time.Now().UnixNano())
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

func stageDomain(t *testing.T, database *gorm.DB, name, selector string, installed *time.Time, intervalDays int) models.Domain {
	t.Helper()
	domain := models.Domain{Name: name, DKIMSelector: selector, DKIMPrivateKey: "dkim:v1:current",
		DKIMCreatedAt: installed, DKIMRotationIntervalDays: intervalDays}
	if selector == "" {
		domain.DKIMPrivateKey = ""
	}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	return domain
}

func stagedRows(t *testing.T, database *gorm.DB, domainID uint) []models.DKIMRotationKey {
	t.Helper()
	var rows []models.DKIMRotationKey
	if err := database.Where("domain_id = ? AND slot = ?", domainID, models.DKIMRotationStaged).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestStage_DueKeyIsStagedOnceWithoutTouchingTheCurrentKey(t *testing.T) {
	database := openStageTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	installed := now.Add(-91 * 24 * time.Hour)
	domain := stageDomain(t, database, "stage.test", "default", &installed, 90)

	key, made, err := dkimrotate.Stage(ctx, database, domain.ID, stageMasterKey, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if !made || key == nil || key.Selector != "restmail-1" || key.TTLSeconds != 3600 || key.PrivateKey != "" {
		t.Fatalf("Stage = %+v, %v; want a new restmail-1 key with TTL 3600 and no private key returned", key, made)
	}
	rows := stagedRows(t, database, domain.ID)
	if len(rows) != 1 || rows[0].ID != key.ID || rows[0].InstalledAt != nil || rows[0].PublishedAt != nil || rows[0].RetireAfter != nil {
		t.Fatalf("stored staged rows = %+v", rows)
	}
	if !models.DKIMKeyIsEncrypted(rows[0].PrivateKey) {
		t.Fatal("the staged private key is not encrypted at rest")
	}
	plain, err := models.LoadDKIMPrivateKey(rows[0].PrivateKey, stageMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.CheckDKIMKey(domain.Name, key.Selector, plain); err != nil {
		t.Fatalf("the staged key cannot sign: %v", err)
	}

	var after models.Domain
	if err := database.First(&after, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.DKIMSelector != "default" || after.DKIMPrivateKey != "dkim:v1:current" || after.DKIMCreatedAt == nil || !after.DKIMCreatedAt.Equal(installed.Truncate(time.Microsecond)) {
		t.Fatalf("the current key changed: selector %q, key %q, installed %v", after.DKIMSelector, after.DKIMPrivateKey, after.DKIMCreatedAt)
	}

	again, made, err := dkimrotate.Stage(ctx, database, domain.ID, stageMasterKey, 2*time.Hour, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if made || again == nil || again.ID != key.ID || again.Selector != "restmail-1" || again.TTLSeconds != 3600 || again.PrivateKey != "" {
		t.Fatalf("second Stage = %+v, %v; want the existing key unchanged", again, made)
	}
	if after := stagedRows(t, database, domain.ID); len(after) != 1 || after[0].PrivateKey != rows[0].PrivateKey {
		t.Fatal("the second Stage replaced the staged key")
	}
}

func TestStage_NothingIsStagedUnlessDue(t *testing.T) {
	database := openStageTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	young := now.Add(-time.Hour)
	old := now.Add(-400 * 24 * time.Hour)
	for _, tc := range []struct {
		name      string
		selector  string
		installed *time.Time
		days      int
	}{
		{"off.test", "default", &old, 0},
		{"young.test", "default", &young, 90},
		{"nokey.test", "", nil, 90},
	} {
		domain := stageDomain(t, database, tc.name, tc.selector, tc.installed, tc.days)
		key, made, err := dkimrotate.Stage(ctx, database, domain.ID, stageMasterKey, time.Hour, now)
		if err != nil || key != nil || made {
			t.Errorf("%s: Stage = %+v, %v, %v; want nothing staged", tc.name, key, made, err)
		}
		if rows := stagedRows(t, database, domain.ID); len(rows) != 0 {
			t.Errorf("%s: staged rows %+v", tc.name, rows)
		}
	}
}

func TestStage_SkipsSelectorsInUse(t *testing.T) {
	database := openStageTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// An unknown installation time is due.
	domain := stageDomain(t, database, "taken.test", "restmail-1", nil, 30)
	retire := now.Add(time.Hour)
	previous := models.DKIMRotationKey{DomainID: domain.ID, Slot: models.DKIMRotationPrevious,
		Selector: "RESTMAIL-2", TTLSeconds: 300, RetireAfter: &retire}
	if err := previous.SetPrivateKey("previous-key", stageMasterKey); err != nil {
		t.Fatal(err)
	}
	if err := database.Omit("Domain").Create(&previous).Error; err != nil {
		t.Fatal(err)
	}

	key, made, err := dkimrotate.Stage(ctx, database, domain.ID, stageMasterKey, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if !made || key.Selector != "restmail-3" {
		t.Fatalf("Stage = %+v, %v; want restmail-3 after current restmail-1 and previous RESTMAIL-2", key, made)
	}
}

func TestStage_RefusesBadInput(t *testing.T) {
	database := openStageTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	domain := stageDomain(t, database, "bad.test", "default", nil, 30)
	for _, ttl := range []time.Duration{0, time.Millisecond, (1 << 31) * time.Second} {
		if key, made, err := dkimrotate.Stage(ctx, database, domain.ID, stageMasterKey, ttl, now); err == nil || key != nil || made {
			t.Errorf("TTL %v: Stage = %+v, %v, %v; want an error", ttl, key, made, err)
		}
	}
	if rows := stagedRows(t, database, domain.ID); len(rows) != 0 {
		t.Errorf("staged rows after refusals: %+v", rows)
	}
	if _, _, err := dkimrotate.Stage(ctx, database, domain.ID+1000, stageMasterKey, time.Hour, now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("missing domain: err = %v, want gorm.ErrRecordNotFound", err)
	}
}
