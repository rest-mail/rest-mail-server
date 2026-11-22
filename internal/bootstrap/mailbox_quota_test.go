package bootstrap

import (
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func TestBootstrapEveryMailboxHasQuotaUsage(t *testing.T) {
	for _, address := range []string{"admin@example.com", "postmaster@example.com"} {
		t.Run(address, func(t *testing.T) {
			database := openBootstrapTestDB(t)
			mustRBAC(t, database)
			if _, err := EnsureFirstAdmin(database, address, strongPassword, true, t0); err != nil {
				t.Fatal(err)
			}
			var mailboxes []models.Mailbox
			if err := database.Find(&mailboxes).Error; err != nil {
				t.Fatal(err)
			}
			for _, mailbox := range mailboxes {
				var usage models.QuotaUsage
				if err := database.Where("mailbox_id = ?", mailbox.ID).First(&usage).Error; err != nil {
					t.Errorf("bootstrap mailbox %s has no quota usage: %v", mailbox.Address, err)
				} else if usage.TotalBytes() != 0 || usage.MessageCount != 0 {
					t.Errorf("fresh mailbox quota is not empty: %+v", usage)
				}
			}
		})
	}
}

func TestBootstrapQuotaFailureRollsBackMailbox(t *testing.T) {
	database := openBootstrapTestDB(t)
	mustRBAC(t, database)
	const callback = "test:reject-bootstrap-quota"
	if err := database.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "quota_usage" {
			_ = tx.AddError(errors.New("quota storage unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Callback().Create().Remove(callback) })
	if _, err := EnsureFirstAdmin(database, "postmaster@example.com", strongPassword, true, t0); err == nil || !strings.Contains(err.Error(), "quota storage unavailable") {
		t.Fatalf("bootstrap must refuse a failed quota insert: %v", err)
	}
	for _, model := range []any{&models.Mailbox{}, &models.Domain{}, &models.QuotaUsage{}} {
		var count int64
		if err := database.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("bootstrap left %d rows of %T after quota failure", count, model)
		}
	}
}
