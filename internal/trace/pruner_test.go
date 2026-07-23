package trace

import (
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// seedTrace inserts one message_traces row with an explicit created_at and
// expires_at so pruner tests control the retention horizon and age ordering.
func seedTrace(t *testing.T, db *gorm.DB, rfcID string, createdAt time.Time, expiresAt *time.Time) {
	t.Helper()
	tr := models.MessageTrace{
		RFCMessageID: rfcID,
		Direction:    "inbound",
		Outcome:      outcomeRejected,
		CreatedAt:    createdAt,
		ExpiresAt:    expiresAt,
	}
	if err := db.Create(&tr).Error; err != nil {
		t.Fatalf("seed trace %s: %v", rfcID, err)
	}
}

func countTraces(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.MessageTrace{}).Count(&n).Error; err != nil {
		t.Fatalf("count traces: %v", err)
	}
	return n
}

// TestPruner_DeletesExpiredKeepsUnexpired proves the retention horizon: rows past
// expires_at are deleted, rows with a future (or NULL) expires_at are kept.
func TestPruner_DeletesExpiredKeepsUnexpired(t *testing.T) {
	gdb := openTraceTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM message_traces").Error; err != nil {
		t.Fatalf("clear message_traces: %v", err)
	}

	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	seedTrace(t, tx, "<expired-1@t>", now.Add(-48*time.Hour), &past)
	seedTrace(t, tx, "<expired-2@t>", now.Add(-48*time.Hour), &past)
	seedTrace(t, tx, "<fresh@t>", now, &future)
	seedTrace(t, tx, "<no-horizon@t>", now, nil) // NULL expires_at → not expiry-pruned

	p := NewPruner(tx, time.Hour, 0) // backstop disabled; test expiry only
	p.pruneOnce(now)

	if got := countTraces(t, tx); got != 2 {
		t.Fatalf("after expiry prune: %d traces remain, want 2 (fresh + no-horizon)", got)
	}
	var remaining []models.MessageTrace
	if err := tx.Find(&remaining).Error; err != nil {
		t.Fatalf("load remaining: %v", err)
	}
	for _, r := range remaining {
		if r.RFCMessageID == "<expired-1@t>" || r.RFCMessageID == "<expired-2@t>" {
			t.Errorf("expired trace %s survived the pruner", r.RFCMessageID)
		}
	}
}

// TestPruner_BackstopTrimsOldest proves the TRACE_MAX_ROWS backstop: when the
// table exceeds the cap, the oldest rows (by created_at) beyond it are deleted,
// even though none are past their expires_at.
func TestPruner_BackstopTrimsOldest(t *testing.T) {
	gdb := openTraceTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM message_traces").Error; err != nil {
		t.Fatalf("clear message_traces: %v", err)
	}

	base := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	future := base.Add(365 * 24 * time.Hour) // far-future horizon: nothing is expired
	// Five rows, ages base+0 .. base+4 minutes (oldest first).
	for i := 0; i < 5; i++ {
		seedTrace(t, tx, "<age-"+time.Duration(i).String()+"@t>", base.Add(time.Duration(i)*time.Minute), &future)
	}

	p := NewPruner(tx, time.Hour, 2) // cap at 2 rows
	p.pruneOnce(base.Add(time.Hour))

	if got := countTraces(t, tx); got != 2 {
		t.Fatalf("after backstop: %d traces remain, want 2 (the cap)", got)
	}
	// The two survivors must be the two NEWEST (created_at base+3m and base+4m).
	var survivors []models.MessageTrace
	if err := tx.Order("created_at ASC").Find(&survivors).Error; err != nil {
		t.Fatalf("load survivors: %v", err)
	}
	oldestSurvivor := survivors[0].CreatedAt.UTC()
	if oldestSurvivor.Before(base.Add(3 * time.Minute)) {
		t.Errorf("oldest survivor created_at = %v, want >= %v (oldest rows should have been trimmed)",
			oldestSurvivor, base.Add(3*time.Minute))
	}
}

// TestPruner_LeavesRollupsUntouched proves the pruner never touches
// pipeline_rollups: aggregate history is long-lived and survives a prune that
// deletes every expired trace.
func TestPruner_LeavesRollupsUntouched(t *testing.T) {
	gdb := openTraceTestDB(t)
	if err := gdb.AutoMigrate(&models.PipelineRollup{}); err != nil {
		t.Skipf("pruner rollup test skipped: migrate pipeline_rollups failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM message_traces").Error; err != nil {
		t.Fatalf("clear message_traces: %v", err)
	}

	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour)

	// One expired trace (will be pruned) and one rollup row (must survive).
	seedTrace(t, tx, "<expired@t>", now.Add(-48*time.Hour), &past)
	rollup := models.PipelineRollup{
		MetricName:    "restmail_pipeline_terminal_total",
		Labels:        []byte(`{"direction":"inbound","outcome":"delivered"}`),
		BucketStart:   now.Truncate(5 * time.Minute),
		BucketSeconds: 300,
		Value:         42,
	}
	if err := tx.Create(&rollup).Error; err != nil {
		t.Fatalf("seed rollup: %v", err)
	}

	p := NewPruner(tx, time.Hour, 1_000_000)
	p.pruneOnce(now)

	if got := countTraces(t, tx); got != 0 {
		t.Errorf("expired trace not pruned: %d remain, want 0", got)
	}
	var rollupCount int64
	if err := tx.Model(&models.PipelineRollup{}).Count(&rollupCount).Error; err != nil {
		t.Fatalf("count rollups: %v", err)
	}
	if rollupCount != 1 {
		t.Errorf("pipeline_rollups rows = %d after prune, want 1 (pruner must never touch rollups)", rollupCount)
	}
}
