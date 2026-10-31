package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

func TestQuotaRaiseRequiresTwoFactorAtTheWrite(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	for _, tc := range []struct {
		name, table, column, path, body string
		id                              uint
	}{
		{name: "mailbox", table: "mailboxes", column: "quota_bytes", path: fmt.Sprintf("/api/v1/admin/mailboxes/%d", fx.admin.ID), body: `{"quota_bytes":80}`, id: fx.admin.ID},
		{name: "domain", table: "domains", column: "default_quota_bytes", path: fmt.Sprintf("/api/v1/admin/domains/%d", fx.domain.ID), body: `{"default_quota_bytes":80}`, id: fx.domain.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Table(tc.table).Where("id = ?", tc.id).Update(tc.column, 100).Error; err != nil {
				t.Fatal(err)
			}
			callback := "fresh_two_factor:concurrent_quota_" + tc.name
			changed := false
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if changed || tx.Statement.Table != tc.table {
					return
				}
				if _, locking := tx.Statement.Clauses["FOR"]; locking {
					return
				}
				changed = true
				// Lower the cap after the initial read, before quota admission
				// takes its row lock. Injecting at UPDATE would wait on that lock.
				if err := db.Exec("UPDATE "+tc.table+" SET "+tc.column+" = 60 WHERE id = ?", tc.id).Error; err != nil {
					_ = tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Callback().Query().Remove(callback); err != nil {
					t.Errorf("remove quota callback: %v", err)
				}
			}()
			r := withClaims(httptest.NewRequest("PATCH", tc.path, strings.NewReader(tc.body)), &auth.Claims{AccountID: fx.admin.ID, AMR: []string{auth.AMRPassword}})
			rr := httptest.NewRecorder()
			if tc.name == "mailbox" {
				mailboxRoutes(NewMailboxHandler(db, grants.NewCache(db, grants.CacheTTL), nil)).ServeHTTP(rr, r)
			} else {
				domainRoutes(NewDomainHandler(db, nil)).ServeHTTP(rr, r)
			}
			if !changed {
				t.Fatal("concurrent quota change was not injected")
			}
			if rr.Code != 403 || errorCode(rr) != "two_factor_required" {
				t.Fatalf("concurrent raise: %d %s", rr.Code, rr.Body)
			}
			var stored int64
			if err := db.WithContext(context.Background()).Table(tc.table).Where("id = ?", tc.id).Pluck(tc.column, &stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored != 60 {
				t.Fatalf("quota was raised without 2FA: %d", stored)
			}
		})
	}
}
