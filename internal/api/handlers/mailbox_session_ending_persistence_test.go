package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestMailboxSessionEndingPersistenceAndRollback(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}, &models.RefreshToken{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "session-ending.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	target := models.Mailbox{DomainID: domain.ID, LocalPart: "target", Address: "target@session-ending.test", Active: true}
	other := models.Mailbox{DomainID: domain.ID, LocalPart: "other", Address: "other@session-ending.test", Active: true}
	for _, row := range []*models.Mailbox{&target, &other} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, kind := range []string{models.CredentialKindPassword, models.CredentialKindTOTP, models.CredentialKindAppPassword, models.CredentialKindAPIKey} {
		if err := db.Create(&models.Credential{AccountID: target.ID, Kind: kind, Name: "Public test fixture", PublicID: fmt.Sprintf("session-ending-%d", i), Scope: models.CredentialScope{MailboxID: target.ID}}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed := func(jti string, id uint, status string) {
		t.Helper()
		if err := db.Create(&models.RefreshToken{Jti: jti, AccountID: id, SessionID: "public-fixture-" + jti, Status: status, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed("active-one", target.ID, models.RefreshTokenActive)
	seed("active-two", target.ID, models.RefreshTokenActive)
	seed("rotated", target.ID, models.RefreshTokenRotated)
	seed("other-active", other.ID, models.RefreshTokenActive)
	cache := NewSessionCache(db, time.Minute)
	h := NewMailboxHandler(db, nil, cache)
	path := fmt.Sprintf("/api/v1/admin/mailboxes/%d/sessions", target.ID)
	call := func(cancel bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, path, nil)
		if cancel {
			ctx, stop := context.WithCancel(r.Context())
			stop()
			r = r.WithContext(ctx)
		}
		rr := httptest.NewRecorder()
		mailboxRoutes(h).ServeHTTP(rr, r)
		return rr
	}
	if ok, err := cache.Current(target.ID, target.CredentialEpoch); err != nil || !ok {
		t.Fatalf("prime:%t %v", ok, err)
	}
	rr := call(false)
	if rr.Code != 204 || rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
		t.Fatalf("end sessions:%d %s", rr.Code, rr.Body)
	}
	var stored models.Mailbox
	if err := db.First(&stored, target.ID).Error; err != nil || stored.CredentialEpoch != target.CredentialEpoch+1 {
		t.Fatalf("epoch:%d %v", stored.CredentialEpoch, err)
	}
	if ok, err := cache.Current(target.ID, target.CredentialEpoch); err != nil || ok {
		t.Fatalf("cached old epoch accepted:%t %v", ok, err)
	}
	for jti, want := range map[string]string{"active-one": models.RefreshTokenRevoked, "active-two": models.RefreshTokenRevoked, "rotated": models.RefreshTokenRotated, "other-active": models.RefreshTokenActive} {
		var row models.RefreshToken
		if err := db.Where("jti = ?", jti).First(&row).Error; err != nil || row.Status != want {
			t.Fatalf("refresh state%s=%s want%s err%v", jti, row.Status, want, err)
		}
	}
	var activeCredentials int64
	if err := db.Model(&models.Credential{}).Where("account_id = ? AND revoked_at IS NULL", target.ID).Count(&activeCredentials).Error; err != nil || activeCredentials != 4 {
		t.Fatalf("session ending revoked credentials:%d %v", activeCredentials, err)
	}
	var otherStored models.Mailbox
	if err := db.First(&otherStored, other.ID).Error; err != nil || otherStored.CredentialEpoch != other.CredentialEpoch {
		t.Fatalf("other account changed:%d %v", otherStored.CredentialEpoch, err)
	}
	seed("active-after", target.ID, models.RefreshTokenActive)
	epoch := stored.CredentialEpoch
	if ok, err := cache.Current(target.ID, epoch); err != nil || !ok {
		t.Fatalf("prime new epoch:%t %v", ok, err)
	}
	generation := cache.gen
	const callback = "session_ending_refresh_failure"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "refresh_tokens" {
			_ = tx.AddError(errors.New("private refresh update detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	rr = call(false)
	if rr.Code != 500 {
		t.Fatalf("failed revocation:%d %s", rr.Code, rr.Body)
	}
	if err := db.Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if cache.gen != generation {
		t.Fatal("failed transaction invalidated session cache")
	}
	if err := db.First(&stored, target.ID).Error; err != nil || stored.CredentialEpoch != epoch {
		t.Fatalf("epoch update did not roll back:%d %v", stored.CredentialEpoch, err)
	}
	var remaining models.RefreshToken
	if err := db.Where("jti = ?", "active-after").First(&remaining).Error; err != nil || remaining.Status != models.RefreshTokenActive {
		t.Fatalf("refresh write did not roll back:%s %v", remaining.Status, err)
	}
	rr = call(true)
	if rr.Code != 500 {
		t.Fatalf("cancelled revocation:%d %s", rr.Code, rr.Body)
	}
	var errorBody map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &errorBody); err != nil {
		t.Fatal(err)
	}
	if body, ok := errorBody["error"].(map[string]interface{}); !ok || body["code"] != "internal_error" || body["message"] != "Internal server error" {
		t.Fatalf("cancelled lookup did not return a sanitized error:%s", rr.Body)
	}
	if err := db.First(&stored, target.ID).Error; err != nil || stored.CredentialEpoch != epoch {
		t.Fatal("cancelled request changed epoch")
	}
}
