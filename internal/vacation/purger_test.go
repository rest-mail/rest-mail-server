package vacation

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

// openVacationTestDB connects to the unit-test Postgres, skipping (never
// failing) when none is reachable, per the repo's DB-test convention.
func openVacationTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("vacation purger DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.VacationResponse{}); err != nil {
		t.Skipf("vacation purger DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func seedResponse(t *testing.T, db *gorm.DB, sender string, respondedAt time.Time) {
	t.Helper()
	r := models.VacationResponse{
		MailboxID:   1,
		Sender:      sender,
		RespondedAt: respondedAt,
	}
	if err := db.Create(&r).Error; err != nil {
		t.Fatalf("seed vacation response %s: %v", sender, err)
	}
}

func countResponses(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.VacationResponse{}).Count(&n).Error; err != nil {
		t.Fatalf("count vacation responses: %v", err)
	}
	return n
}

// TestPurger_DeletesAgedKeepsRecent proves the retention horizon is enforced:
// vacation_responses rows older than the retention window are deleted while
// recent ones survive. On the pre-fix codebase (no purger) nothing is ever
// deleted — the unbounded-growth defect this closes.
func TestPurger_DeletesAgedKeepsRecent(t *testing.T) {
	gdb := openVacationTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM vacation_responses").Error; err != nil {
		t.Fatalf("clear vacation_responses: %v", err)
	}

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour

	seedResponse(t, tx, "aged-1", now.Add(-31*24*time.Hour)) // past retention
	seedResponse(t, tx, "aged-2", now.Add(-40*24*time.Hour)) // past retention
	seedResponse(t, tx, "recent", now.Add(-1*24*time.Hour))  // within retention

	p := NewPurger(tx, time.Hour, retention)
	deleted := p.PurgeOnce(now)

	if deleted != 2 {
		t.Errorf("PurgeOnce deleted = %d, want 2", deleted)
	}
	if got := countResponses(t, tx); got != 1 {
		t.Fatalf("after purge: %d rows remain, want 1 (recent)", got)
	}
}
