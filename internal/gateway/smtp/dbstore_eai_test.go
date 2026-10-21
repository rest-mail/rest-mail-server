package smtp

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/bootstrap"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

func TestDBStore_SenderMatchesAccount(t *testing.T) {
	db := openStoreTestDB(t).Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	t.Cleanup(func() { db.Rollback() })

	unicodeDomain := fmt.Sprintf("bücher-identity-%d.test", time.Now().UnixNano())
	asciiDomain, err := rmail.DomainASCII(unicodeDomain)
	if err != nil {
		t.Fatal(err)
	}
	dom := models.Domain{Name: asciiDomain, Active: true}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatal(err)
	}
	seed := func(local string) models.Mailbox {
		t.Helper()
		mb := models.Mailbox{DomainID: dom.ID, LocalPart: local, Address: local + "@" + asciiDomain, Active: true}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatal(err)
		}
		return mb
	}
	sender, other := seed("josé"), seed("ana")
	legacy := seed("cafe\u0301")
	store := NewStore(db, "")
	for _, tc := range []struct {
		address string
		account uint
		want    bool
	}{
		{sender.Address, sender.ID, true},
		{"josé@" + unicodeDomain, sender.ID, true},
		{"JOSE\u0301@" + unicodeDomain, sender.ID, true},
		{"café@" + unicodeDomain, legacy.ID, true},
		{other.Address, sender.ID, false},
		{"unknown@" + unicodeDomain, sender.ID, false},
		{"bad\xff@" + unicodeDomain, sender.ID, false},
		{"josé@" + unicodeDomain, 0, false},
	} {
		ok, err := store.SenderMatchesAccount(tc.account, tc.address)
		if err != nil || ok != tc.want {
			t.Errorf("SenderMatchesAccount(%d, %q) = %v, %v; want %v", tc.account, tc.address, ok, err, tc.want)
		}
	}

	// Two pre-existing NFC-equivalent accounts remain distinct. The exact
	// decomposed identity must never authorize a session for the composed one.
	twin := seed("jose\u0301")
	for _, account := range []uint{sender.ID, twin.ID} {
		ok, err := store.SenderMatchesAccount(account, "jose\u0301@"+unicodeDomain)
		if err != nil || ok != (account == twin.ID) {
			t.Errorf("legacy twin for %d = %v, %v", account, ok, err)
		}
	}
	first, second := seed("a\u0301\u0323"), seed("a\u0323\u0301")
	for _, account := range []uint{first.ID, second.ID} {
		ok, err := store.SenderMatchesAccount(account, "ạ\u0301@"+unicodeDomain)
		if err != nil || ok {
			t.Errorf("ambiguous identity for %d = %v, %v; want false, nil", account, ok, err)
		}
	}

	// Unexpected lookup errors are surfaced to the session to fail closed.
	broken := &dbStore{db: db.Session(&gorm.Session{NewDB: true})}
	lookupErr := errors.New("identity lookup unavailable")
	_ = broken.db.AddError(lookupErr)
	if ok, err := broken.SenderMatchesAccount(sender.ID, sender.Address); ok || !errors.Is(err, lookupErr) {
		t.Errorf("lookup failure = %v, %v; want false, lookup error", ok, err)
	}
}

// openStoreRBACTestDB is openStoreTestDB with the full schema and the built-in
// roles, which SenderAuthorized reads grants through.
func openStoreRBACTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Skipf("smtp dbStore test skipped: migrate failed (%v)", err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatalf("roles: %v", err)
	}
	return db
}

// TestDBStore_EAISender proves submission resolves a hosted sender's identity
// the way sign-in and delivery do (#153): a Unicode domain, upper-case or
// decomposed local part reaches the stored A-label, NFC account, while the
// submitted spelling is kept on the message.
func TestDBStore_EAISender(t *testing.T) {
	db := openStoreRBACTestDB(t)

	suffix := time.Now().UnixNano()
	unicodeDomain := fmt.Sprintf("bücher-%d.test", suffix)
	asciiDomain, err := rmail.DomainASCII(unicodeDomain)
	if err != nil {
		t.Fatalf("DomainASCII: %v", err)
	}
	stored := "josé@" + asciiDomain
	submitted := "JOSÉ@" + unicodeDomain

	dom := models.Domain{Name: asciiDomain, Active: true}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	sender := models.Mailbox{DomainID: dom.ID, LocalPart: "josé", Address: stored, Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&sender).Error; err != nil {
		t.Fatalf("create sender mailbox: %v", err)
	}
	other := models.Mailbox{DomainID: dom.ID, LocalPart: "ana", Address: "ana@" + asciiDomain, Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other mailbox: %v", err)
	}
	var owner models.Role
	if err := db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
		t.Fatalf("owner role: %v", err)
	}
	grant := models.Grant{HolderKind: models.GrantHolderAccount, HolderID: other.ID, RoleID: owner.ID,
		Scope: "mailbox:" + stored, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("create grant: %v", err)
	}
	t.Cleanup(func() {
		db.Where("id = ?", grant.ID).Delete(&models.Grant{})
		db.Where("mailbox_id = ?", sender.ID).Delete(&models.Message{})
		db.Where("id IN ?", []uint{sender.ID, other.ID}).Delete(&models.Mailbox{})
		db.Where("id = ?", dom.ID).Delete(&models.Domain{})
	})

	store := NewStore(db, "")

	t.Run("sender authorization uses the stored identity's grant", func(t *testing.T) {
		for _, from := range []string{stored, submitted, "josé@" + unicodeDomain} {
			ok, err := store.SenderAuthorized(other.ID, from)
			if err != nil || !ok {
				t.Errorf("SenderAuthorized(%q) = %v, %v; want true", from, ok, err)
			}
		}
		// The sender itself holds no grant on its own row here, and the grant
		// on josé@ does not extend to another account.
		if ok, err := store.SenderAuthorized(sender.ID, "ana@"+unicodeDomain); err != nil || ok {
			t.Errorf("SenderAuthorized(ungranted) = %v, %v; want false", ok, err)
		}
		if ok, err := store.SenderAuthorized(other.ID, "nobody@"+unicodeDomain); err != nil || ok {
			t.Errorf("SenderAuthorized(unknown) = %v, %v; want false", ok, err)
		}
	})

	t.Run("submitted message is attributed to the stored identity", func(t *testing.T) {
		ref, err := store.PersistSubmittedMessage(SubmittedMessage{
			Sender:     submitted,
			MessageID:  "<eai@" + asciiDomain + ">",
			Subject:    "Grüße",
			RawMessage: "Subject: Grüße\r\n\r\nbody",
		})
		if err != nil || ref == nil {
			t.Fatalf("PersistSubmittedMessage = %v, %v; want an id", ref, err)
		}
		var row models.Message
		if err := db.First(&row, *ref).Error; err != nil {
			t.Fatalf("load message: %v", err)
		}
		if row.MailboxID != sender.ID {
			t.Errorf("mailbox_id = %d, want %d", row.MailboxID, sender.ID)
		}
		if row.Sender != submitted || row.Subject != "Grüße" {
			t.Errorf("sender/subject = %q/%q, want the submitted values", row.Sender, row.Subject)
		}
	})

	t.Run("inactive identity is not attributed", func(t *testing.T) {
		if err := db.Model(&models.Mailbox{}).Where("id = ?", sender.ID).Update("active", false).Error; err != nil {
			t.Fatal(err)
		}
		defer db.Model(&models.Mailbox{}).Where("id = ?", sender.ID).Update("active", true)
		ref, err := store.PersistSubmittedMessage(SubmittedMessage{Sender: submitted, MessageID: "<off@" + asciiDomain + ">"})
		if err != nil || ref != nil {
			t.Errorf("PersistSubmittedMessage(inactive) = %v, %v; want nil, nil", ref, err)
		}
	})
}
