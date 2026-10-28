package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// Exercise the API transaction against a verified fresh database, including
// failure after the activation audit has already been appended.
func TestGrantRemovalPersistenceAndAtomicAudit(t *testing.T) {
	for _, scenario := range []string{"active", "live", "final_audit_failure", "grant_delete_failure", "activation_delete_failure", "cancelled", "cancelled_after_load", "last_superadmin"} {
		t.Run(scenario, func(t *testing.T) {
			db := openCustomFilterCreateScratchDB(t)
			if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.AuditEntry{}); err != nil {
				t.Fatal(err)
			}
			create := func(v interface{}) {
				t.Helper()
				if err := db.Create(v).Error; err != nil {
					t.Fatal(err)
				}
			}
			d := models.Domain{Name: "example.org"}
			create(&d)
			actor := models.Mailbox{DomainID: d.ID, LocalPart: "actor", Address: "actor@example.org", Active: true}
			holder := models.Mailbox{DomainID: d.ID, LocalPart: "holder", Address: "holder@example.org", Active: true}
			create(&actor)
			create(&holder)
			credentials := []models.Credential{{AccountID: actor.ID, Kind: models.CredentialKindAppPassword, PublicID: "actor-public-fixture", Name: "Actor fixture"}, {AccountID: holder.ID, Kind: models.CredentialKindAPIKey, PublicID: "holder-public-fixture", Name: "Holder fixture"}}
			for i := range credentials {
				create(&credentials[i])
			}
			root := models.Role{Name: "root"}
			read := models.Role{Name: "reader"}
			create(&root)
			create(&read)
			rootCap := models.Capability{Name: "*"}
			readCap := models.Capability{Name: "mail:read"}
			create(&rootCap)
			create(&readCap)
			create(&models.RoleCapability{RoleID: root.ID, CapabilityID: rootCap.ID})
			create(&models.RoleCapability{RoleID: read.ID, CapabilityID: readCap.ID})
			actorGrant := models.Grant{HolderKind: "account", HolderID: actor.ID, RoleID: root.ID, Scope: "*", Mode: "active", GrantedByKind: "system", GrantedAt: time.Now()}
			target := models.Grant{HolderKind: "account", HolderID: holder.ID, RoleID: read.ID, Scope: grants.Domain(d.Name), Mode: "eligible", GrantedByKind: "system", GrantedAt: time.Now()}
			if scenario == "active" {
				target.Mode = "active"
			}
			if scenario == "last_superadmin" {
				target.RoleID = root.ID
				target.Scope = "*"
				target.Mode = "active"
			} else {
				create(&actorGrant)
			}
			create(&target)
			activation := models.GrantActivation{GrantID: target.ID, ActivatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), Reason: "Public test activation"}
			if scenario == "active" || scenario == "last_superadmin" {
				activation.ExpiresAt = time.Now().Add(-time.Second)
			}
			create(&activation)
			// A second grant and its activation must survive every target operation.
			untouched := models.Grant{HolderKind: "account", HolderID: holder.ID, RoleID: read.ID, Scope: grants.Mailbox("other@example.org"), Mode: "eligible", GrantedByKind: "system", GrantedAt: time.Now()}
			create(&untouched)
			untouchedActivation := models.GrantActivation{GrantID: untouched.ID, ActivatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), Reason: "Other public activation"}
			create(&untouchedActivation)
			ensureDefaultServerCeiling(t, db)
			cache := grants.NewCache(db, time.Minute)
			held, err := cache.Held(holder.ID)
			if err != nil || !held.Allows("mail:read", grants.Domain(d.Name)) {
				t.Fatalf("prime permission cache:%v", err)
			}
			h := NewGrantHandler(db, cache, "")
			claims := &auth.Claims{AccountID: actor.ID, Address: actor.Address, AMR: []string{auth.AMRPassword, auth.AMROTP}}
			if scenario == "last_superadmin" {
				claims.AccountID = holder.ID
				claims.Address = holder.Address
			}
			r := withClaims(httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/admin/grants/%d", target.ID), nil), claims)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			if scenario == "cancelled" {
				cancel()
			}
			if scenario == "cancelled_after_load" {
				if err := db.Callback().Query().After("gorm:after_query").Register("grant-removal:cancel", func(tx *gorm.DB) {
					if tx.Statement.Table == "grants" {
						cancel()
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "grant_delete_failure" || scenario == "activation_delete_failure" {
				if err := db.Callback().Delete().Before("gorm:delete").Register("grant-removal:delete-fault", func(tx *gorm.DB) {
					if scenario == "grant_delete_failure" && tx.Statement.Table == "grants" || scenario == "activation_delete_failure" && tx.Statement.Table == "grant_activations" {
						_ = tx.AddError(errors.New("private grant deletion failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "final_audit_failure" {
				if err := db.Callback().Create().Before("gorm:create").Register("grant-removal:audit-fault", func(tx *gorm.DB) {
					entry, ok := tx.Statement.Dest.(*models.AuditEntry)
					if ok && entry.Action == grants.ActionDelete {
						_ = tx.AddError(errors.New("private final audit failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			rr := httptest.NewRecorder()
			grantRoutes(h).ServeHTTP(rr, r)
			if scenario == "cancelled_after_load" {
				if err := db.Callback().Query().Remove("grant-removal:cancel"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "grant_delete_failure" || scenario == "activation_delete_failure" {
				if err := db.Callback().Delete().Remove("grant-removal:delete-fault"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "final_audit_failure" {
				if err := db.Callback().Create().Remove("grant-removal:audit-fault"); err != nil {
					t.Fatal(err)
				}
			}
			success := scenario == "active" || scenario == "live"
			want := 500
			if success {
				want = 204
			}
			if scenario == "last_superadmin" {
				want = 409
			}
			if rr.Code != want || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("response:%d %s want%d", rr.Code, rr.Body, want)
			}
			count := func(model interface{}, id uint) int64 {
				t.Helper()
				var n int64
				if err := db.Model(model).Where("id = ?", id).Count(&n).Error; err != nil {
					t.Fatal(err)
				}
				return n
			}
			if (count(&models.Grant{}, target.ID) == 0) != success || (count(&models.GrantActivation{}, activation.ID) == 0) != success {
				t.Fatal("grant/activation persistence or rollback")
			}
			if count(&models.Grant{}, untouched.ID) != 1 || count(&models.GrantActivation{}, untouchedActivation.ID) != 1 {
				t.Fatal("another grant or activation changed")
			}
			var entries []models.AuditEntry
			if err := db.Order("seq").Find(&entries).Error; err != nil {
				t.Fatal(err)
			}
			wantEntries := 0
			if scenario == "active" {
				wantEntries = 1
			}
			if scenario == "live" {
				wantEntries = 2
			}
			if len(entries) != wantEntries {
				t.Fatalf("atomic audit entries:%d want%d", len(entries), wantEntries)
			}
			for i, entry := range entries {
				wantAction := grants.ActionDelete
				if scenario == "live" && i == 0 {
					wantAction = grants.ActionDeactivate
				}
				if entry.Action != wantAction || entry.Target["grant_id"] != strconv.FormatUint(uint64(target.ID), 10) || entry.ActorID == nil || *entry.ActorID != claims.AccountID {
					t.Fatalf("grant audit target/action/actor:%s", entry.Action)
				}
				if entry.Action == grants.ActionDeactivate && (entry.Target["activation_id"] != strconv.FormatUint(uint64(activation.ID), 10) || entry.Target["ended_at"] == "") {
					t.Fatal("live activation end not audited")
				}
			}
			if success {
				result, err := audit.VerifyDB(db)
				if err != nil || !result.OK {
					t.Fatalf("audit integrity:%+v %v", result, err)
				}
			}
			held, err = cache.Held(holder.ID)
			if err != nil || held.Allows("mail:read", grants.Domain(d.Name)) == success {
				t.Fatal("permission cache changed before commit or retained deleted permission")
			}
			// Removal is not an account deletion: both accounts and credentials survive.
			if count(&models.Mailbox{}, actor.ID) != 1 || count(&models.Mailbox{}, holder.ID) != 1 {
				t.Fatal("grant removal deleted an account")
			}
			for _, cred := range credentials {
				var stored models.Credential
				if err := db.First(&stored, cred.ID).Error; err != nil || stored.RevokedAt != nil || stored.AccountID != cred.AccountID {
					t.Fatal("grant removal changed a credential")
				}
			}
		})
	}
}
