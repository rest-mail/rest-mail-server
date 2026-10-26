package handlers

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestSnapshotRestorePersistence(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}, &models.QuotaUsage{}, &models.Role{}, &models.Grant{}, &models.Message{}, &models.OutboundQueue{}, &models.Pipeline{}, &models.Ban{}); err != nil {
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
	role := models.Role{Name: "retained administrator"}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	grant := models.Grant{HolderKind: models.GrantHolderAccount, HolderID: admin.ID, RoleID: role.ID, Scope: "*", Mode: models.GrantActive, GrantedByKind: "system", GrantedAt: time.Now()}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{AccountID: admin.ID, Kind: models.CredentialKindPassword, Name: "Preserved", PublicID: "restore-admin", SecretHash: "fixture-marker", Scope: models.CredentialScope{}}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(seedTestData); err != nil {
		t.Fatal(err)
	}
	type persisted struct {
		Domains     []models.Domain
		Mailboxes   []models.Mailbox
		Aliases     []models.Alias
		Messages    []models.Message
		Queue       []models.OutboundQueue
		Pipelines   []models.Pipeline
		Bans        []models.Ban
		Credentials []models.Credential
		Grants      []models.Grant
		Quotas      []models.QuotaUsage
	}
	read := func() persisted {
		t.Helper()
		var out persisted
		for _, dest := range []any{&out.Domains, &out.Mailboxes, &out.Aliases, &out.Messages, &out.Queue, &out.Pipelines, &out.Bans, &out.Credentials, &out.Grants, &out.Quotas} {
			if err := db.Order("1").Find(dest).Error; err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	initial := read()
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	messageID := uint(41)
	// The arrays are authoritative. Nested read associations must not create
	// extra rows or change an administrator that ON CONFLICT preserves.
	snap := testSnapshotModels{
		Domains:    []models.Domain{{ID: 11, Name: "snapshot.test", ServerType: "traditional", Active: false, DefaultQuotaBytes: 0, DMARCPolicy: "none", CreatedAt: stamp, UpdatedAt: stamp}, {ID: domain.ID, Name: "changed-admin.test", DMARCPolicy: "none"}},
		Mailboxes:  []models.Mailbox{{ID: 21, DomainID: 11, LocalPart: "owner", Address: "owner@snapshot.test", Active: false, QuotaBytes: 0, CreatedAt: stamp, UpdatedAt: stamp, Domain: models.Domain{ID: 999, Name: "read-view.test"}}, {ID: admin.ID, DomainID: domain.ID, Address: "changed@admin.test"}},
		Aliases:    []models.Alias{{ID: 31, DomainID: 11, SourceAddress: "alias@snapshot.test", DestinationAddress: "owner@snapshot.test", Active: false, CreatedAt: stamp}},
		Messages:   []models.Message{{ID: 41, MailboxID: 21, Folder: "INBOX", MsgID: "<restore@snapshot.test>", Sender: "sender@example.test", RecipientsTo: models.JSONB(`["owner@snapshot.test"]`), RecipientsCc: models.JSONB(`[]`), Headers: models.JSONB(`{"X-Fixture":"true"}`), BodyText: "Fixture body ✓", ReceivedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}},
		Queue:      []models.OutboundQueue{{ID: 51, MessageID: &messageID, Sender: "sender@example.test", Recipient: "owner@snapshot.test", Domain: "snapshot.test", Status: "pending", MaxRetries: 0, NextAttempt: stamp, CreatedAt: stamp, ExpiresAt: stamp.Add(time.Hour)}},
		Pipelines:  []models.Pipeline{{ID: 61, DomainID: 11, Direction: "inbound", Active: false, Filters: json.RawMessage(`[{"name":"header_validate","enabled":false,"config":{"nested":[1,false]}}]`), CreatedAt: stamp, UpdatedAt: stamp}},
		Bans:       []models.Ban{{ID: 71, IP: "192.0.2.17", Protocol: "smtp", CreatedAt: stamp}},
		SnapshotAt: stamp,
	}
	in, err := snapshotJSON[RestoreTestSnapshot](snap)
	if err != nil {
		t.Fatal(err)
	}
	h := NewTestHandler(db, &config.Config{Environment: "test"})
	r := httptest.NewRequest("POST", "/api/v1/admin/test/snapshot/restore", nil)
	const callback = "snapshot_restore_late_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "bans" {
			_ = tx.AddError(errors.New("private final restore failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if _, err := h.restoreSnapshot(r, in); err == nil {
		t.Fatal("late restore failure reported success")
	}
	if !reflect.DeepEqual(initial, read()) {
		t.Fatal("failed restore persisted partial rows or lost preexisting fixture data")
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if status, err := h.restoreSnapshot(r, in); err != nil || status.Status != "restore_complete" {
		t.Fatalf("restore status %q, error %v", status.Status, err)
	}
	got := read()
	if len(got.Domains) != 2 || len(got.Mailboxes) != 2 || len(got.Aliases) != 1 || len(got.Messages) != 1 || len(got.Queue) != 1 || len(got.Pipelines) != 1 || len(got.Bans) != 1 || len(got.Credentials) != 1 || len(got.Quotas) != 0 {
		t.Fatal("restore did not replace exactly the seven fixture tables")
	}
	if !reflect.DeepEqual(got.Domains[0], initial.Domains[0]) || !reflect.DeepEqual(got.Mailboxes[0], initial.Mailboxes[0]) || !reflect.DeepEqual(got.Credentials[0], initial.Credentials[0]) || !reflect.DeepEqual(got.Grants, initial.Grants) {
		t.Fatal("restore changed the retained administrator, credential, domain or grant")
	}
	if got.Domains[1].ID != 11 || got.Domains[1].DefaultQuotaBytes != 0 || got.Mailboxes[1].ID != 21 || got.Mailboxes[1].DomainID != 11 || got.Mailboxes[1].QuotaBytes != 0 || got.Mailboxes[1].Active || got.Aliases[0].Active || got.Queue[0].MaxRetries != 0 || got.Pipelines[0].Active {
		t.Fatal("restore changed explicit zero/false values or references")
	}
	if got.Messages[0].ID != 41 || got.Messages[0].MailboxID != 21 || got.Messages[0].BodyText != "Fixture body ✓" || !got.Messages[0].CreatedAt.Equal(stamp) || got.Queue[0].MessageID == nil || *got.Queue[0].MessageID != 41 {
		t.Fatal("restore changed message content, timestamp or queue reference")
	}
	for _, pair := range [][2][]byte{{got.Messages[0].Headers, snap.Messages[0].Headers}, {got.Pipelines[0].Filters, snap.Pipelines[0].Filters}} {
		var a, b any
		if json.Unmarshal(pair[0], &a) != nil || json.Unmarshal(pair[1], &b) != nil || !reflect.DeepEqual(a, b) {
			t.Fatal("restore changed raw JSON content")
		}
	}
}
