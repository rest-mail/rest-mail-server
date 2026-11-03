package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// A per-domain list read by domain_id alone answered 200 with an empty list for a
// deleted domain. Each now looks the domain up and answers the 404 the other
// per-domain routes answer (#1746).
func TestDomainPolicyListsRefuseDeletedDomain(t *testing.T) {
	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("deleted-domain list test needs PostgreSQL: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.DomainSenderRule{}, &models.Suppression{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"error": map[string]interface{}{"code": "not_found", "message": "Domain not found"}}
	for _, tc := range []struct {
		operation, path string
	}{
		{"listAllowlist", "/api/v1/admin/domains/{id}/allowlist"},
		{"listBlocklist", "/api/v1/admin/domains/{id}/blocklist"},
		{"listSuppressions", "/api/v1/admin/domains/{id}/suppressions"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			tx := gdb.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { tx.Rollback() })
			routes := policyRoutes(NewSenderRuleHandler(tx))
			if tc.operation == "listSuppressions" {
				routes = policyRoutes(NewSuppressionHandler(tx))
			}
			domain := models.Domain{Name: "deleted-" + strings.ToLower(tc.operation) + ".test", ServerType: "restmail"}
			if err := tx.Create(&domain).Error; err != nil {
				t.Fatal(err)
			}
			list := func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, strings.ReplaceAll(tc.path, "{id}", fmt.Sprint(domain.ID)), nil))
				return rr
			}
			if rr := list(); rr.Code != http.StatusOK {
				t.Fatalf("existing domain answered %d %s", rr.Code, rr.Body)
			}
			if err := tx.Delete(&models.Domain{}, domain.ID).Error; err != nil {
				t.Fatal(err)
			}
			rr := list()
			if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
				t.Fatalf("deleted domain answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
			}
		})
	}
}
