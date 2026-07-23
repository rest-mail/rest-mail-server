package rollup

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// TestDelta covers the per-bucket increment computation, including counter-reset
// handling — the crux of accuracy across snapshots and process restarts.
func TestDelta(t *testing.T) {
	cases := []struct {
		name          string
		current, last float64
		want          float64
	}{
		{"normal increment", 10, 4, 6},
		{"first observation (watermark 0)", 7, 0, 7},
		{"no change", 5, 5, 0},
		{"counter reset (current < last)", 3, 10, 3},
		{"reset to zero", 0, 8, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := delta(tc.current, tc.last); got != tc.want {
				t.Errorf("delta(%v, %v) = %v, want %v", tc.current, tc.last, got, tc.want)
			}
		})
	}
}

// TestCanonicalLabels confirms label JSON is sorted-key canonical and the series
// key is deterministic regardless of map insertion order.
func TestCanonicalLabels(t *testing.T) {
	a, ka := canonicalLabels("m", map[string]string{"outcome": "delivered", "direction": "inbound"})
	b, kb := canonicalLabels("m", map[string]string{"direction": "inbound", "outcome": "delivered"})
	if string(a) != string(b) || ka != kb {
		t.Errorf("canonicalLabels not order-independent: %q/%q vs %q/%q", a, ka, b, kb)
	}
	if string(a) != `{"direction":"inbound","outcome":"delivered"}` {
		t.Errorf("labels JSON = %s, want sorted-key form", a)
	}
}

// ── DB-backed (skip when no database is reachable) ───────────────────

func openRollupTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("rollup DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.PipelineRollup{}); err != nil {
		t.Skipf("rollup DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// terminalCounter builds a fresh registry with a restmail_pipeline_terminal_total
// counter, so the worker snapshots a controlled series set (isolated from the
// process-default registry the real metrics register into).
func terminalCounter() (*prometheus.Registry, *prometheus.CounterVec) {
	reg := prometheus.NewRegistry()
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "restmail_pipeline_terminal_total",
		Help: "test",
	}, []string{"direction", "outcome"})
	reg.MustRegister(cv)
	return reg, cv
}

// rollupValues returns the current pipeline_rollups values for the terminal
// metric, keyed by outcome, for assertions.
func rollupValues(t *testing.T, db *gorm.DB) map[string]float64 {
	t.Helper()
	var rows []models.PipelineRollup
	if err := db.Where("metric_name = ?", "restmail_pipeline_terminal_total").Find(&rows).Error; err != nil {
		t.Fatalf("load rollups: %v", err)
	}
	out := map[string]float64{}
	for _, r := range rows {
		var labels map[string]string
		if err := json.Unmarshal(r.Labels, &labels); err != nil {
			t.Fatalf("decode labels %s: %v", r.Labels, err)
		}
		out[labels["outcome"]] = r.Value
	}
	return out
}

// TestWorker_RollupSnapshots is the end-to-end accuracy proof: after pipeline
// counter increments and a rollup tick, pipeline_rollups holds counts matching
// the counters. It also proves delta-across-snapshots, idempotent re-runs (no
// double count), and counter-reset handling.
func TestWorker_RollupSnapshots(t *testing.T) {
	gdb := openRollupTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM pipeline_rollups").Error; err != nil {
		t.Fatalf("clear pipeline_rollups: %v", err)
	}

	reg, cv := terminalCounter()
	w := NewWorker(tx, 5*time.Minute)
	w.gatherer = reg

	now := time.Date(2026, 7, 23, 12, 3, 0, 0, time.UTC) // falls in the 12:00 bucket

	// Snapshot 1: 5 delivered, 2 rejected.
	cv.WithLabelValues("inbound", "delivered").Add(5)
	cv.WithLabelValues("inbound", "rejected").Add(2)
	if err := w.rollupOnce(now); err != nil {
		t.Fatalf("rollupOnce #1: %v", err)
	}
	got := rollupValues(t, tx)
	if got["delivered"] != 5 || got["rejected"] != 2 {
		t.Fatalf("after snapshot 1: rollups = %v, want delivered=5 rejected=2", got)
	}

	// Snapshot 2: +3 delivered (cumulative 8). Same bucket → delta accumulates.
	cv.WithLabelValues("inbound", "delivered").Add(3)
	if err := w.rollupOnce(now); err != nil {
		t.Fatalf("rollupOnce #2: %v", err)
	}
	got = rollupValues(t, tx)
	if got["delivered"] != 8 || got["rejected"] != 2 {
		t.Fatalf("after snapshot 2: rollups = %v, want delivered=8 rejected=2", got)
	}

	// Idempotent re-run: no counter change → zero delta → no double count.
	if err := w.rollupOnce(now); err != nil {
		t.Fatalf("rollupOnce #3 (idempotent): %v", err)
	}
	got = rollupValues(t, tx)
	if got["delivered"] != 8 || got["rejected"] != 2 {
		t.Fatalf("after idempotent re-run: rollups = %v, want delivered=8 rejected=2 (no double count)", got)
	}
	// Exactly two series rows exist (delivered + rejected), not duplicated.
	var rowCount int64
	if err := tx.Model(&models.PipelineRollup{}).
		Where("metric_name = ? AND bucket_start = ?", "restmail_pipeline_terminal_total", now.UTC().Truncate(5*time.Minute)).
		Count(&rowCount).Error; err != nil {
		t.Fatalf("count rollup rows: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("rollup rows in bucket = %d, want 2 (one per series, upsert not insert)", rowCount)
	}

	// Counter reset: a brand-new registry (counters restart at low values) is
	// below the watermark (delivered=8). delta() treats current as the delta, so
	// the bucket gains the post-reset increment rather than a negative swing.
	reg2, cv2 := terminalCounter()
	w.gatherer = reg2
	cv2.WithLabelValues("inbound", "delivered").Add(1)
	if err := w.rollupOnce(now); err != nil {
		t.Fatalf("rollupOnce #4 (reset): %v", err)
	}
	got = rollupValues(t, tx)
	if got["delivered"] != 9 { // 8 + reset-delta 1
		t.Errorf("after counter reset: delivered = %v, want 9 (8 + 1 post-reset)", got["delivered"])
	}
}

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
