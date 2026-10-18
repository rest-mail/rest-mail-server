package queue

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func openSuppressionTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("suppression DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Message{},
		&models.OutboundQueue{}, &models.Suppression{}); err != nil {
		t.Skipf("suppression DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// sendingDomain creates a domain hosted here with one mailbox to send from,
// and removes both, and everything sent from them, when the test ends.
func sendingDomain(t *testing.T, gdb *gorm.DB) (models.Domain, models.Mailbox) {
	t.Helper()
	d := models.Domain{Name: fmt.Sprintf("supp-%d.test", time.Now().UnixNano()), Active: true}
	if err := gdb.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	mb := models.Mailbox{DomainID: d.ID, LocalPart: "alice", Address: "alice@" + d.Name, Active: true}
	if err := gdb.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gdb.Exec("DELETE FROM messages WHERE mailbox_id = ?", mb.ID)
		gdb.Exec("DELETE FROM outbound_queue WHERE sender = ?", mb.Address)
		gdb.Exec("DELETE FROM suppressions WHERE domain_id = ?", d.ID)
		gdb.Delete(&mb)
		gdb.Delete(&d)
	})
	return d, mb
}

// claimedFrom puts a message from sender to recipient on the queue as the
// claim leaves it.
func claimedFrom(t *testing.T, gdb *gorm.DB, sender, recipient string) models.OutboundQueue {
	t.Helper()
	now := time.Now()
	item := models.OutboundQueue{
		Sender: sender, Recipient: recipient, Domain: recipient[strings.LastIndexByte(recipient, '@')+1:],
		RawMessage: "Subject: hi\r\n\r\nhi\r\n",
		Status:     "delivering", Attempts: 1, MaxRetries: 30, LastAttempt: &now,
		ExpiresAt: now.Add(72 * time.Hour),
	}
	if err := gdb.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func suppressionOf(t *testing.T, gdb *gorm.DB, domainID uint, address string) (models.Suppression, bool) {
	t.Helper()
	var s models.Suppression
	res := gdb.Where("domain_id = ? AND address = ?", domainID, address).Limit(1).Find(&s)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	return s, res.RowsAffected > 0
}

// A bounce saying the recipient doesn't exist suppresses it for the sending
// domain, lower-cased, for the configured time. Any other failure doesn't.
func TestRecordFailure_SuppressesOnlyRecipientsThatDoNotExist(t *testing.T) {
	gdb := openSuppressionTestDB(t)
	d, mb := sendingDomain(t, gdb)
	w := &Worker{db: gdb, hostname: "mx.supp.test", suppressionTTL: 30 * 24 * time.Hour}

	cases := []struct {
		name     string
		err      error
		suppress bool
	}{
		{"550 5.1.1 no such mailbox", rcptRefusal(550, "5.1.1 The email account that you tried to reach does not exist."), true},
		{"550 5.1.10 Exchange unknown recipient", rcptRefusal(550, "5.1.10 RESOLVER.ADR.RecipientNotFound; Recipient not found by SMTP address lookup"), true},
		{"550 5.1.2 no such domain", rcptRefusal(550, "5.1.2 Host unknown"), true},
		{"null MX", fmt.Errorf("all MX hosts failed: %w", error(&SMTPError{Code: 556, Enhanced: "5.1.10", Message: "recipient domain accepts no mail (null MX)"})), true},
		{"NXDOMAIN", fmt.Errorf("MX lookup: %w", &net.DNSError{Err: "no such host", Name: "nowhere.invalid", IsNotFound: true}), true},
		{"550 5.7.1 policy", rcptRefusal(550, "5.7.1 Message rejected as spam"), false},
		{"552 5.2.2 mailbox full", rcptRefusal(552, "5.2.2 Mailbox full"), false},
		{"450 4.1.1 temporary", rcptRefusal(450, "4.1.1 Recipient address rejected: unverified address"), false},
		{"451 4.7.1 greylisted", rcptRefusal(451, "4.7.1 Greylisted, please try again later"), false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recipient := fmt.Sprintf("Bob.%d@Remote.Test", i)
			item := claimedFrom(t, gdb, mb.Address, recipient)
			now := time.Now()
			w.recordFailure(item, tc.err, now)

			s, ok := suppressionOf(t, gdb, d.ID, strings.ToLower(recipient))
			if ok != tc.suppress {
				t.Fatalf("suppressed = %v, want %v (category %s)", ok, tc.suppress, ClassifyFailure(tc.err))
			}
			if !ok {
				return
			}
			if s.Reason != models.SuppressionHardBounce || !s.FailureCategory.Suppresses() {
				t.Errorf("reason %q, category %q", s.Reason, s.FailureCategory)
			}
			if got := s.ExpiresAt.Sub(now); got < 30*24*time.Hour-time.Minute || got > 30*24*time.Hour+time.Minute {
				t.Errorf("expires in %v, want 30 days", got)
			}
		})
	}
}

// A message to a suppressed recipient is bounced without being tried, with a
// DSN that says the address is suppressed and until when. Mail to the same
// address from another domain is not affected.
func TestProcess_SuppressedRecipientBouncesWithoutAnAttempt(t *testing.T) {
	gdb := openSuppressionTestDB(t)
	d, mb := sendingDomain(t, gdb)
	_, other := sendingDomain(t, gdb)
	w := &Worker{db: gdb, hostname: "mx.supp.test", suppressionTTL: DefaultSuppressionTTL}

	until := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	if err := gdb.Create(&models.Suppression{DomainID: d.ID, Address: "gone@remote.invalid",
		Reason: models.SuppressionHardBounce, FailureCategory: models.FailureBadMailbox, ExpiresAt: until}).Error; err != nil {
		t.Fatal(err)
	}

	// The recipient is matched without regard to case.
	item := claimedFrom(t, gdb, mb.Address, "Gone@Remote.Invalid")
	w.process(item, time.Now())

	var got models.OutboundQueue
	gdb.First(&got, item.ID)
	if got.Status != "bounced" || got.Attempts != 0 || !strings.HasPrefix(got.LastError, "not sent:") ||
		got.FailureCategory != models.FailureBadMailbox {
		t.Fatalf("row = status %q, attempts %d, category %q, error %q; want bounced without an attempt",
			got.Status, got.Attempts, got.FailureCategory, got.LastError)
	}

	var dsn models.Message
	if err := gdb.Where("mailbox_id = ? AND is_dsn = ?", mb.ID, true).First(&dsn).Error; err != nil {
		t.Fatalf("no DSN for the sender: %v", err)
	}
	wantUntil := until.UTC().Format("2 January 2006 15:04 MST")
	if !strings.Contains(dsn.RawMessage, "suppressed until "+wantUntil) || !strings.Contains(dsn.RawMessage, "Status: 5.1.1") {
		t.Errorf("DSN doesn't say the address is suppressed until %s:\n%s", wantUntil, dsn.RawMessage)
	}
	if strings.Contains(dsn.RawMessage, "has been attempted") {
		t.Errorf("DSN claims an attempt was made:\n%s", dsn.RawMessage)
	}

	// The other domain's suppressions are its own.
	if _, ok := w.suppression(claimedFrom(t, gdb, other.Address, "gone@remote.invalid"), time.Now()); ok {
		t.Error("another domain's mail to the address is suppressed too")
	}
}

// An expired suppression no longer suppresses; a new bounce starts it again.
// A cleared one no longer suppresses either.
func TestSuppression_ExpiryAndClearing(t *testing.T) {
	gdb := openSuppressionTestDB(t)
	d, mb := sendingDomain(t, gdb)
	w := &Worker{db: gdb, hostname: "mx.supp.test", suppressionTTL: DefaultSuppressionTTL}
	now := time.Now()

	s := models.Suppression{DomainID: d.ID, Address: "gone@remote.test", Reason: models.SuppressionHardBounce,
		FailureCategory: models.FailureBadMailbox, ExpiresAt: now.Add(-time.Minute)}
	if err := gdb.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	item := claimedFrom(t, gdb, mb.Address, "gone@remote.test")
	if _, ok := w.suppression(item, now); ok {
		t.Fatal("an expired suppression still suppresses")
	}

	w.recordFailure(item, rcptRefusal(550, "5.1.1 User unknown"), now)
	got, ok := w.suppression(item, now)
	if !ok || got.ID != s.ID || got.ExpiresAt.Before(now.Add(DefaultSuppressionTTL-time.Minute)) {
		t.Fatalf("after a new bounce: suppressed %v, row %d (want %d), until %v", ok, got.ID, s.ID, got.ExpiresAt)
	}

	if err := gdb.Delete(&models.Suppression{}, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := w.suppression(item, now); ok {
		t.Fatal("a cleared suppression still suppresses")
	}
}

// The null sender, or a sender whose domain isn't hosted here, has no
// suppression list.
func TestSuppress_SenderDomainNotHostedHere(t *testing.T) {
	gdb := openSuppressionTestDB(t)
	w := &Worker{db: gdb, hostname: "mx.supp.test", suppressionTTL: DefaultSuppressionTTL}
	recipient := fmt.Sprintf("gone-%d@remote.test", time.Now().UnixNano())
	for _, sender := range []string{"", "someone@not-hosted.invalid"} {
		w.suppress(models.OutboundQueue{Sender: sender, Recipient: recipient}, models.FailureBadMailbox, "550 5.1.1", time.Now())
		var n int64
		gdb.Model(&models.Suppression{}).Where("address = ?", recipient).Count(&n)
		if n != 0 {
			t.Errorf("sender %q: %d suppressions recorded", sender, n)
		}
	}
}
