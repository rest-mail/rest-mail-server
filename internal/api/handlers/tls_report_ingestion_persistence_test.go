package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// This regression uses only a verified fresh scratch database. Without local
// PostgreSQL it skips rather than writing to the shared test database.
func TestTLSIngestionPersistence(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.TLSReport{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"restmail.test", "example.com"} {
		if err := db.Create(&models.Domain{Name: name, Active: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	raw := `{"organization-name":"Fixture Reporter","x-reporter":{"kept":true},"date-range":{"start-datetime":"2026-10-16T00:00:00Z","end-datetime":"2026-10-16T23:59:59Z"},"policies":[{"policy":{"policy-domain":"restmail.test","policy-type":"sts"},"summary":{"total-successful-session-count":12,"total-failure-session-count":1},"failure-details":[{"result-type":"certificate-expired","x-reason":true}]},{"policy":{"policy-domain":"unknown.test","policy-type":"sts"}},{"policy":{"policy-domain":"example.com.","policy-type":"no-policy-found"}}]}`
	var report TLSRPTReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	h := NewTLSReportHandler(db)
	r := httptest.NewRequest("POST", tlsReportPath, strings.NewReader(raw))
	count := func() int64 {
		t.Helper()
		var n int64
		if err := db.Model(&models.TLSReport{}).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	const callback = "tls_ingestion_second_policy_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*models.TLSReport); ok && row.PolicyDomain == "example.com" {
			_ = tx.AddError(errors.New("private late report insertion detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if _, err := h.receiveReport(r, receiveTLSReportInput{Report: &report}); err == nil {
		t.Fatal("late report insertion failure reported success")
	}
	if count() != 0 {
		t.Fatal("late failure left an earlier policy row committed")
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err := h.receiveReport(httptest.NewRequest("POST", tlsReportPath, strings.NewReader(raw)), receiveTLSReportInput{Report: &report})
	end := time.Now()
	if err != nil || result.Stored != 2 {
		t.Fatalf("stored %d policies: %v", result.Stored, err)
	}
	var rows []models.TLSReport
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].PolicyDomain != "restmail.test" || rows[0].PolicyType != "sts" || rows[0].TotalSuccessful != 12 || rows[0].TotalFailure != 1 || rows[1].PolicyDomain != "example.com" || rows[1].PolicyType != "no-policy" || !strings.Contains(string(rows[0].FailureDetails), `"x-reason": true`) && !strings.Contains(string(rows[0].FailureDetails), `"x-reason":true`) {
		t.Fatal("stored policy fields, unknown-domain skip or extension details changed")
	}
	for _, row := range rows {
		if row.RawReport != raw || row.ReportingOrg != "Fixture Reporter" || row.StartDate.Format(time.RFC3339) != "2026-10-16T00:00:00Z" || row.EndDate.Format(time.RFC3339) != "2026-10-16T23:59:59Z" || row.ReceivedAt.Before(start) || row.ReceivedAt.After(end) {
			t.Fatal("persisted original report or timestamps changed")
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	if _, err := h.receiveReport(httptest.NewRequest("POST", tlsReportPath, strings.NewReader(raw)).WithContext(ctx), receiveTLSReportInput{Report: &report}); err == nil || count() != 2 {
		t.Fatal("cancelled HTTP report wrote policies or reported success")
	}
}
