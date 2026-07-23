package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// TestBuildFunnel_ReshapesAllStages proves the pure reshape: every tracked
// metric family lands in its funnel stage with the right bounded labels, and
// each slice is deterministically sorted.
func TestBuildFunnel_ReshapesAllStages(t *testing.T) {
	series := []seriesSum{
		{metric: metricMessagesReceived, labels: map[string]string{"transport": "tls"}, total: 90},
		{metric: metricMessagesReceived, labels: map[string]string{"transport": "plaintext"}, total: 10},
		{metric: metricAuthVerdict, labels: map[string]string{"mechanism": "spf", "result": "pass"}, total: 80},
		{metric: metricAuthVerdict, labels: map[string]string{"mechanism": "dmarc", "result": "fail"}, total: 5},
		{metric: metricStageDecisions, labels: map[string]string{"filter": "dmarc_check", "action": "reject"}, total: 5},
		{metric: metricStageDecisions, labels: map[string]string{"filter": "spf_check", "action": "continue"}, total: 95},
		{metric: metricTerminal, labels: map[string]string{"direction": "inbound", "outcome": "delivered"}, total: 88},
		{metric: metricTerminal, labels: map[string]string{"direction": "inbound", "outcome": "rejected"}, total: 12},
		{metric: metricRejectReason, labels: map[string]string{"reason_code": "dmarc_reject"}, total: 5},
		{metric: metricRejectReason, labels: map[string]string{"reason_code": "spf_fail"}, total: 7},
	}

	f := buildFunnel(series)

	// Received sorted by transport (plaintext < tls).
	if len(f.Received) != 2 || f.Received[0].Transport != "plaintext" || f.Received[1].Transport != "tls" {
		t.Fatalf("received = %+v, want [plaintext, tls]", f.Received)
	}
	if f.Received[1].Count != 90 {
		t.Errorf("tls received count = %v, want 90", f.Received[1].Count)
	}
	// Auth verdicts sorted by mechanism then result (dmarc < spf).
	if len(f.AuthVerdicts) != 2 || f.AuthVerdicts[0].Mechanism != "dmarc" || f.AuthVerdicts[1].Mechanism != "spf" {
		t.Fatalf("auth verdicts = %+v, want dmarc then spf", f.AuthVerdicts)
	}
	// Stage decisions sorted by filter (dmarc_check < spf_check).
	if len(f.StageDecisions) != 2 || f.StageDecisions[0].Filter != "dmarc_check" {
		t.Fatalf("stage decisions = %+v, want dmarc_check first", f.StageDecisions)
	}
	// Terminal outcomes present.
	if len(f.TerminalOutcomes) != 2 {
		t.Fatalf("terminal outcomes = %+v, want 2", f.TerminalOutcomes)
	}
	// Top reject reasons sorted by count desc: spf_fail(7) before dmarc_reject(5).
	if len(f.TopRejectReasons) != 2 || f.TopRejectReasons[0].ReasonCode != "spf_fail" || f.TopRejectReasons[1].ReasonCode != "dmarc_reject" {
		t.Fatalf("top reject reasons = %+v, want spf_fail then dmarc_reject", f.TopRejectReasons)
	}
}

// TestBuildFunnel_TopRejectReasonsCapped proves the reason breakdown is capped
// and ordered by descending count.
func TestBuildFunnel_TopRejectReasonsCapped(t *testing.T) {
	var series []seriesSum
	// More distinct reason codes than the cap, each with a distinct count.
	codes := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n"}
	for i, c := range codes {
		series = append(series, seriesSum{
			metric: metricRejectReason, labels: map[string]string{"reason_code": c}, total: float64(i + 1),
		})
	}
	f := buildFunnel(series)
	if len(f.TopRejectReasons) != maxTopRejectReasons {
		t.Fatalf("top reject reasons len = %d, want cap %d", len(f.TopRejectReasons), maxTopRejectReasons)
	}
	// Highest count ("n"=14) must lead.
	if f.TopRejectReasons[0].ReasonCode != "n" {
		t.Errorf("top reason = %q, want n (highest count)", f.TopRejectReasons[0].ReasonCode)
	}
}

// TestParseAnalyticsWindow covers the ?since / ?window / default resolution.
func TestParseAnalyticsWindow(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	// Default when neither param is present.
	def := parseAnalyticsWindow(httptest.NewRequest(http.MethodGet, "/x", nil), now)
	if def.Since != now.Add(-defaultAnalyticsWindow) {
		t.Errorf("default since = %v, want %v", def.Since, now.Add(-defaultAnalyticsWindow))
	}

	// Explicit window duration.
	winReq := httptest.NewRequest(http.MethodGet, "/x?window=1h", nil)
	win := parseAnalyticsWindow(winReq, now)
	if win.Since != now.Add(-time.Hour) || win.Label != "1h" {
		t.Errorf("window=1h → since=%v label=%q, want %v / 1h", win.Since, win.Label, now.Add(-time.Hour))
	}

	// Explicit since timestamp wins and clears the label.
	sinceReq := httptest.NewRequest(http.MethodGet, "/x?since=2026-07-20T00:00:00Z&window=1h", nil)
	s := parseAnalyticsWindow(sinceReq, now)
	if !s.Since.Equal(time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)) || s.Label != "" {
		t.Errorf("since param → %v label=%q, want 2026-07-20 / empty label", s.Since, s.Label)
	}

	// Invalid window falls back to default and does not echo the bad label.
	badReq := httptest.NewRequest(http.MethodGet, "/x?window=notaduration", nil)
	bad := parseAnalyticsWindow(badReq, now)
	if bad.Since != now.Add(-defaultAnalyticsWindow) || bad.Label != defaultAnalyticsWindow.String() {
		t.Errorf("invalid window → since=%v label=%q, want default", bad.Since, bad.Label)
	}
}

// openRollupTestDB connects to the unit-test Postgres and migrates
// pipeline_rollups. It skips (never fails) when no database is reachable,
// matching the repo's depless-local / DB-in-CI convention.
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
		t.Skipf("pipeline analytics DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.PipelineRollup{}); err != nil {
		t.Skipf("pipeline analytics DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// seedRollup inserts one pipeline_rollups bucket row for the given series.
func seedRollup(t *testing.T, tx *gorm.DB, metric string, labels map[string]string, bucketStart time.Time, value float64) {
	t.Helper()
	lj, _ := json.Marshal(labels)
	row := models.PipelineRollup{
		MetricName:    metric,
		Labels:        lj,
		BucketStart:   bucketStart,
		BucketSeconds: 3600,
		Value:         value,
	}
	if err := tx.Create(&row).Error; err != nil {
		t.Fatalf("seed rollup %s %v: %v", metric, labels, err)
	}
}

// TestPipelineAnalytics_WindowSum seeds rollup buckets across labels and times
// and asserts the endpoint sums only the in-window buckets per series, excludes
// out-of-window buckets, and reshapes into the funnel.
func TestPipelineAnalytics_WindowSum(t *testing.T) {
	gdb := openRollupTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM pipeline_rollups").Error; err != nil {
		t.Fatalf("clear pipeline_rollups: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Hour)
	inA := now.Add(-1 * time.Hour) // in a 24h window
	inB := now.Add(-2 * time.Hour) // same series as inA, different bucket → sums
	out := now.Add(-48 * time.Hour) // outside a 24h window → excluded

	// tls received: two in-window buckets (30+20=50) plus one out-of-window (999).
	seedRollup(t, tx, metricMessagesReceived, map[string]string{"transport": "tls"}, inA, 30)
	seedRollup(t, tx, metricMessagesReceived, map[string]string{"transport": "tls"}, inB, 20)
	seedRollup(t, tx, metricMessagesReceived, map[string]string{"transport": "tls"}, out, 999)
	// plaintext received: one in-window bucket.
	seedRollup(t, tx, metricMessagesReceived, map[string]string{"transport": "plaintext"}, inA, 7)
	// terminal + reject reason in-window.
	seedRollup(t, tx, metricTerminal, map[string]string{"direction": "inbound", "outcome": "rejected"}, inA, 12)
	seedRollup(t, tx, metricRejectReason, map[string]string{"reason_code": "dmarc_reject"}, inA, 12)

	h := NewStatsHandler(tx)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/pipelines/analytics?window=24h", nil)
	rr := httptest.NewRecorder()
	h.GetPipelineAnalytics(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}

	var resp struct {
		Data PipelineAnalyticsResponse `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	f := resp.Data.Funnel

	// tls in-window sum = 50 (the 999 out-of-window bucket excluded); plaintext = 7.
	var tls, plaintext float64
	for _, rc := range f.Received {
		switch rc.Transport {
		case "tls":
			tls = rc.Count
		case "plaintext":
			plaintext = rc.Count
		}
	}
	if tls != 50 {
		t.Errorf("tls received = %v, want 50 (in-window sum, out-of-window excluded)", tls)
	}
	if plaintext != 7 {
		t.Errorf("plaintext received = %v, want 7", plaintext)
	}
	if len(f.TerminalOutcomes) != 1 || f.TerminalOutcomes[0].Outcome != "rejected" || f.TerminalOutcomes[0].Count != 12 {
		t.Errorf("terminal outcomes = %+v, want one rejected=12", f.TerminalOutcomes)
	}
	if len(f.TopRejectReasons) != 1 || f.TopRejectReasons[0].ReasonCode != "dmarc_reject" || f.TopRejectReasons[0].Count != 12 {
		t.Errorf("top reject reasons = %+v, want dmarc_reject=12", f.TopRejectReasons)
	}
	// Window metadata reflects the requested look-back.
	if resp.Data.Window.Label != "24h" {
		t.Errorf("window label = %q, want 24h", resp.Data.Window.Label)
	}
}
