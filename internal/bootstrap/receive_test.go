package bootstrap

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
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

// The receiver role comes from the first start, holding only mail:receive, and
// a start gives an account nothing: a mailbox holds it from being created.
func TestReceiveRoleAndStartGrantNothing(t *testing.T) {
	db := openBootstrapTestDB(t)
	d := models.Domain{Name: "receive-start.test", Active: true}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: d.ID, LocalPart: "row", Address: "row@" + d.Name, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	mustRBAC(t, db)
	var role models.Role
	if err := db.Where("name = ? AND system_role", models.RoleReceiver).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := grants.RoleCapabilities(db, role.ID); err != nil || !slices.Equal(got, []string{models.CapMailReceive}) {
		t.Fatalf("receiver role holds %v, %v", got, err)
	}
	if got, err := grants.RoleCapabilities(db, serverGrant(t, db).RoleID); err != nil || !slices.Contains(got, models.CapMailReceive) {
		t.Fatalf("server ceiling without mail:receive: %v, %v", got, err)
	}
	if allowed, err := grants.CanReceive(db, &mailbox); err != nil || allowed {
		t.Fatalf("a start gave a mailbox receive: %v %v", allowed, err)
	}
}

// What someone removed stays removed across restarts: a mailbox's receive
// grant, the capability on the receiver role, and the capability on the server.
func TestReceiveRestartKeepsRemovals(t *testing.T) {
	db := openBootstrapTestDB(t)
	mustRBAC(t, db)
	d := models.Domain{Name: "receive-restart.test", Active: true}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: d.ID, LocalPart: "new", Address: "new@" + d.Name, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureReceive(db, &mailbox); err != nil {
		t.Fatal(err)
	}
	allowed, err := grants.CanReceive(db, &mailbox)
	if err != nil || !allowed {
		t.Fatalf("new mailbox: receive=%v err=%v", allowed, err)
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
		t.Fatalf("grant audit count=%d", count)
	}
	if err := grants.Remove(db, &grant, models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "test"}); err != nil {
		t.Fatal(err)
	}
	mustRBAC(t, db)
	held, err := grants.Load(db, mailbox.ID, time.Now())
	if err != nil || held.Allows(models.CapMailReceive, grants.Mailbox(mailbox.Address)) {
		t.Fatalf("restart restored receive: %v %v", held, err)
	}
	if err := grants.EnsureReceive(db, &mailbox); err != nil {
		t.Fatal(err)
	}
	removeCapability := func(roleID uint) {
		t.Helper()
		if err := db.Where("role_id = ? AND capability_id = (SELECT id FROM capabilities WHERE name = ?)", roleID, models.CapMailReceive).Delete(&models.RoleCapability{}).Error; err != nil {
			t.Fatal(err)
		}
		mustRBAC(t, db)
		allowed, err := grants.CanReceive(db, &mailbox)
		if err != nil || allowed {
			t.Fatalf("restart restored a removed capability: %v %v", allowed, err)
		}
	}
	removeCapability(serverGrant(t, db).RoleID)
	removeCapability(role.ID)
	if got, err := grants.RoleCapabilities(db, role.ID); err != nil || len(got) != 0 {
		t.Fatalf("restart refilled the receiver role: %v, %v", got, err)
	}
}

// An installation from before the rename keeps its role assignments and server
// ceiling, and its API keys keep working, without anyone touching them.
func TestRenamedCapabilitiesMigrateOnce(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	if err := EnsureRBAC(gdb); err != nil {
		t.Fatal(err)
	}
	var capability models.Capability
	if err := gdb.Where("name = ?", "accounts:write").First(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Model(&capability).Updates(map[string]any{"name": "mailboxes:write", "resource": "mailboxes"}).Error; err != nil {
		t.Fatal(err)
	}
	var assigned int64
	if err := gdb.Model(&models.RoleCapability{}).Where("capability_id = ?", capability.ID).Count(&assigned).Error; err != nil || assigned == 0 {
		t.Fatalf("no role holds it: %d %v", assigned, err)
	}
	domain := models.Domain{Name: "rename.test", Active: true}
	if err := gdb.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	account := models.Mailbox{DomainID: domain.ID, LocalPart: "rename", Address: "rename@rename.test", Active: true}
	if err := gdb.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	key := models.Credential{AccountID: account.ID, Kind: models.CredentialKindAPIKey, PublicID: "rename-key", SecretHash: "x",
		Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: "mailboxes:write", Scope: "*"}}}}
	if err := gdb.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := EnsureRBAC(gdb); err != nil {
			t.Fatal(err)
		}
		var renamed models.Capability
		if err := gdb.Where("name = ?", "accounts:write").First(&renamed).Error; err != nil || renamed.ID != capability.ID || renamed.Resource != "accounts" {
			t.Fatalf("capability row: %+v %v, want id %d", renamed, err, capability.ID)
		}
		var old, held int64
		gdb.Model(&models.Capability{}).Where("name LIKE 'mailboxes:%'").Count(&old)
		gdb.Model(&models.RoleCapability{}).Where("capability_id = ?", capability.ID).Count(&held)
		if old != 0 || held != assigned {
			t.Fatalf("old names %d, roles holding it %d, want 0 and %d", old, held, assigned)
		}
		var stored models.Credential
		if err := gdb.First(&stored, key.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got := stored.Scope.Permissions; len(got) != 1 || got[0].Capability != "accounts:write" || got[0].Scope != "*" {
			t.Fatalf("stored permissions: %+v", got)
		}
	}
}
