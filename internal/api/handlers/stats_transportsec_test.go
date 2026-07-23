package handlers

import (
	"os"
	"strconv"
	"testing"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// TestBuildInboundTransportSecurity checks the pure count→DTO assembly: field
// mapping and TLS/plaintext percentage derivation with one-decimal rounding.
func TestBuildInboundTransportSecurity(t *testing.T) {
	got := buildInboundTransportSecurity(inboundTLSCounts{
		TotalInboundMX:    4,
		OverTLS:           1,
		Plaintext:         3,
		PlaintextAuthPass: 2,
		PlaintextAuthFail: 1,
	})
	if got.TotalInboundMX != 4 || got.OverTLS != 1 || got.Plaintext != 3 {
		t.Errorf("counts = %+v, want total=4 tls=1 plaintext=3", got)
	}
	if got.PlaintextAuthPass != 2 || got.PlaintextAuthFail != 1 {
		t.Errorf("plaintext auth split = %d/%d, want 2/1", got.PlaintextAuthPass, got.PlaintextAuthFail)
	}
	if got.TLSPercent != 25.0 {
		t.Errorf("TLSPercent = %v, want 25.0", got.TLSPercent)
	}
	if got.PlaintextPercent != 75.0 {
		t.Errorf("PlaintextPercent = %v, want 75.0", got.PlaintextPercent)
	}
}

// TestBuildInboundTransportSecurity_Rounding verifies percentages round to one
// decimal rather than emitting a long float or NaN.
func TestBuildInboundTransportSecurity_Rounding(t *testing.T) {
	got := buildInboundTransportSecurity(inboundTLSCounts{TotalInboundMX: 3, OverTLS: 1, Plaintext: 2})
	if got.TLSPercent != 33.3 {
		t.Errorf("TLSPercent = %v, want 33.3", got.TLSPercent)
	}
	if got.PlaintextPercent != 66.7 {
		t.Errorf("PlaintextPercent = %v, want 66.7", got.PlaintextPercent)
	}
}

// TestBuildInboundTransportSecurity_ZeroDenominator guards the no-inbound case:
// percentages must be 0, never a divide-by-zero NaN.
func TestBuildInboundTransportSecurity_ZeroDenominator(t *testing.T) {
	got := buildInboundTransportSecurity(inboundTLSCounts{})
	if got.TLSPercent != 0 || got.PlaintextPercent != 0 {
		t.Errorf("percentages = %v/%v, want 0/0 for empty input", got.TLSPercent, got.PlaintextPercent)
	}
}

// openTransportSecTestDB connects to the Postgres provisioned for unit tests
// (CI sets DB_*; defaults match a local `postgres` container). It skips — never
// fails — when no database is reachable, so the depless local gate stays green
// while CI exercises the real aggregation SQL against seeded rows.
func openTransportSecTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("inbound transport-security DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Message{}); err != nil {
		t.Skipf("inbound transport-security DB test skipped: migrate failed (%v)", err)
	}
	return gdb
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

// TestInboundTransportSecurity_Aggregation seeds a representative mix of message
// rows and asserts the real aggregate SQL splits them into
// TLS/plaintext/auth-pass/auth-fail correctly and excludes non inbound-MX rows.
func TestInboundTransportSecurity_Aggregation(t *testing.T) {
	gdb := openTransportSecTestDB(t)

	// Run in a transaction that starts from a clean messages table and rolls
	// back, so the test is isolated and leaves no residue.
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM messages").Error; err != nil {
		t.Fatalf("clear messages: %v", err)
	}

	tru, fls := true, false
	seed := []models.Message{
		// Inbound-MX over TLS.
		{MailboxID: 1, Sender: "a@remote.test", ReceivedTLS: &tru, TLSVersion: "TLS1.3", RawMessage: "Subject: ok\r\n\r\nhi"},
		// Inbound-MX plaintext, SPF passed → auth pass.
		{MailboxID: 1, Sender: "b@remote.test", ReceivedTLS: &fls, RawMessage: "Authentication-Results: mx; spf=pass smtp.mailfrom=remote.test\r\n\r\nhi"},
		// Inbound-MX plaintext, DKIM passed → auth pass.
		{MailboxID: 1, Sender: "c@remote.test", ReceivedTLS: &fls, RawMessage: "Authentication-Results: restmail; dkim=pass header.d=remote.test\r\n\r\nhi"},
		// Inbound-MX plaintext, nothing passed → auth fail.
		{MailboxID: 1, Sender: "d@remote.test", ReceivedTLS: &fls, RawMessage: "Authentication-Results: mx; spf=fail; dkim=none\r\n\r\nhi"},
		// NOT inbound-MX (nil) → must be excluded from the denominator entirely.
		{MailboxID: 1, Sender: "e@local.test", ReceivedTLS: nil, RawMessage: "Authentication-Results: mx; spf=pass\r\n\r\nhi"},
	}
	for i := range seed {
		if err := tx.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	h := NewStatsHandler(tx)
	got, err := h.getInboundTransportSecurity()
	if err != nil {
		t.Fatalf("getInboundTransportSecurity: %v", err)
	}

	if got.TotalInboundMX != 4 {
		t.Errorf("TotalInboundMX = %d, want 4 (nil row excluded)", got.TotalInboundMX)
	}
	if got.OverTLS != 1 {
		t.Errorf("OverTLS = %d, want 1", got.OverTLS)
	}
	if got.Plaintext != 3 {
		t.Errorf("Plaintext = %d, want 3", got.Plaintext)
	}
	if got.PlaintextAuthPass != 2 {
		t.Errorf("PlaintextAuthPass = %d, want 2 (spf=pass + dkim=pass)", got.PlaintextAuthPass)
	}
	if got.PlaintextAuthFail != 1 {
		t.Errorf("PlaintextAuthFail = %d, want 1", got.PlaintextAuthFail)
	}
	if got.TLSPercent != 25.0 {
		t.Errorf("TLSPercent = %v, want 25.0", got.TLSPercent)
	}
	if got.PlaintextPercent != 75.0 {
		t.Errorf("PlaintextPercent = %v, want 75.0", got.PlaintextPercent)
	}
}

// TestInboundTransportSecurity_Persistence proves the additive model fields
// round-trip through the database: a true flag persists as true, and an unset
// flag persists as NULL (not false), preserving the "not applicable" distinction.
func TestInboundTransportSecurity_Persistence(t *testing.T) {
	gdb := openTransportSecTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	tru := true
	m := models.Message{MailboxID: 1, Sender: "a@remote.test", ReceivedTLS: &tru, TLSVersion: "TLS1.2", RawMessage: "x"}
	if err := tx.Create(&m).Error; err != nil {
		t.Fatalf("create tls message: %v", err)
	}
	var gotTLS models.Message
	if err := tx.First(&gotTLS, m.ID).Error; err != nil {
		t.Fatalf("reload tls message: %v", err)
	}
	if gotTLS.ReceivedTLS == nil || !*gotTLS.ReceivedTLS {
		t.Errorf("ReceivedTLS = %v, want persisted true", gotTLS.ReceivedTLS)
	}
	if gotTLS.TLSVersion != "TLS1.2" {
		t.Errorf("TLSVersion = %q, want TLS1.2", gotTLS.TLSVersion)
	}

	// Unset (non inbound-MX) delivery → NULL, not false.
	n := models.Message{MailboxID: 1, Sender: "b@local.test", RawMessage: "y"}
	if err := tx.Create(&n).Error; err != nil {
		t.Fatalf("create nil message: %v", err)
	}
	var gotNil models.Message
	if err := tx.First(&gotNil, n.ID).Error; err != nil {
		t.Fatalf("reload nil message: %v", err)
	}
	if gotNil.ReceivedTLS != nil {
		t.Errorf("ReceivedTLS = %v, want NULL for a non inbound-MX delivery", *gotNil.ReceivedTLS)
	}
}
