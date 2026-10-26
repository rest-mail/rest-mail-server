package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestContactImportRollbackAndDuplicatePersistence(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.Contact{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_mailbox_email ON contacts(mailbox_id, email)`).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	existing := models.Contact{MailboxID: fx.ownerMailbox.ID, Email: "duplicate@example.com", Name: "Keep", TrustLevel: "trusted", Source: "manual"}
	if err := tx.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	routes := contactRoutes(NewContactHandler(tx))
	send := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := authzRequest(http.MethodPost, contactsPathOf(fx.ownerMailbox.ID, "/import"), fx.ownerMailbox.ID, nil, body)
		r.Header.Set("Content-Type", "application/json")
		routes.ServeHTTP(rr, r)
		return rr
	}
	rr := send(`{"contacts":[{"email":"new@example.com"},{"email":"duplicate@example.com","name":"Must not overwrite"},{"email":"repeat@example.com"},{"email":"repeat@example.com"}]}`)
	if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `{"data":{"imported":2}}` {
		t.Fatalf("duplicate import:%d %s", rr.Code, rr.Body)
	}
	var kept models.Contact
	if err := tx.First(&kept, existing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if kept.Name != existing.Name || kept.TrustLevel != existing.TrustLevel || kept.Source != existing.Source {
		t.Fatalf("duplicate overwritten:%+v", kept)
	}
	const callback = "contact_import_batch_failure"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if c, ok := db.Statement.Dest.(*models.Contact); ok && c.Email == "fail@example.com" {
			_ = db.AddError(errors.New("private batch persistence failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Callback().Create().Remove(callback) })
	rr = send(`{"contacts":[{"email":"atomic-first@example.com"},{"email":"fail@example.com"},{"email":"atomic-last@example.com"}]}`)
	if rr.Code != 500 {
		t.Fatalf("batch failure:%d %s", rr.Code, rr.Body)
	}
	if err := tx.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Model(&models.Contact{}).Where("mailbox_id = ? AND email IN ?", fx.ownerMailbox.ID, []string{"atomic-first@example.com", "fail@example.com", "atomic-last@example.com"}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed batch left %d contacts", count)
	}
}

func TestContactSuggestionLiteralPersistence(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.Contact{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	for _, c := range []models.Contact{{MailboxID: fx.ownerMailbox.ID, Email: "percent@example.com", Name: "50% offer", TrustLevel: "auto", Source: "sent"}, {MailboxID: fx.ownerMailbox.ID, Email: "underscore@example.com", Name: "part_one", TrustLevel: "trusted", Source: "manual"}, {MailboxID: fx.ownerMailbox.ID, Email: "backslash@example.com", Name: `path\name`, TrustLevel: "auto", Source: "sent"}, {MailboxID: fx.ownerMailbox.ID, Email: "blocked@example.com", Name: "50% offer", TrustLevel: "blocked", Source: "manual"}} {
		if err := tx.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	routes := contactRoutes(NewContactHandler(tx))
	for query, email := range map[string]string{"%": "percent@example.com", "_": "underscore@example.com", `\`: "backslash@example.com"} {
		t.Run(query, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := authzRequest(http.MethodGet, contactsPathOf(fx.ownerMailbox.ID, "/suggest")+"?q="+url.QueryEscape(query), fx.ownerMailbox.ID, nil, "")
			routes.ServeHTTP(rr, r)
			var got struct{ Data []Contact }
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if rr.Code != 200 || len(got.Data) != 1 || got.Data[0].Email != email {
				t.Fatalf("literal query %q:%d %s", query, rr.Code, rr.Body)
			}
		})
	}
}
