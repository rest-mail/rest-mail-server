package rollup

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// ── pure unit (no DB) ────────────────────────────────────────────────

// TestDownsampleEnabled confirms the enable gate: both a positive detailed
// window and a positive coarse resolution are required.
func TestDownsampleEnabled(t *testing.T) {
	cases := []struct {
		name             string
		detailed, coarse time.Duration
		want             bool
	}{
		{"both set", 7 * 24 * time.Hour, 24 * time.Hour, true},
		{"no detailed", 0, 24 * time.Hour, false},
		{"no coarse", 7 * 24 * time.Hour, 0, false},
		{"neither", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &Worker{detailedRetention: tc.detailed, coarseResolution: tc.coarse}
			if got := w.downsampleEnabled(); got != tc.want {
				t.Errorf("downsampleEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDownsampleOnce_DisabledNoop proves a worker without downsampling configured
// makes no DB calls (nil db would panic if it tried) — the additive feature is
// truly opt-in.
func TestDownsampleOnce_DisabledNoop(t *testing.T) {
	w := &Worker{} // downsampling disabled, nil db
	if err := w.downsampleOnce(time.Now()); err != nil {
		t.Fatalf("disabled downsampleOnce should be a no-op, got %v", err)
	}
}

// ── DB-backed (skip when no database is reachable) ───────────────────

func openDownsampleTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("downsample DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.PipelineRollup{}, &models.PipelineRollupCoarse{}); err != nil {
		t.Skipf("downsample DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

const testMetric = "restmail_pipeline_terminal_total"

// seedFine inserts one fine rollup row with canonical labels.
func seedFine(t *testing.T, db *gorm.DB, labels map[string]string, bucketStart time.Time, value float64) {
	t.Helper()
	lj, _ := canonicalLabels(testMetric, labels)
	row := models.PipelineRollup{
		MetricName:    testMetric,
		Labels:        lj,
		BucketStart:   bucketStart.UTC(),
		BucketSeconds: 300,
		Value:         value,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed fine row: %v", err)
	}
}

// coarseByDayOutcome returns coarse values keyed by "YYYY-MM-DD/outcome".
func coarseByDayOutcome(t *testing.T, db *gorm.DB) map[string]float64 {
	t.Helper()
	var rows []models.PipelineRollupCoarse
	if err := db.Where("metric_name = ?", testMetric).Find(&rows).Error; err != nil {
		t.Fatalf("load coarse rows: %v", err)
	}
	out := map[string]float64{}
	for _, r := range rows {
		var labels map[string]string
		if err := json.Unmarshal(r.Labels, &labels); err != nil {
			t.Fatalf("decode coarse labels %s: %v", r.Labels, err)
		}
		out[r.BucketStart.UTC().Format("2006-01-02")+"/"+labels["outcome"]] = r.Value
	}
	return out
}

func countFine(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.PipelineRollup{}).Where("metric_name = ?", testMetric).Count(&n).Error; err != nil {
		t.Fatalf("count fine: %v", err)
	}
	return n
}

func countCoarse(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.PipelineRollupCoarse{}).Where("metric_name = ?", testMetric).Count(&n).Error; err != nil {
		t.Fatalf("count coarse: %v", err)
	}
	return n
}

// downsampleTestWorker builds a worker over the test DB with daily downsampling
// and a 7-day detailed window.
func downsampleTestWorker(db *gorm.DB) *Worker {
	return &Worker{
		db:                 db,
		detailedRetention:  7 * 24 * time.Hour,
		coarseResolution:   24 * time.Hour,
		coarseRetention:    0, // unbounded coarse by default
		downsampleInterval: time.Hour,
	}
}

// TestDownsample_CondensesAgedRows is the core proof: fine rows in coarse periods
// aged past the 7-day window are SUMmed into daily coarse rows and their fine rows
// removed, while recent (within-window) fine rows are left untouched at fine
// resolution.
func TestDownsample_CondensesAgedRows(t *testing.T) {
	db := openDownsampleTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM pipeline_rollups").Error; err != nil {
		t.Fatalf("clear fine: %v", err)
	}
	if err := tx.Exec("DELETE FROM pipeline_rollups_coarse").Error; err != nil {
		t.Fatalf("clear coarse: %v", err)
	}

	delivered := map[string]string{"direction": "inbound", "outcome": "delivered"}
	rejected := map[string]string{"direction": "inbound", "outcome": "rejected"}

	// now: 2026-07-24 12:00 → cutoff = truncate(2026-07-17 12:00, day) = 2026-07-17.
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	// Aged day A (2026-07-14): delivered 5 + 3 (two fine buckets), rejected 2.
	seedFine(t, tx, delivered, time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 5)
	seedFine(t, tx, delivered, time.Date(2026, 7, 14, 0, 5, 0, 0, time.UTC), 3)
	seedFine(t, tx, rejected, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), 2)
	// Aged day B (2026-07-15): delivered 4.
	seedFine(t, tx, delivered, time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC), 4)
	// Boundary day (2026-07-16, still < cutoff → eligible): delivered 1.
	seedFine(t, tx, delivered, time.Date(2026, 7, 16, 23, 55, 0, 0, time.UTC), 1)
	// Recent day within window (2026-07-20 ≥ cutoff): delivered 7 — must survive.
	seedFine(t, tx, delivered, time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC), 7)

	w := downsampleTestWorker(tx)
	if err := w.downsampleOnce(now); err != nil {
		t.Fatalf("downsampleOnce: %v", err)
	}

	coarse := coarseByDayOutcome(t, tx)
	want := map[string]float64{
		"2026-07-14/delivered": 8,
		"2026-07-14/rejected":  2,
		"2026-07-15/delivered": 4,
		"2026-07-16/delivered": 1,
	}
	for k, v := range want {
		if coarse[k] != v {
			t.Errorf("coarse[%s] = %v, want %v (all: %v)", k, coarse[k], v, coarse)
		}
	}
	if len(coarse) != len(want) {
		t.Errorf("coarse row count = %d, want %d (all: %v)", len(coarse), len(want), coarse)
	}

	// Only the recent within-window fine row survives.
	if n := countFine(t, tx); n != 1 {
		t.Errorf("fine rows remaining = %d, want 1 (recent only)", n)
	}

	// Coarse rows carry the coarse width, not the fine width.
	var sample models.PipelineRollupCoarse
	if err := tx.Where("metric_name = ?", testMetric).First(&sample).Error; err != nil {
		t.Fatalf("load coarse sample: %v", err)
	}
	if sample.BucketSeconds != 86400 {
		t.Errorf("coarse BucketSeconds = %d, want 86400", sample.BucketSeconds)
	}
}

// TestDownsample_Idempotent proves re-running condenses nothing new — no double
// count and no duplicate coarse rows — since committed periods have no fine rows
// left to re-aggregate.
func TestDownsample_Idempotent(t *testing.T) {
	db := openDownsampleTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	tx.Exec("DELETE FROM pipeline_rollups")
	tx.Exec("DELETE FROM pipeline_rollups_coarse")

	delivered := map[string]string{"direction": "inbound", "outcome": "delivered"}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	seedFine(t, tx, delivered, time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 5)
	seedFine(t, tx, delivered, time.Date(2026, 7, 14, 0, 5, 0, 0, time.UTC), 3)

	w := downsampleTestWorker(tx)
	for i := 0; i < 3; i++ {
		if err := w.downsampleOnce(now); err != nil {
			t.Fatalf("downsampleOnce #%d: %v", i, err)
		}
	}

	coarse := coarseByDayOutcome(t, tx)
	if coarse["2026-07-14/delivered"] != 8 {
		t.Errorf("delivered = %v, want 8 (no double count over 3 runs)", coarse["2026-07-14/delivered"])
	}
	if n := countCoarse(t, tx); n != 1 {
		t.Errorf("coarse rows = %d, want 1 (no duplicate series)", n)
	}
	if n := countFine(t, tx); n != 0 {
		t.Errorf("fine rows = %d, want 0 (all condensed)", n)
	}
}

// TestDownsample_CrashSafe proves per-period atomicity: a failure between the
// coarse write and the fine delete rolls the whole period back (fine rows survive,
// no partial coarse row), and a subsequent clean run condenses correctly — no data
// loss, no double count.
func TestDownsample_CrashSafe(t *testing.T) {
	db := openDownsampleTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	tx.Exec("DELETE FROM pipeline_rollups")
	tx.Exec("DELETE FROM pipeline_rollups_coarse")

	delivered := map[string]string{"direction": "inbound", "outcome": "delivered"}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	seedFine(t, tx, delivered, time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 5)
	seedFine(t, tx, delivered, time.Date(2026, 7, 14, 0, 5, 0, 0, time.UTC), 3)

	w := downsampleTestWorker(tx)
	w.onBeforeFineDelete = func() error { return errors.New("simulated crash mid-downsample") }

	if err := w.downsampleOnce(now); err == nil {
		t.Fatal("expected downsampleOnce to surface the injected failure")
	}

	// Consistent state after the crash: fine rows intact, nothing condensed.
	if n := countFine(t, tx); n != 2 {
		t.Errorf("fine rows after crash = %d, want 2 (rolled back intact)", n)
	}
	if n := countCoarse(t, tx); n != 0 {
		t.Errorf("coarse rows after crash = %d, want 0 (no partial write)", n)
	}

	// Recovery run condenses cleanly.
	w.onBeforeFineDelete = nil
	if err := w.downsampleOnce(now); err != nil {
		t.Fatalf("recovery downsampleOnce: %v", err)
	}
	coarse := coarseByDayOutcome(t, tx)
	if coarse["2026-07-14/delivered"] != 8 {
		t.Errorf("delivered after recovery = %v, want 8", coarse["2026-07-14/delivered"])
	}
	if n := countFine(t, tx); n != 0 {
		t.Errorf("fine rows after recovery = %d, want 0", n)
	}
}

// TestDownsample_WithinWindowUntouched proves data inside the detailed-retention
// window is never condensed.
func TestDownsample_WithinWindowUntouched(t *testing.T) {
	db := openDownsampleTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	tx.Exec("DELETE FROM pipeline_rollups")
	tx.Exec("DELETE FROM pipeline_rollups_coarse")

	delivered := map[string]string{"direction": "inbound", "outcome": "delivered"}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	// All within the last 7 days.
	seedFine(t, tx, delivered, time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC), 9)
	seedFine(t, tx, delivered, time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC), 4)

	w := downsampleTestWorker(tx)
	if err := w.downsampleOnce(now); err != nil {
		t.Fatalf("downsampleOnce: %v", err)
	}

	if n := countCoarse(t, tx); n != 0 {
		t.Errorf("coarse rows = %d, want 0 (nothing aged out)", n)
	}
	if n := countFine(t, tx); n != 2 {
		t.Errorf("fine rows = %d, want 2 (untouched)", n)
	}
}

// TestDownsample_CoarseRetentionPrune proves the optional coarse-retention cap
// deletes coarse rows past the horizon while keeping newer ones.
func TestDownsample_CoarseRetentionPrune(t *testing.T) {
	db := openDownsampleTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	tx.Exec("DELETE FROM pipeline_rollups")
	tx.Exec("DELETE FROM pipeline_rollups_coarse")

	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	lj, _ := canonicalLabels(testMetric, map[string]string{"direction": "inbound", "outcome": "delivered"})
	mk := func(day time.Time, v float64) {
		row := models.PipelineRollupCoarse{
			MetricName: testMetric, Labels: lj, BucketStart: day.UTC(),
			BucketSeconds: 86400, Value: v,
		}
		if err := tx.Create(&row).Error; err != nil {
			t.Fatalf("seed coarse: %v", err)
		}
	}
	// Old coarse (100 days ago) and recent coarse (10 days ago).
	mk(time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), 3)
	mk(time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC), 5)

	w := downsampleTestWorker(tx)
	w.coarseRetention = 30 * 24 * time.Hour // keep only ~30 days of coarse rows
	if err := w.downsampleOnce(now); err != nil {
		t.Fatalf("downsampleOnce: %v", err)
	}

	coarse := coarseByDayOutcome(t, tx)
	if _, ok := coarse["2026-04-15/delivered"]; ok {
		t.Errorf("old coarse row past retention should be pruned, got %v", coarse)
	}
	if coarse["2026-07-14/delivered"] != 5 {
		t.Errorf("recent coarse row = %v, want 5 (within retention)", coarse["2026-07-14/delivered"])
	}
}
