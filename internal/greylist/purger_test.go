package greylist

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// openGreylistTestDB connects to the unit-test Postgres, skipping (never
// failing) when none is reachable, per the repo's DB-test convention.
func openGreylistTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("greylist purger DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.GreylistEntry{}); err != nil {
		t.Skipf("greylist purger DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func seedEntry(t *testing.T, db *gorm.DB, sender string, createdAt time.Time, expiresAt *time.Time) {
	t.Helper()
	e := models.GreylistEntry{
		Sender:     sender,
		Recipient:  "r@ex.test",
		SourceIP:   sender, // unique per row: keeps the (sender,recipient,ip) key unique
		FirstSeen:  createdAt,
		RetryAfter: createdAt.Add(5 * time.Minute),
		ExpiresAt:  expiresAt,
		CreatedAt:  createdAt,
	}
	if err := db.Create(&e).Error; err != nil {
		t.Fatalf("seed greylist entry %s: %v", sender, err)
	}
}

func countEntries(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.GreylistEntry{}).Count(&n).Error; err != nil {
		t.Fatalf("count greylist entries: %v", err)
	}
	return n
}

// TestPurger_DeletesExpiredKeepsUnexpired proves the TTL horizon is enforced:
// rows past expires_at are deleted while rows with a future horizon are kept. On
// a codebase with no purger nothing is ever deleted, which is exactly the
// unbounded-growth defect this closes.
func TestPurger_DeletesExpiredKeepsUnexpired(t *testing.T) {
	gdb := openGreylistTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM greylist_entries").Error; err != nil {
		t.Fatalf("clear greylist_entries: %v", err)
	}

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	seedEntry(t, tx, "expired-1", now.Add(-48*time.Hour), &past)
	seedEntry(t, tx, "expired-2", now.Add(-48*time.Hour), &past)
	seedEntry(t, tx, "fresh", now, &future)

	p := NewPurger(tx, time.Hour)
	deleted := p.PurgeOnce(now)

	if deleted != 2 {
		t.Errorf("PurgeOnce deleted = %d, want 2", deleted)
	}
	if got := countEntries(t, tx); got != 1 {
		t.Fatalf("after purge: %d entries remain, want 1 (fresh)", got)
	}
}

// TestPurger_FallbackBoundsNullHorizonRows proves legacy rows written before the
// expires_at column existed (NULL horizon) are still bounded: those older than
// the default TTL are collected by the created_at fallback, recent ones survive.
func TestPurger_FallbackBoundsNullHorizonRows(t *testing.T) {
	gdb := openGreylistTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM greylist_entries").Error; err != nil {
		t.Fatalf("clear greylist_entries: %v", err)
	}

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	seedEntry(t, tx, "legacy-old", now.Add(-40*24*time.Hour), nil) // older than 36d default TTL
	seedEntry(t, tx, "legacy-new", now.Add(-1*24*time.Hour), nil)  // recent: must survive

	p := NewPurger(tx, time.Hour)
	deleted := p.PurgeOnce(now)

	if deleted != 1 {
		t.Errorf("PurgeOnce deleted = %d, want 1 (the aged NULL-horizon row)", deleted)
	}
	if got := countEntries(t, tx); got != 1 {
		t.Fatalf("after purge: %d entries remain, want 1 (recent legacy row)", got)
	}
}
