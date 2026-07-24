package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
)

// TestMailboxUpdate_IgnoresNonAllowlistedColumns proves the mailbox update
// handler writes only vetted columns. Ownership/identity columns supplied in the
// request body (domain_id, address, local_part) and the server-owned
// quota_used_bytes must be ignored, while an allowlisted field (display_name) is
// applied. This locks in the mass-assignment (CWE-915) protection: a future
// change that threaded one of these into the update path would fail here.
func TestMailboxUpdate_IgnoresNonAllowlistedColumns(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}); err != nil {
		t.Skipf("mass-assignment test skipped: migrate QuotaUsage failed (%v)", err)
	}

	owner := models.Domain{Name: "massassign.test"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner domain: %v", err)
	}
	other := models.Domain{Name: "other-massassign.test"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other domain: %v", err)
	}
	mb := models.Mailbox{
		DomainID:    owner.ID,
		LocalPart:   "victim",
		Address:     "victim@massassign.test",
		Password:    "hash",
		DisplayName: "Original",
		QuotaBytes:  1000,
		Active:      true,
	}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}

	h := NewMailboxHandler(db)

	// One allowlisted field (display_name) mixed with columns the caller must
	// never control. domain_id/address/local_part/quota_used_bytes are not part
	// of updateMailboxRequest; sending them raw simulates a mass-assignment.
	body := `{"display_name":"Updated",` +
		`"domain_id":` + strconv.FormatUint(uint64(other.ID), 10) + `,` +
		`"address":"attacker@evil.test",` +
		`"local_part":"attacker",` +
		`"quota_used_bytes":999999}`

	idStr := strconv.FormatUint(uint64(mb.ID), 10)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/mailboxes/"+idStr, bytes.NewBufferString(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	h.Update(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got models.Mailbox
	if err := db.First(&got, mb.ID).Error; err != nil {
		t.Fatalf("reload mailbox: %v", err)
	}
	if got.DisplayName != "Updated" {
		t.Errorf("display_name = %q, want the allowlisted update %q", got.DisplayName, "Updated")
	}
	if got.DomainID != owner.ID {
		t.Errorf("domain_id = %d, want unchanged %d (ownership must not be mass-assignable)", got.DomainID, owner.ID)
	}
	if got.Address != "victim@massassign.test" {
		t.Errorf("address = %q, want unchanged (identity must not be mass-assignable)", got.Address)
	}
	if got.LocalPart != "victim" {
		t.Errorf("local_part = %q, want unchanged", got.LocalPart)
	}
	if got.QuotaUsedBytes != 0 {
		t.Errorf("quota_used_bytes = %d, want unchanged 0 (usage is server-owned)", got.QuotaUsedBytes)
	}
}
