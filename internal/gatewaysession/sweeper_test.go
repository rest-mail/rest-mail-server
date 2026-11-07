package gatewaysession

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, err := strconv.Atoi(envOr("DB_PORT", "5432"))
	if err != nil {
		t.Fatalf("DB_PORT: %v", err)
	}
	gdb, err := rmdb.Connect(&config.Config{
		DBHost: envOr("DB_HOST", "localhost"), DBPort: port,
		DBName: envOr("DB_NAME", "restmail"), DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	})
	if err != nil {
		testdb.Unreachable(t, "gateway session sweeper DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.GatewaySession{}); err != nil {
		t.Fatalf("migrate gateway_sessions: %v", err)
	}
	// Each test owns its rows: start from an empty table.
	if err := gdb.Exec("DELETE FROM gateway_sessions").Error; err != nil {
		t.Fatalf("clear gateway_sessions: %v", err)
	}
	return gdb
}

// openSession opens a session last used at lastUsed.
func openSession(t *testing.T, db *gorm.DB, lastUsed time.Time) *models.GatewaySession {
	t.Helper()
	s, _, err := repositories.NewGatewaySessionRepository(db).Open(1, 1, "password", models.GatewayServiceIMAP, "imap", lastUsed)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	return s
}

func closedAt(t *testing.T, db *gorm.DB, id uint) *time.Time {
	t.Helper()
	var s models.GatewaySession
	if err := db.First(&s, id).Error; err != nil {
		t.Fatalf("load session %d: %v", id, err)
	}
	return s.ClosedAt
}

// TestSweepOnceClosesOnlyIdleSessions: a session unused for longer than
// GatewaySessionIdle is closed, one used within it stays open, and one
// already closed keeps its time.
func TestSweepOnceClosesOnlyIdleSessions(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	idle := openSession(t, db, now.Add(-models.GatewaySessionIdle-time.Minute))
	recent := openSession(t, db, now.Add(-3*time.Hour))
	ended := openSession(t, db, now.Add(-48*time.Hour))
	endedAt := now.Add(-47 * time.Hour)
	if err := db.Model(ended).Update("closed_at", endedAt).Error; err != nil {
		t.Fatal(err)
	}

	if got := NewSweeper(db, time.Hour).SweepOnce(context.Background(), now); got != 1 {
		t.Fatalf("SweepOnce closed %d sessions, want 1", got)
	}
	if c := closedAt(t, db, idle.ID); c == nil || !c.Equal(now) {
		t.Errorf("idle session closed_at = %v, want %v", c, now)
	}
	if c := closedAt(t, db, recent.ID); c != nil {
		t.Errorf("recently used session closed at %v, want open", c)
	}
	if c := closedAt(t, db, ended.ID); c == nil || !c.Equal(endedAt) {
		t.Errorf("already closed session closed_at = %v, want %v kept", c, endedAt)
	}
}

// TestSweeperRunsOnItsTicker: once started, the sweeper closes an idle session
// by itself.
func TestSweeperRunsOnItsTicker(t *testing.T) {
	db := openTestDB(t)
	idle := openSession(t, db, time.Now().Add(-models.GatewaySessionIdle-time.Hour))

	s := NewSweeper(db, 20*time.Millisecond)
	s.Start()
	defer s.Shutdown()
	deadline := time.Now().Add(5 * time.Second)
	for closedAt(t, db, idle.ID) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the started sweeper did not close the idle session")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
