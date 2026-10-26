package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// These bare-domain cases test refusal and cancellation. Removal also clears the
// domain's grants and owned configuration, so those tables exist here too; the
// cleanup itself is TestDomainCleanupPostgresLifecycleRetentionAndRollback's (#1141).
func TestDomainRemovalPersistenceAndCancellation(t *testing.T) {
	db := openDomainMutationScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Alias{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{},
		&models.Pipeline{}, &models.CustomFilter{}, &models.DomainSenderRule{}, &models.Certificate{}, &models.DKIMRotationKey{}, &models.MTASTSPolicy{}, &models.DomainRelay{}, &models.Suppression{}); err != nil {
		t.Fatal(err)
	}
	h := NewDomainHandler(db, nil)
	makeDomain := func(label string) models.Domain {
		t.Helper()
		d := models.Domain{Name: label + ".test", ServerType: "traditional"}
		if err := db.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
		return d
	}
	exists := func(d models.Domain) bool {
		t.Helper()
		var n int64
		if err := db.Model(&models.Domain{}).Where("id = ?", d.ID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	other := makeDomain("other")
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"empty", 204}, {"account", 409}, {"alias", 409}, {"lookup_cancel", 500}, {"after_guards_cancel", 500}, {"count_failure", 500}, {"delete_failure", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := makeDomain(tc.name)
			if tc.name == "account" {
				if err := db.Create(&models.Mailbox{DomainID: d.ID, Address: "owner@" + d.Name, LocalPart: "owner"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "alias" {
				if err := db.Create(&models.Alias{DomainID: d.ID, SourceAddress: "alias@" + d.Name, DestinationAddress: "owner@other.test"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/admin/domains/%d", d.ID), nil)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			queryCallback := "domain-removal-query"
			if err := db.Callback().Query().After("gorm:after_query").Register(queryCallback, func(tx *gorm.DB) {
				if tc.name == "lookup_cancel" && tx.Statement.Table == "domains" {
					cancel()
				}
				if tc.name == "after_guards_cancel" && tx.Statement.Table == "aliases" {
					cancel()
				}
				if tc.name == "count_failure" && tx.Statement.Table == "mailboxes" {
					_ = tx.AddError(errors.New("private count failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			deleteCallback := "domain-removal-delete"
			if err := db.Callback().Delete().Before("gorm:delete").Register(deleteCallback, func(tx *gorm.DB) {
				if tc.name == "delete_failure" && tx.Statement.Table == "domains" {
					_ = tx.AddError(errors.New("private delete failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			domainRoutes(h).ServeHTTP(rr, r)
			// Remove fault hooks before checking durable rows using fresh contexts.
			if err := db.Callback().Query().Remove(queryCallback); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Delete().Remove(deleteCallback); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tc.status {
				t.Fatalf("status:%d body:%s want%d", rr.Code, rr.Body, tc.status)
			}
			if strings.Contains(rr.Body.String(), "private") {
				t.Fatal("storage detail exposed")
			}
			if exists(d) != (tc.status != 204) || !exists(other) {
				t.Fatal("domain state or cross-domain isolation changed")
			}
			if tc.status == 409 && errorCodeOf(rr) != "domain_not_empty" {
				t.Fatalf("refusal code:%s", rr.Body)
			}
			if tc.status == 204 && rr.Body.Len() != 0 {
				t.Fatal("204 returned a body")
			}
		})
	}
}
