package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// openGreylistTestDB connects to the unit-test Postgres, skipping (never
// failing) when none is reachable, per the repo's DB-test convention.
func openGreylistTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: fcEnvOr("DB_HOST", "localhost"),
		DBPort: fcEnvIntOr("DB_PORT", 5432),
		DBName: fcEnvOr("DB_NAME", "restmail"),
		DBUser: fcEnvOr("DB_USER", "restmail"),
		DBPass: fcEnvOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("greylist DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.GreylistEntry{}); err != nil {
		t.Skipf("greylist DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func buildGreylist(t *testing.T, db *gorm.DB, cfg json.RawMessage) pipeline.Filter {
	t.Helper()
	f, err := NewGreylist(db)(cfg)
	if err != nil {
		t.Fatalf("build greylist filter: %v", err)
	}
	return f
}

// greylistEmail builds an inbound email carrying the envelope fields greylisting
// keys on (sender, first recipient, client IP).
func greylistEmail(sender, rcpt, ip string) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{
			MailFrom: sender,
			RcptTo:   []string{rcpt},
			ClientIP: ip,
		},
	}
}

func loadEntry(t *testing.T, db *gorm.DB, sender, rcpt, ip string) models.GreylistEntry {
	t.Helper()
	var e models.GreylistEntry
	err := db.Where("sender = ? AND recipient = ? AND source_ip = ?", sender, rcpt, ip).First(&e).Error
	if err != nil {
		t.Fatalf("load greylist entry (%s/%s/%s): %v", sender, rcpt, ip, err)
	}
	return e
}

// uniqueTriple returns a sender/recipient/IP triple unique to this test run so
// tests never collide on the (sender, recipient, source_ip) key.
func uniqueTriple(tag string) (sender, rcpt, ip string) {
	n := time.Now().UnixNano()
	return fmt.Sprintf("s-%s-%d@ex.test", tag, n),
		fmt.Sprintf("r-%s-%d@ex.test", tag, n),
		fmt.Sprintf("10.0.%d.%d", (n/256)%256, n%256)
}

// TestGreylist_FirstContactStampsTTL proves ttl_days actually takes effect: a
// first-contact defer records an entry whose expires_at horizon reflects the
// configured ttl_days. On the pre-fix code no expires_at was written at all, so
// this fails (nil horizon) — the dead ttl_days config had no effect.
func TestGreylist_FirstContactStampsTTL(t *testing.T) {
	db := openGreylistTestDB(t)
	sender, rcpt, ip := uniqueTriple("ttl")

	f := buildGreylist(t, db, json.RawMessage(`{"delay_minutes": 5, "ttl_days": 2}`))
	before := time.Now()
	res, err := f.Execute(context.Background(), greylistEmail(sender, rcpt, ip))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("first contact action = %v, want defer", res.Action)
	}

	e := loadEntry(t, db, sender, rcpt, ip)
	t.Cleanup(func() { db.Delete(&e) })
	if e.ExpiresAt == nil {
		t.Fatal("first-contact entry has no expires_at horizon — ttl_days had no effect")
	}
	wantLo := before.Add(2*24*time.Hour - time.Minute)
	wantHi := time.Now().Add(2*24*time.Hour + time.Minute)
	if e.ExpiresAt.Before(wantLo) || e.ExpiresAt.After(wantHi) {
		t.Errorf("expires_at = %v, want ~now+2d (ttl_days=2)", e.ExpiresAt)
	}
}

// TestGreylist_ExpiredWhitelistIsReGreylisted proves a passed (auto-whitelisted)
// entry is NOT honored forever: once past its TTL horizon it is re-greylisted
// (deferred) and reset. On the pre-fix code a passed entry always continued, so
// this fails (it would pass instead of defer) — the permanent-whitelist bug.
func TestGreylist_ExpiredWhitelistIsReGreylisted(t *testing.T) {
	db := openGreylistTestDB(t)
	sender, rcpt, ip := uniqueTriple("expired")

	past := time.Now().Add(-1 * time.Hour)
	seed := models.GreylistEntry{
		Sender:     sender,
		Recipient:  rcpt,
		SourceIP:   ip,
		FirstSeen:  time.Now().Add(-48 * time.Hour),
		RetryAfter: time.Now().Add(-47 * time.Hour),
		Passed:     true,
		ExpiresAt:  &past,
		CreatedAt:  time.Now().Add(-48 * time.Hour),
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed passed entry: %v", err)
	}
	t.Cleanup(func() { db.Delete(&seed) })

	f := buildGreylist(t, db, nil)
	res, err := f.Execute(context.Background(), greylistEmail(sender, rcpt, ip))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("expired whitelist action = %v, want defer (must be reconsidered, not honored forever)", res.Action)
	}

	e := loadEntry(t, db, sender, rcpt, ip)
	if e.Passed {
		t.Error("expired entry should have been reset to passed=false")
	}
	if e.ExpiresAt == nil || !e.ExpiresAt.After(time.Now()) {
		t.Errorf("re-greylisted entry expires_at = %v, want a future horizon", e.ExpiresAt)
	}
}

// TestGreylist_UnexpiredWhitelistPasses is the counterpart guard: a passed entry
// still within its horizon continues immediately (we must not over-defer good
// senders).
func TestGreylist_UnexpiredWhitelistPasses(t *testing.T) {
	db := openGreylistTestDB(t)
	sender, rcpt, ip := uniqueTriple("fresh")

	future := time.Now().Add(24 * time.Hour)
	seed := models.GreylistEntry{
		Sender:     sender,
		Recipient:  rcpt,
		SourceIP:   ip,
		FirstSeen:  time.Now().Add(-1 * time.Hour),
		RetryAfter: time.Now().Add(-30 * time.Minute),
		Passed:     true,
		ExpiresAt:  &future,
		CreatedAt:  time.Now().Add(-1 * time.Hour),
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed passed entry: %v", err)
	}
	t.Cleanup(func() { db.Delete(&seed) })

	f := buildGreylist(t, db, nil)
	res, err := f.Execute(context.Background(), greylistEmail(sender, rcpt, ip))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionContinue {
		t.Fatalf("unexpired whitelist action = %v, want continue", res.Action)
	}
}

// TestGreylist_PassAfterDelayRestampsHorizon proves that satisfying the delay
// marks the triple passed AND stamps a fresh (future) TTL horizon, so the
// auto-whitelist is finite rather than permanent.
func TestGreylist_PassAfterDelayRestampsHorizon(t *testing.T) {
	db := openGreylistTestDB(t)
	sender, rcpt, ip := uniqueTriple("pass")

	future := time.Now().Add(10 * 24 * time.Hour)
	seed := models.GreylistEntry{
		Sender:     sender,
		Recipient:  rcpt,
		SourceIP:   ip,
		FirstSeen:  time.Now().Add(-10 * time.Minute),
		RetryAfter: time.Now().Add(-5 * time.Minute), // delay already satisfied
		Passed:     false,
		ExpiresAt:  &future,
		CreatedAt:  time.Now().Add(-10 * time.Minute),
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed pending entry: %v", err)
	}
	t.Cleanup(func() { db.Delete(&seed) })

	f := buildGreylist(t, db, json.RawMessage(`{"ttl_days": 36}`))
	res, err := f.Execute(context.Background(), greylistEmail(sender, rcpt, ip))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionContinue {
		t.Fatalf("delay-satisfied action = %v, want continue", res.Action)
	}
	e := loadEntry(t, db, sender, rcpt, ip)
	if !e.Passed {
		t.Error("entry should be marked passed after delay satisfied")
	}
	if e.ExpiresAt == nil || !e.ExpiresAt.After(time.Now().Add(35*24*time.Hour)) {
		t.Errorf("passed entry expires_at = %v, want ~now+36d", e.ExpiresAt)
	}
}

// TestGreylist_StrictModeDropsEntry proves whitelist_after_pass=false takes
// effect: satisfying the delay lets the message through but does NOT persist a
// whitelist — the entry is dropped so a later message is greylisted afresh. On
// the pre-fix code the flag was parsed but never consulted, so the entry was
// always marked passed (never dropped).
func TestGreylist_StrictModeDropsEntry(t *testing.T) {
	db := openGreylistTestDB(t)
	sender, rcpt, ip := uniqueTriple("strict")

	future := time.Now().Add(10 * 24 * time.Hour)
	seed := models.GreylistEntry{
		Sender:     sender,
		Recipient:  rcpt,
		SourceIP:   ip,
		FirstSeen:  time.Now().Add(-10 * time.Minute),
		RetryAfter: time.Now().Add(-5 * time.Minute),
		Passed:     false,
		ExpiresAt:  &future,
		CreatedAt:  time.Now().Add(-10 * time.Minute),
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed pending entry: %v", err)
	}
	t.Cleanup(func() { db.Delete(&seed) })

	f := buildGreylist(t, db, json.RawMessage(`{"whitelist_after_pass": false}`))
	res, err := f.Execute(context.Background(), greylistEmail(sender, rcpt, ip))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionContinue {
		t.Fatalf("strict-mode delay-satisfied action = %v, want continue", res.Action)
	}
	var count int64
	db.Model(&models.GreylistEntry{}).
		Where("sender = ? AND recipient = ? AND source_ip = ?", sender, rcpt, ip).
		Count(&count)
	if count != 0 {
		t.Errorf("strict mode should have dropped the entry, but %d remain", count)
	}
}

// TestGreylist_CreateErrorSurfaced proves the previously-swallowed write error is
// surfaced. A read-only transaction lets the initial lookup (SELECT) succeed but
// makes the first-contact INSERT fail; the filter must return that error so the
// engine can fail closed. On the pre-fix code the Create error was discarded and
// the filter returned a defer with a nil error.
func TestGreylist_CreateErrorSurfaced(t *testing.T) {
	db := openGreylistTestDB(t)
	sender, rcpt, ip := uniqueTriple("dberr")

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("SET TRANSACTION READ ONLY").Error; err != nil {
		t.Fatalf("set read only: %v", err)
	}

	f := buildGreylist(t, tx, nil)
	res, err := f.Execute(context.Background(), greylistEmail(sender, rcpt, ip))
	if err == nil {
		t.Fatalf("expected the greylist write error to be surfaced, got nil (action=%v) — DB error was swallowed", res.Action)
	}
}
