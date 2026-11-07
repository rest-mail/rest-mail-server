package handlers

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// seedSenderWithOwnData is seedSendingMailbox for a send test that runs alone
// on a fresh database (#1385, #1605): it seeds every table, role and grant a
// send reads, gives the account the owner role on its mailbox, so it may send
// from it, and gives the mailbox the quota row every account has.
func seedSenderWithOwnData(t *testing.T, db *gorm.DB, domain, local string) models.Mailbox {
	t.Helper()
	if err := migrateModels(db, accountModels...); err != nil {
		t.Skipf("send test skipped: migrate failed (%v)", err)
	}
	if err := migrateAuditLog(db); err != nil {
		t.Skipf("send test skipped: audit log migrate failed (%v)", err)
	}
	if err := ensureRBAC(db); err != nil {
		t.Fatalf("seed the roles: %v", err)
	}
	mb := seedSendingMailbox(t, db, domain, local)
	grantOwnerRole(t, db, mb.ID, mb.Address)
	// A send's Sent copy is charged to the sender's quota row.
	if err := db.Create(&models.QuotaUsage{MailboxID: mb.ID}).Error; err != nil {
		t.Fatalf("seed the sender's quota usage: %v", err)
	}
	return mb
}
