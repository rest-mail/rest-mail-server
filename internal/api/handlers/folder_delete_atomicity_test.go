package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// This regression runs against PostgreSQL in CI. The surrounding fixture has
// an outer transaction, so folder deletion also exercises GORM's savepoint path.
func TestDeleteFolder_QuotaFailureRollsBack(t *testing.T) {
	fx := newFolderRulesFixture(t)
	var before models.Mailbox
	if err := fx.db.First(&before, fx.mailbox).Error; err != nil {
		t.Fatal(err)
	}
	count := fx.count(t, "Work")
	if count != 2 {
		t.Fatalf("rollback fixture has %d messages, want 2", count)
	}
	const callback = "test:folder_delete_quota_failure"
	if err := fx.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "mailboxes" {
			_ = tx.AddError(errors.New("injected quota write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fx.db.Callback().Update().Remove(callback) })
	wantRefusal(t, fx.send(t, http.MethodDelete, "Work", ""), http.StatusInternalServerError, "Internal server error")
	if got := fx.count(t, "Work"); got != count {
		t.Fatalf("quota failure removed messages: got %d, want %d", got, count)
	}
	var after models.Mailbox
	if err := fx.db.First(&after, fx.mailbox).Error; err != nil {
		t.Fatal(err)
	}
	if after.QuotaUsedBytes != before.QuotaUsedBytes {
		t.Fatalf("quota failure changed usage: got %d, want %d", after.QuotaUsedBytes, before.QuotaUsedBytes)
	}
}
