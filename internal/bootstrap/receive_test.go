package bootstrap

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

func TestReceiveDefaultGrantRollsBackWhenAuditFails(t *testing.T) {
	db := openBootstrapTestDB(t)
	mustRBAC(t, db)
	domain := models.Domain{Name: "receive-audit.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "new", Address: "new@" + domain.Name, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	const callback = "receive:audit-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			_ = tx.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if err := grants.EnsureReceive(db, &mailbox); err == nil {
		t.Fatal("unaudited receive grant succeeded")
	}
	var count int64
	if err := db.Model(&models.Grant{}).Where("holder_kind = ? AND holder_id = ?", models.GrantHolderAccount, mailbox.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("audit failure left %d receive grants", count)
	}
}

func TestReceiveMigrationPreservesExistingMailboxesAndRevocations(t *testing.T) {
	db := openBootstrapTestDB(t)
	d := models.Domain{Name: "receive-migration.test", Active: true}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: d.ID, LocalPart: "old", Address: "old@" + d.Name, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	// Simulate an installation with a server ceiling predating receive.
	if err := db.Create(&models.Capability{Name: models.CapMailSend}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureServerCeiling(db); err != nil {
		t.Fatal(err)
	}
	mustRBAC(t, db)
	allowed, err := grants.CanReceive(db, &mailbox)
	if err != nil || !allowed {
		t.Fatalf("existing mailbox: receive=%v err=%v", allowed, err)
	}
	var role models.Role
	if err := db.Where("name = ?", models.RoleReceiver).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	var grant models.Grant
	if err := db.Where("holder_kind = ? AND holder_id = ? AND role_id = ?", models.GrantHolderAccount, mailbox.ID, role.ID).First(&grant).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&models.AuditEntry{}).Where("action = ? AND target->>'grant_id' = ?", grants.ActionCreate, strconv.FormatUint(uint64(grant.ID), 10)).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration audit count=%d", count)
	}
	if err := grants.Remove(db, &grant, models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "test"}); err != nil {
		t.Fatal(err)
	}
	mustRBAC(t, db)
	held, err := grants.Load(db, mailbox.ID, time.Now())
	if err != nil || held.Allows(models.CapMailReceive, grants.Mailbox(mailbox.Address)) {
		t.Fatalf("restart restored receive: %v %v", held, err)
	}
	// The migration must not refill a revoked server capability either.
	server := serverGrant(t, db)
	if err := db.Where("role_id = ? AND capability_id = (SELECT id FROM capabilities WHERE name = ?)", server.RoleID, models.CapMailReceive).Delete(&models.RoleCapability{}).Error; err != nil {
		t.Fatal(err)
	}
	mustRBAC(t, db)
	if err := grants.EnsureReceive(db, &mailbox); err != nil {
		t.Fatal(err)
	}
	allowed, err = grants.CanReceive(db, &mailbox)
	if err != nil || allowed {
		t.Fatalf("restart restored server receive: %v %v", allowed, err)
	}
}
