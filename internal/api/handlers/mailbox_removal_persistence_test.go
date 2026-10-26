package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

func TestMailboxRemovalPersistenceAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "delete_failure", "audit_failure", "cancelled", "last_superadmin", "without_two_factor"} {
		t.Run(scenario, func(t *testing.T) {
			db := openCustomFilterCreateScratchDB(t)
			if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}, &models.RefreshToken{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.AuditEntry{}); err != nil {
				t.Fatal(err)
			}
			create := func(v interface{}) {
				t.Helper()
				if err := db.Create(v).Error; err != nil {
					t.Fatal(err)
				}
			}
			domain := models.Domain{Name: "mailbox-removal.test"}
			create(&domain)
			target := models.Mailbox{DomainID: domain.ID, LocalPart: "target", Address: "target@" + domain.Name, Active: true}
			actor := models.Mailbox{DomainID: domain.ID, LocalPart: "actor", Address: "actor@" + domain.Name, Active: true}
			observer := models.Mailbox{DomainID: domain.ID, LocalPart: "observer", Address: "observer@" + domain.Name, Active: true}
			for _, row := range []*models.Mailbox{&target, &actor, &observer} {
				create(row)
			}
			rootRole := models.Role{Name: "root"}
			readRole := models.Role{Name: "reader"}
			create(&rootRole)
			create(&readRole)
			rootCap := models.Capability{Name: "*"}
			readCap := models.Capability{Name: "mail:read"}
			create(&rootCap)
			create(&readCap)
			create(&models.RoleCapability{RoleID: rootRole.ID, CapabilityID: rootCap.ID})
			create(&models.RoleCapability{RoleID: readRole.ID, CapabilityID: readCap.ID})
			actorGrant := models.Grant{HolderKind: "account", HolderID: actor.ID, RoleID: rootRole.ID, Scope: "*", Mode: "active", GrantedByKind: "system", GrantedAt: time.Now()}
			targetGrant := models.Grant{HolderKind: "account", HolderID: target.ID, RoleID: readRole.ID, Scope: grants.Mailbox(target.Address), Mode: "active", GrantedByKind: "system", GrantedAt: time.Now()}
			observerGrant := models.Grant{HolderKind: "account", HolderID: observer.ID, RoleID: readRole.ID, Scope: grants.Mailbox(target.Address), Mode: "active", GrantedByKind: "system", GrantedAt: time.Now()}
			if scenario == "last_superadmin" {
				targetGrant.RoleID = rootRole.ID
				targetGrant.Scope = "*"
			} else {
				create(&actorGrant)
			}
			create(&targetGrant)
			create(&observerGrant)
			kinds := []string{models.CredentialKindPassword, models.CredentialKindTOTP, models.CredentialKindRecoveryCode, models.CredentialKindAppPassword, models.CredentialKindAPIKey}
			for _, kind := range kinds {
				create(&models.Credential{AccountID: target.ID, Kind: kind, PublicID: "target-" + kind, Name: "Public fixture"})
			}
			otherCredential := models.Credential{AccountID: actor.ID, Kind: models.CredentialKindAPIKey, PublicID: "other-key", Name: "Other fixture"}
			create(&otherCredential)
			for _, row := range []models.RefreshToken{{Jti: "target-active", AccountID: target.ID, Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)}, {Jti: "target-rotated", AccountID: target.ID, Status: models.RefreshTokenRotated, ExpiresAt: time.Now().Add(time.Hour)}, {Jti: "other-active", AccountID: actor.ID, Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)}} {
				row := row
				create(&row)
			}
			permissionCache := grants.NewCache(db, time.Minute)
			sessionCache := NewSessionCache(db, time.Minute)
			h := NewMailboxHandler(db, permissionCache, sessionCache)
			held, err := permissionCache.Held(observer.ID)
			if err != nil || !held.Allows("mail:read", grants.Mailbox(target.Address)) {
				t.Fatalf("prime grants:%v", err)
			}
			current, err := sessionCache.Current(target.ID, 0)
			if err != nil || !current {
				t.Fatalf("prime session:%v", err)
			}
			r := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/admin/mailboxes/%d", target.ID), nil)
			claims := &auth.Claims{AccountID: actor.ID, Address: actor.Address, AMR: []string{auth.AMRPassword, auth.AMROTP}}
			if scenario == "last_superadmin" {
				claims.AccountID = target.ID
				claims.Address = target.Address
			}
			if scenario == "without_two_factor" {
				claims.AMR = []string{auth.AMRPassword}
			}
			r = withClaims(r, claims)
			if scenario == "cancelled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			if scenario == "delete_failure" {
				if err := db.Callback().Delete().Before("gorm:delete").Register("mailbox-removal:fault", func(tx *gorm.DB) {
					if tx.Statement.Table == "mailboxes" {
						_ = tx.AddError(errors.New("private deletion failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "audit_failure" {
				if err := db.Callback().Create().Before("gorm:create").Register("mailbox-removal:fault", func(tx *gorm.DB) {
					if tx.Statement.Table == "audit_log" {
						_ = tx.AddError(errors.New("private audit failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			rr := httptest.NewRecorder()
			mailboxRoutes(h).ServeHTTP(rr, r)
			if scenario == "delete_failure" {
				if err := db.Callback().Delete().Remove("mailbox-removal:fault"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "audit_failure" {
				if err := db.Callback().Create().Remove("mailbox-removal:fault"); err != nil {
					t.Fatal(err)
				}
			}
			want := 500
			switch scenario {
			case "success":
				want = 204
			case "last_superadmin":
				want = 409
			case "without_two_factor":
				want = 403
			}
			if rr.Code != want {
				t.Fatalf("status:%d body:%s want%d", rr.Code, rr.Body, want)
			}
			success := scenario == "success"
			var count int64
			if err := db.Model(&models.Mailbox{}).Where("id = ?", target.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if (count == 0) != success {
				t.Fatal("mailbox persistence/rollback")
			}
			var creds []models.Credential
			if err := db.Where("account_id = ?", target.ID).Find(&creds).Error; err != nil {
				t.Fatal(err)
			}
			if len(creds) != 5 {
				t.Fatal("credential audit rows removed")
			}
			for _, cred := range creds {
				if (cred.RevokedAt != nil) != success {
					t.Fatalf("credential %s revocation/rollback", cred.Kind)
				}
			}
			if err := db.First(&otherCredential, otherCredential.ID).Error; err != nil || otherCredential.RevokedAt != nil {
				t.Fatal("other account credential changed")
			}
			for jti, wantStatus := range map[string]string{"target-active": models.RefreshTokenActive, "target-rotated": models.RefreshTokenRotated, "other-active": models.RefreshTokenActive} {
				if success && jti == "target-active" {
					wantStatus = models.RefreshTokenRevoked
				}
				var row models.RefreshToken
				if err := db.Where("jti = ?", jti).First(&row).Error; err != nil || row.Status != wantStatus {
					t.Fatalf("refresh %s:%s want%s err%v", jti, row.Status, wantStatus, err)
				}
			}
			for _, g := range []models.Grant{targetGrant, observerGrant} {
				var n int64
				if err := db.Model(&models.Grant{}).Where("id = ?", g.ID).Count(&n).Error; err != nil {
					t.Fatal(err)
				}
				if (n == 0) != success {
					t.Fatal("grant persistence/rollback")
				}
			}
			var entries []models.AuditEntry
			if err := db.Order("seq").Find(&entries).Error; err != nil {
				t.Fatal(err)
			}
			wantEntries := 0
			if success {
				wantEntries = 2
			}
			if len(entries) != wantEntries {
				t.Fatalf("atomic grant audit:%d want%d", len(entries), wantEntries)
			}
			for _, entry := range entries {
				if entry.Action != grants.ActionDelete || entry.ActorID == nil || *entry.ActorID != claims.AccountID {
					t.Fatal("grant audit attribution")
				}
			}
			if success {
				result, err := audit.VerifyDB(db)
				if err != nil || !result.OK {
					t.Fatalf("grant audit integrity:%+v %v", result, err)
				}
			}
			held, err = permissionCache.Held(observer.ID)
			if err != nil || held.Allows("mail:read", grants.Mailbox(target.Address)) == success {
				t.Fatal("grant cache commit/rollback")
			}
			current, err = sessionCache.Current(target.ID, 0)
			if err != nil || current == success {
				t.Fatal("session cache commit/rollback")
			}
		})
	}
}
