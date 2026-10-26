package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestBanCreationPersistenceAndFailures(t *testing.T) {
	// Reuse the verified scratch allocator; all added ban rows/migrations stay
	// inside that fresh database, never the shared application database.
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Ban{}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db.NowFunc = func() time.Time { return stamp }
	h := NewBanHandler(db)
	hour := "1h"
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/bans", nil)
	in := createBanRequest{IP: "192.0.2.10", Reason: "Original", Protocol: "all", CreatedBy: "admin@restmail.test", Duration: &hour}
	answer, err := h.create(r, in)
	if err != nil || answer.Code != 201 {
		t.Fatalf("create:%+v %v", answer, err)
	}
	var before models.Ban
	if err := db.First(&before, answer.Value.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.ExpiresAt == nil || !before.ExpiresAt.Equal(stamp.Add(time.Hour)) || !before.CreatedAt.Equal(stamp) {
		t.Fatalf("creation clock/expiry:%+v", before)
	}
	in.Reason = "Permanent override"
	in.Protocol = "imap"
	in.CreatedBy = "operator@restmail.test"
	in.Duration = nil
	answer, err = h.create(r, in)
	if err != nil || answer.Code != 200 {
		t.Fatalf("upsert:%+v %v", answer, err)
	}
	var after models.Ban
	if err := db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.IP != before.IP || !after.CreatedAt.Equal(before.CreatedAt) || after.ExpiresAt != nil || after.Reason != in.Reason || after.Protocol != in.Protocol || after.CreatedBy != in.CreatedBy {
		t.Fatalf("replacement lost identity/fields:%+v", after)
	}
	const callback = "ban_creation_update_failure"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "bans" {
			_ = tx.AddError(errors.New("private ban update detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	in.Reason = "Must not persist"
	if _, err = h.create(r, in); err == nil {
		t.Fatal("failed update returned success")
	}
	if err := db.Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&after, before.ID).Error; err != nil || after.Reason != "Permanent override" {
		t.Fatalf("failed update changed stored row:%+v %v", after, err)
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	in.IP = "192.0.2.11"
	if _, err = h.create(r.WithContext(ctx), in); err == nil {
		t.Fatal("cancelled lookup/create returned success")
	}
	var count int64
	if err := db.Model(&models.Ban{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed/cancelled upsert changed row count:%d %v", count, err)
	}
}
