package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
	"gorm.io/gorm"
)

// Named missing-domain requests must reach the real lookup before DNS or
// policy work. A successful linked lookup must still address an existing domain.
func TestDomainLookups_DocumentedMissingExamples(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Get struct {
				OperationID string `yaml:"operationId"`
				Parameters  []struct {
					Name     string `yaml:"name"`
					In       string `yaml:"in"`
					Required bool   `yaml:"required"`
					Examples map[string]struct {
						Value any `yaml:"value"`
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
	for _, tc := range []struct {
		path, operation, code string
		routes                func(*gorm.DB) http.Handler
	}{
		{"/api/v1/admin/dkim/{id}/rotation", "getDKIMRotation", "not_found", func(db *gorm.DB) http.Handler {
			return dkimRoutes(NewDKIMHandler(db, ""))
		}},
		{"/api/v1/admin/domains/{id}/dns", "domainDNSCheck", "domain_not_found", func(db *gorm.DB) http.Handler {
			h := NewDomainHandler(db, nil)
			h.lookupMX = func(_ context.Context, name string) ([]*net.MX, error) {
				return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
			}
			h.lookupTXT = func(_ context.Context, name string) ([]string, error) {
				return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
			}
			return domainRoutes(h)
		}},
		{"/api/v1/admin/domains/{id}/mta-sts", "getMtaStsPolicy", "not_found", func(db *gorm.DB) http.Handler {
			return policyRoutes(NewMTASTSHandler(db))
		}},
		{"/api/v1/admin/domains/{id}/relay", "getDomainRelay", "not_found", func(db *gorm.DB) http.Handler {
			return policyRoutes(NewRelayHandler(db, ""))
		}},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			op := doc.Paths[tc.path].Get
			if op.OperationID != tc.operation || len(op.Parameters) != 1 {
				t.Fatalf("unexpected operation or parameters: %+v", op)
			}
			parameter := op.Parameters[0]
			parameterCount, refusalCount := 2, 1
			if tc.operation == "getMtaStsPolicy" {
				parameterCount, refusalCount = 3, 2
			}
			if parameter.Name != "id" || parameter.In != "path" || !parameter.Required || len(parameter.Examples) != parameterCount {
				t.Fatalf("unexpected ID parameter: %+v", parameter)
			}
			missing := contractPathExampleID(t, parameter.Examples["missing"].Value)
			if missing != 2147483647 || contractPathExampleID(t, parameter.Examples["found"].Value) != 1 {
				t.Fatalf("unexpected path examples: %+v", parameter.Examples)
			}
			examples := op.Responses["404"].Content["application/json"].Examples
			want := map[string]interface{}{"error": map[string]interface{}{"code": tc.code, "message": "Domain not found"}}
			if len(examples) != refusalCount || !reflect.DeepEqual(jsonValue(t, examples["missing"].Value), want) {
				t.Fatalf("unexpected 404 examples: %+v", examples)
			}

			t.Run("PostgreSQL", func(t *testing.T) {
				gdb, err := connectTestDB()
				if err != nil {
					t.Skipf("domain lookup contract test needs PostgreSQL: %v", err)
				}
				if err := gdb.AutoMigrate(&models.Domain{}, &models.MTASTSPolicy{}, &models.DomainRelay{}, &models.DKIMRotationKey{}); err != nil {
					t.Fatal(err)
				}
				tx := gdb.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				t.Cleanup(func() { tx.Rollback() })
				domain := models.Domain{Name: "contract-" + strings.ToLower(tc.operation) + ".test", ServerType: "restmail"}
				if err := tx.Create(&domain).Error; err != nil {
					t.Fatal(err)
				}
				if err := tx.First(&domain, domain.ID).Error; err != nil {
					t.Fatal(err)
				}
				policy := models.MTASTSPolicy{DomainID: domain.ID, Mode: "testing", MXHosts: "mx.example.test", MaxAge: 86400, Active: true}
				if err := tx.Create(&policy).Error; err != nil {
					t.Fatal(err)
				}
				var count int64
				if err := tx.Model(&models.Domain{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("missing ID must not exist: count %d, error %v", count, err)
				}
				routes := tc.routes(tx)
				lookup := func(id uint) *httptest.ResponseRecorder {
					rr := httptest.NewRecorder()
					routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, strings.ReplaceAll(tc.path, "{id}", fmt.Sprint(id)), nil))
					return rr
				}
				rr := lookup(missing)
				if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
					t.Fatalf("missing ID answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
				}
				rr = lookup(domain.ID)
				if rr.Code != http.StatusOK {
					t.Fatalf("created ID answered %d %s", rr.Code, rr.Body)
				}
				body := decodeShapeBody(t, rr).(map[string]interface{})
				data := body["data"].(map[string]interface{})
				if tc.operation == "domainDNSCheck" {
					if data["domain"] != domain.Name {
						t.Fatalf("DNS lookup answered for another domain: %s", rr.Body)
					}
				} else if data["domain_id"] != float64(domain.ID) {
					t.Fatalf("lookup answered for another domain: %s", rr.Body)
				}
				var stored models.Domain
				if err := tx.First(&stored, domain.ID).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(stored, domain) {
					t.Fatal("lookup changed the stored domain")
				}
				if err := tx.Model(&models.Domain{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("lookup created missing ID: count %d, error %v", count, err)
				}
			})
		})
	}
}
