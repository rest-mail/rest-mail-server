package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestContactMutationPersistence(t *testing.T) {
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
	routes := contactRoutes(NewContactHandler(tx))
	send := func(method, suffix, body string) *httptest.ResponseRecorder {
		if suffix != "" {
			suffix = "/" + suffix
		}
		rr := httptest.NewRecorder()
		r := authzRequest(method, contactsPathOf(fx.ownerMailbox.ID, suffix), fx.ownerMailbox.ID, nil, body)
		r.Header.Set("Content-Type", "application/json")
		routes.ServeHTTP(rr, r)
		return rr
	}
	rr := send(http.MethodPost, "", `{"email":"new@example.com","name":"Before"}`)
	if rr.Code != 201 {
		t.Fatalf("create:%d %s", rr.Code, rr.Body)
	}
	var contact models.Contact
	if err := tx.Where("mailbox_id = ? AND email = ?", fx.ownerMailbox.ID, "new@example.com").First(&contact).Error; err != nil {
		t.Fatal(err)
	}
	if contact.TrustLevel != "trusted" || contact.Source != "manual" {
		t.Fatalf("creation defaults:%+v", contact)
	}
	if err := tx.SavePoint("contact_duplicate_probe").Error; err != nil {
		t.Fatal(err)
	}
	rr = send(http.MethodPost, "", `{"email":"new@example.com"}`)
	if rr.Code != 409 {
		t.Fatalf("duplicate:%d %s", rr.Code, rr.Body)
	}
	if err := tx.RollbackTo("contact_duplicate_probe").Error; err != nil {
		t.Fatal(err)
	}
	rr = send(http.MethodPatch, uintToStr(contact.ID), `{"name":"","trust_level":"auto"}`)
	if rr.Code != 200 {
		t.Fatalf("patch:%d %s", rr.Code, rr.Body)
	}
	var changed models.Contact
	if err := tx.First(&changed, contact.ID).Error; err != nil {
		t.Fatal(err)
	}
	if changed.Name != "" || changed.Email != contact.Email || changed.MailboxID != fx.ownerMailbox.ID || changed.TrustLevel != "auto" {
		t.Fatalf("patch changed wrong fields:%+v", changed)
	}
	rr = send(http.MethodPost, "block", `{"email":"new@example.com"}`)
	if rr.Code != 200 {
		t.Fatalf("block existing:%d %s", rr.Code, rr.Body)
	}
	if err := tx.First(&changed, contact.ID).Error; err != nil {
		t.Fatal(err)
	}
	if changed.Name != "" || changed.TrustLevel != "blocked" || changed.Source != "manual" || changed.Email != contact.Email {
		t.Fatalf("block persistence:%+v", changed)
	}
	const callback = "contact_mutation_creation_failure"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "contacts" {
			_ = db.AddError(errors.New("private create dependency failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Callback().Create().Remove(callback) })
	rr = send(http.MethodPost, "", `{"email":"other@example.com"}`)
	if rr.Code != 500 {
		t.Fatalf("non-unique create failure:%d %s", rr.Code, rr.Body)
	}
	if err := tx.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := tx.Callback().Query().Before("gorm:query").Register("contact_mutation_lookup_failure", func(db *gorm.DB) {
		if db.Statement.Table == "contacts" {
			_ = db.AddError(errors.New("private lookup dependency failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Callback().Query().Remove("contact_mutation_lookup_failure") })
	for _, request := range []struct{ method, suffix, body string }{{http.MethodPatch, uintToStr(contact.ID), `{"name":"Changed"}`}, {http.MethodPost, "block", `{"email":"new@example.com"}`}} {
		rr = send(request.method, request.suffix, request.body)
		if rr.Code != 500 {
			t.Fatalf("lookup failure:%d %s", rr.Code, rr.Body)
		}
	}
}
