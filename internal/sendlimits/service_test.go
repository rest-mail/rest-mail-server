package sendlimits

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for PostgreSQL send-limit proofs")
	}
	port, err := strconv.Atoi(os.Getenv("DB_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	configured := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	cfg := config.Config{DBHost: os.Getenv("DB_HOST"), DBPort: port, DBName: configured("DB_NAME", "restmail"), DBUser: configured("DB_USER", "restmail"), DBPass: configured("DB_PASS", "restmail"), DBMaxIdleConns: 2}
	database := testdb.Clone(t, &cfg, "sendlimits", testdb.Models(&models.Mailbox{}, &models.SendLimitPolicy{}, &models.SendLimitCounter{}, &models.SendLimitNotice{}, &models.AuditEntry{}, &models.Message{}))
	database.Logger = logger.Default.LogMode(logger.Silent)
	return database
}

func seed(t *testing.T, db *gorm.DB, local string, owner *uint, domain uint) models.Mailbox {
	t.Helper()
	m := models.Mailbox{DomainID: domain, Address: local + "@example.test", LocalPart: local, OwnerID: owner, Active: true}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	return m
}
func attempt(id uint, n int64) Attempt {
	return Attempt{AccountID: id, SenderID: id, Recipients: n, Entry: &models.AuditEntry{ActorKind: models.AuditActorAccount, ActorID: &id, CredentialID: ptr(uint(99)), ActorName: "agent@example.test", Method: "POST", Route: "/api/v1/messages/send", Status: 202}}
}
func ptr[T any](v T) *T { return &v }

func TestSharedOwnerBudgetPersistsAndAuditsEveryHit(t *testing.T) {
	db := testDB(t)
	owner := seed(t, db, "owner", nil, 1)
	a := seed(t, db, "a", &owner.ID, 1)
	b := seed(t, db, "b", &owner.ID, 1)
	svc := Service{Defaults: models.SendLimitValues{MessagesPerHour: 2, MessagesPerDay: 5, RecipientsPerHour: 4, RecipientsPerDay: 10}}
	for _, id := range []uint{a.ID, b.ID} {
		hit, err := svc.Reserve(db, attempt(id, 2))
		if err != nil || hit != nil {
			t.Fatalf("admission: %+v %v", hit, err)
		}
	}
	// A new service/process sees exactly the same counters. Owner and children share capacity.
	svc2 := svc
	for _, id := range []uint{owner.ID, a.ID, b.ID} {
		hit, err := svc2.Reserve(db, attempt(id, 1))
		if err != nil || hit == nil || hit.OwnerID != owner.ID {
			t.Fatalf("shared hit: %+v %v", hit, err)
		}
	}
	var entries []models.AuditEntry
	db.Order("seq").Find(&entries)
	if len(entries) != 3 {
		t.Fatalf("audit hits=%d want 3", len(entries))
	}
	for _, e := range entries {
		if e.Action != "send_limits.hit" || e.CredentialID == nil || *e.CredentialID != 99 || e.ActorID == nil {
			t.Fatalf("lost attribution: %+v", e)
		}
	}
	if got := audit.Verify(entries); !got.OK {
		t.Fatalf("broken audit: %+v", got)
	}
	var notices int64
	db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", owner.ID, "INBOX").Count(&notices)
	if notices != 1 {
		t.Fatalf("owner inbox notices=%d, want one coalesced alert", notices)
	}
}

func TestConcurrentBudgetReservationCannotOvershoot(t *testing.T) {
	db := testDB(t)
	owner := seed(t, db, "owner", nil, 1)
	a := seed(t, db, "a", &owner.ID, 1)
	svc := Service{Defaults: models.SendLimitValues{MessagesPerHour: 3}}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hit, err := svc.Reserve(db, attempt(a.ID, 1))
			if err != nil {
				t.Error(err)
			} else if hit == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 3 {
		t.Fatalf("accepted %d, want 3", got)
	}
}

func TestLowerOverridesDomainServerAndRollover(t *testing.T) {
	db := testDB(t)
	owner := seed(t, db, "owner", nil, 1)
	a := seed(t, db, "a", &owner.ID, 1)
	other := seed(t, db, "other", nil, 1)
	far := seed(t, db, "far", nil, 2)
	now := time.Date(2026, 10, 28, 10, 30, 0, 0, time.UTC)
	svc := Service{Defaults: models.SendLimitValues{MessagesPerHour: 10, MessagesPerDay: 20}, Domain: models.SendLimitValues{RecipientsPerHour: 3}, Server: models.SendLimitValues{RecipientsPerDay: 4}, Now: func() time.Time { return now }}
	if err := svc.SetAccount(db, a.ID, models.SendLimitValues{MessagesPerHour: 11}); err == nil {
		t.Fatal("accepted child limit above parent")
	}
	if err := svc.SetAccount(db, a.ID, models.SendLimitValues{MessagesPerHour: 1, MessagesPerDay: 2}); err != nil {
		t.Fatal(err)
	}
	hit, err := svc.Reserve(db, attempt(a.ID, 2))
	if err != nil || hit != nil {
		t.Fatalf("first: %+v %v", hit, err)
	}
	hit, err = svc.Reserve(db, attempt(a.ID, 1))
	if err != nil || hit == nil {
		t.Fatalf("child cap missing: %+v %v", hit, err)
	}
	hit, err = svc.Reserve(db, attempt(other.ID, 2))
	if err != nil || hit == nil || hit.Scope != "domain:1" {
		t.Fatalf("domain cap: %+v %v", hit, err)
	}
	hit, err = svc.Reserve(db, attempt(far.ID, 2))
	if err != nil || hit != nil {
		t.Fatalf("other domain: %+v %v", hit, err)
	}
	now = now.Add(time.Hour)
	hit, err = svc.Reserve(db, attempt(a.ID, 1))
	if err != nil || hit == nil || hit.Scope != "*" {
		t.Fatalf("server day cap survived hour: %+v %v", hit, err)
	}
	now = now.Add(24 * time.Hour)
	hit, err = svc.Reserve(db, attempt(a.ID, 1))
	if err != nil || hit != nil {
		t.Fatalf("rollover: %+v %v", hit, err)
	}
}

func TestDelegatedSenderLimitAlertsItsOwner(t *testing.T) {
	db := testDB(t)
	actor := seed(t, db, "actor", nil, 1)
	owner := seed(t, db, "senderowner", nil, 2)
	sender := seed(t, db, "sender", &owner.ID, 2)
	svc := Service{Defaults: models.SendLimitValues{MessagesPerHour: 10}}
	if err := svc.SetAccount(db, owner.ID, models.SendLimitValues{MessagesPerHour: 1}); err != nil {
		t.Fatal(err)
	}
	a := attempt(actor.ID, 1)
	a.SenderID = sender.ID
	if hit, err := svc.Reserve(db, a); err != nil || hit != nil {
		t.Fatalf("first: %+v %v", hit, err)
	}
	hit, err := svc.Reserve(db, a)
	if err != nil || hit == nil || hit.OwnerID != owner.ID {
		t.Fatalf("wrong owner alerted: %+v %v", hit, err)
	}
	var notice models.Message
	if err := db.Where("mailbox_id = ?", owner.ID).First(&notice).Error; err != nil {
		t.Fatal(err)
	}
	// getMessage links a message to its thread, so the notice needs one to link to.
	if notice.MsgID == "" || notice.ThreadID != notice.MsgID {
		t.Fatalf("notice message_id %q, thread_id %q; want one thread named by its own Message-ID", notice.MsgID, notice.ThreadID)
	}
}

func TestDelegationCannotRotateSendingDomainsPastAccountDomainBudget(t *testing.T) {
	db := testDB(t)
	actor := seed(t, db, "actor", nil, 1)
	first := seed(t, db, "first", nil, 2)
	second := seed(t, db, "second", nil, 3)
	svc := Service{Defaults: models.SendLimitValues{MessagesPerHour: 10}, Domain: models.SendLimitValues{MessagesPerHour: 1}}
	a := attempt(actor.ID, 1)
	a.SenderID = first.ID
	if hit, err := svc.Reserve(db, a); err != nil || hit != nil {
		t.Fatalf("first: %+v %v", hit, err)
	}
	a.SenderID = second.ID
	if hit, err := svc.Reserve(db, a); err != nil || hit == nil || hit.Scope != "domain:1" {
		t.Fatalf("delegation bypassed actor domain ceiling: %+v %v", hit, err)
	}
}
