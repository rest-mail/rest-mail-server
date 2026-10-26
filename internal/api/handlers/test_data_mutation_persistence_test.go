package handlers

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestTestDataMutationPersistence(t *testing.T) {
	// This helper verifies a freshly created scratch database before migrations
	// or writes. Missing optional tables intentionally exercise PostgreSQL's
	// aborted-transaction recovery through the cleanup savepoints.
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}, &models.QuotaUsage{}, &models.Role{}, &models.Grant{}, &models.Pipeline{}, &models.Quarantine{}, &models.DomainSenderRule{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "admin.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	admin := models.Mailbox{DomainID: domain.ID, LocalPart: "admin", Address: "admin@admin.test", Active: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	role := models.Role{Name: "fixture administrator"}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	grant := models.Grant{HolderKind: models.GrantHolderAccount, HolderID: admin.ID, RoleID: role.ID, Scope: "*", Mode: models.GrantActive, GrantedByKind: "system", GrantedAt: time.Now()}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{AccountID: admin.ID, Kind: models.CredentialKindPassword, Name: "Preserved", PublicID: "fixture-admin", SecretHash: "fixture-marker", Scope: models.CredentialScope{}}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	type persisted struct {
		Domains     []models.Domain
		Mailboxes   []models.Mailbox
		Quotas      []models.QuotaUsage
		Aliases     []models.Alias
		Credentials []models.Credential
		Grants      []models.Grant
		Quarantines []models.Quarantine
		SenderRules []models.DomainSenderRule
	}
	read := func() persisted {
		t.Helper()
		var out persisted
		for _, dest := range []any{&out.Domains, &out.Mailboxes, &out.Quotas, &out.Aliases, &out.Credentials, &out.Grants, &out.Quarantines, &out.SenderRules} {
			if err := db.Order("1").Find(dest).Error; err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	initial := read()
	h := NewTestHandler(db, &config.Config{Environment: "test"})
	r := httptest.NewRequest("POST", "/api/v1/admin/test/seed", nil)
	const callback = "test_data_late_alias_failure"
	setFailure := func() {
		t.Helper()
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "aliases" {
				_ = tx.AddError(errors.New("private late fixture failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	clearFailure := func() {
		t.Helper()
		if err := db.Callback().Create().Remove(callback); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	setFailure()
	if _, err := h.seedTestData(r, endpoint.Empty{}); err == nil {
		t.Fatal("late seed failure reported success")
	}
	if !reflect.DeepEqual(initial, read()) {
		t.Fatal("failed seed persisted partial fixture rows or changed the administrator")
	}
	clearFailure()
	if status, err := h.seedTestData(r, endpoint.Empty{}); err != nil || status.Status != "seed_complete" {
		t.Fatalf("seed status %q, error %v", status.Status, err)
	}
	seeded := read()
	if len(seeded.Domains) != 4 || len(seeded.Mailboxes) != 10 || len(seeded.Quotas) != 9 || len(seeded.Aliases) != 4 || len(seeded.Credentials) != 10 {
		t.Fatal("seeded fixture counts differ")
	}
	for _, row := range []any{
		&models.Quarantine{MailboxID: seeded.Mailboxes[1].ID, Sender: "fixture@example.test", RawMessage: "fixture", QuarantineReason: "test"},
		&models.DomainSenderRule{DomainID: seeded.Domains[1].ID, Pattern: "@example.test", ListType: "block"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	seeded = read()
	if _, err := h.seedTestData(r, endpoint.Empty{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seeded, read()) {
		t.Fatal("repeated seed changed existing fixtures or credentials")
	}
	setFailure()
	if _, err := h.resetTestData(r, endpoint.Empty{}); err == nil {
		t.Fatal("late reset failure reported success")
	}
	if !reflect.DeepEqual(seeded, read()) {
		t.Fatal("failed reset did not restore the exact preexisting rows and credentials")
	}
	clearFailure()
	if status, err := h.resetTestData(r, endpoint.Empty{}); err != nil || status.Status != "reset_complete" {
		t.Fatalf("reset status %q, error %v", status.Status, err)
	}
	reset := read()
	if len(reset.Domains) != 4 || len(reset.Mailboxes) != 10 || len(reset.Quotas) != 9 || len(reset.Aliases) != 4 || len(reset.Credentials) != 10 {
		t.Fatal("reset did not repopulate the complete fixture")
	}
	if len(reset.Quarantines) != 0 || len(reset.SenderRules) != 0 {
		t.Fatal("reset missed the actual quarantine or domain_sender_rules table")
	}
	if reset.Mailboxes[0].ID != admin.ID || !reflect.DeepEqual(reset.Credentials[0], initial.Credentials[0]) || !reflect.DeepEqual(reset.Grants, initial.Grants) || !reflect.DeepEqual(reset.Domains[0], initial.Domains[0]) {
		t.Fatal("reset changed the retained administrator, credential, domain or grant")
	}
	if reset.Mailboxes[1].ID == seeded.Mailboxes[1].ID {
		t.Fatal("reset did not replace non-admin fixtures")
	}
}
