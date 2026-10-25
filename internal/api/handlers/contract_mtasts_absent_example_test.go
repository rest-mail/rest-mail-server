package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

func TestMtaStsPolicy_DocumentedAbsentExample(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Get struct {
				OperationID string `yaml:"operationId"`
				Parameters  []struct {
					Examples map[string]struct {
						Value uint `yaml:"value"`
					} `yaml:"examples"`
				} `yaml:"parameters"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"get"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	op := doc.Paths["/api/v1/admin/domains/{id}/mta-sts"].Get
	if op.OperationID != "getMtaStsPolicy" || len(op.Parameters) != 1 {
		t.Fatalf("unexpected lookup operation: %+v", op)
	}
	if op.Parameters[0].Examples["policy_absent"].Value != 2147483646 {
		t.Fatal("policy-absent request must retain its distinct fixture placeholder")
	}
	want := map[string]interface{}{"error": map[string]interface{}{
		"code": "not_found", "message": "No MTA-STS policy found for this domain",
	}}
	example := op.Responses["404"].Content["application/json"].Examples["policy_absent"]
	if !reflect.DeepEqual(jsonValue(t, example.Value), want) {
		t.Fatalf("policy-absent response is %s, want %s", mustJSON(example.Value), mustJSON(want))
	}

	t.Run("PostgreSQL", func(t *testing.T) {
		gdb, err := connectTestDB()
		if err != nil {
			t.Skipf("policy-absent contract test needs PostgreSQL: %v", err)
		}
		if err := gdb.AutoMigrate(&models.Domain{}, &models.MTASTSPolicy{}); err != nil {
			t.Fatal(err)
		}
		tx := gdb.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		t.Cleanup(func() { tx.Rollback() })
		absent := models.Domain{Name: "contract-policy-absent.test", ServerType: "restmail"}
		configured := models.Domain{Name: "contract-policy-configured.test", ServerType: "restmail"}
		for _, domain := range []*models.Domain{&absent, &configured} {
			if err := tx.Create(domain).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.First(domain, domain.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
		policy := models.MTASTSPolicy{DomainID: configured.ID, Mode: "testing", MXHosts: "mx.example.test", MaxAge: 86400, Active: true}
		if err := tx.Create(&policy).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.First(&policy, policy.ID).Error; err != nil {
			t.Fatal(err)
		}
		countPolicies := func() int64 {
			t.Helper()
			var count int64
			if err := tx.Model(&models.MTASTSPolicy{}).Where("domain_id = ?", absent.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			return count
		}
		if countPolicies() != 0 {
			t.Fatal("fixture unexpectedly has a policy")
		}
		rr := httptest.NewRecorder()
		policyRoutes(NewMTASTSHandler(tx)).ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/admin/domains/%d/mta-sts", absent.ID), nil))
		if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
			t.Fatalf("policy-absent lookup answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
		}
		if countPolicies() != 0 {
			t.Fatal("lookup created a policy")
		}
		for _, before := range []models.Domain{absent, configured} {
			var after models.Domain
			if err := tx.First(&after, before.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("lookup changed a stored domain")
			}
		}
		var after models.MTASTSPolicy
		if err := tx.First(&after, policy.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, policy) {
			t.Fatal("lookup changed another domain's policy")
		}
	})
}
