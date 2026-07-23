package handlers

import (
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// openTraceTestDB connects to the unit-test Postgres and migrates message_traces.
// It skips (never fails) when no database is reachable, matching the repo's
// depless-local / DB-in-CI convention.
func openTraceTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("MessageTrace DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.MessageTrace{}); err != nil {
		t.Skipf("MessageTrace DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestMessageTrace_DeliveredVsRejected proves the schema-commitment invariant
// round-trips through the database: a delivered trace persists with a non-nil
// message_id (the FK is set only on the delivered path), and a rejected trace
// persists with a nil message_id plus the correct outcome and derived
// reason_code — the correlation key for mail that never became a Message row.
func TestMessageTrace_DeliveredVsRejected(t *testing.T) {
	gdb := openTraceTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM message_traces").Error; err != nil {
		t.Fatalf("clear message_traces: %v", err)
	}

	// Delivered: continue outcome, message_id set (as the handler does after the
	// Message row is created).
	mid := uint(1001)
	delivered := buildTrace(traceInputs{
		PipelineID: 5,
		Direction:  "inbound",
		Result: &pipeline.ExecutionResult{
			FinalAction: pipeline.ActionContinue,
			Duration:    2 * time.Millisecond,
			Steps: []pipeline.StepResult{
				{FilterName: "spf_check", Action: pipeline.ActionContinue},
				{FilterName: "dmarc_check", Action: pipeline.ActionContinue},
			},
		},
		Envelope:     pipeline.Envelope{MailFrom: "ok@remote.test", RcptTo: []string{"user@local.test"}, ClientIP: "203.0.113.1"},
		Transport:    "tls",
		RFCMessageID: "<delivered@remote.test>",
		Outcome:      outcomeDelivered,
		MessageID:    &mid,
	})

	// Rejected: DMARC reject terminal → nil message_id, reason dmarc_reject.
	rejected := buildTrace(traceInputs{
		PipelineID: 5,
		Direction:  "inbound",
		Result: &pipeline.ExecutionResult{
			FinalAction: pipeline.ActionReject,
			RejectMsg:   "DMARC policy=reject",
			Steps: []pipeline.StepResult{
				{FilterName: "dmarc_check", Action: pipeline.ActionReject, Log: pipeline.FilterLog{Result: "fail"}},
			},
		},
		Envelope:     pipeline.Envelope{MailFrom: "spoof@evil.test", RcptTo: []string{"user@local.test"}, ClientIP: "198.51.100.7"},
		Transport:    "plaintext",
		RFCMessageID: "<spoof@evil.test>",
		Outcome:      outcomeRejected,
	})

	if err := tx.Create(&delivered).Error; err != nil {
		t.Fatalf("persist delivered trace: %v", err)
	}
	if err := tx.Create(&rejected).Error; err != nil {
		t.Fatalf("persist rejected trace: %v", err)
	}

	// Reload the delivered row: message_id must survive as non-nil.
	var gotDelivered models.MessageTrace
	if err := tx.Where("outcome = ?", outcomeDelivered).First(&gotDelivered).Error; err != nil {
		t.Fatalf("reload delivered trace: %v", err)
	}
	if gotDelivered.MessageID == nil || *gotDelivered.MessageID != 1001 {
		t.Errorf("delivered MessageID = %v, want 1001 (non-nil on the delivered path)", gotDelivered.MessageID)
	}
	if gotDelivered.ReasonCode != "" {
		t.Errorf("delivered ReasonCode = %q, want empty", gotDelivered.ReasonCode)
	}
	if gotDelivered.Transport != "tls" {
		t.Errorf("delivered Transport = %q, want tls", gotDelivered.Transport)
	}
	if len(gotDelivered.Stages) == 0 {
		t.Error("delivered Stages empty, want persisted JSON")
	}

	// Reload the rejected row: message_id must be NULL; outcome + reason_code exact.
	var gotRejected models.MessageTrace
	if err := tx.Where("outcome = ?", outcomeRejected).First(&gotRejected).Error; err != nil {
		t.Fatalf("reload rejected trace: %v", err)
	}
	if gotRejected.MessageID != nil {
		t.Errorf("rejected MessageID = %v, want nil (no Message row for rejected mail)", gotRejected.MessageID)
	}
	if gotRejected.ReasonCode != string(pipeline.ReasonDMARCReject) {
		t.Errorf("rejected ReasonCode = %q, want %q", gotRejected.ReasonCode, pipeline.ReasonDMARCReject)
	}
	if gotRejected.RFCMessageID != "<spoof@evil.test>" {
		t.Errorf("rejected RFCMessageID = %q, want the correlation key", gotRejected.RFCMessageID)
	}
}
