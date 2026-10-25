package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

func TestSieveInstallationPersistsExplicitInactiveCreation(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	rr := putSieve(t, NewSieveHandler(tx), fx.ownerMailbox.ID, `{"script":"keep;","active":false}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var stored models.SieveScript
	if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Active || stored.Script != "keep;" || stored.ScriptHash != filters.HashSieveScript("keep;") || stored.ValidatedAt == nil {
		t.Fatalf("incorrect installation: %+v", stored)
	}
	var response struct{ Data SieveScript }
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Active || response.Data.ID != stored.ID || response.Data.MailboxID != stored.MailboxID {
		t.Fatalf("response differs from stored row: %s", rr.Body)
	}
}

func TestSieveInstallationLookupFailureDoesNotWrite(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	const callback = "sieve_installation_lookup_failure"
	if err := tx.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
		if query.Statement.Table == "sieve_scripts" {
			query.AddError(errors.New("private Sieve lookup failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Callback().Query().Remove(callback) })
	rr := putSieve(t, NewSieveHandler(tx), fx.ownerMailbox.ID, `{"script":"keep;"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("lookup failure: %d %s", rr.Code, rr.Body)
	}
	if err := tx.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Model(&models.SieveScript{}).Where("mailbox_id = ?", fx.ownerMailbox.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("lookup failure created %d scripts", count)
	}
}
