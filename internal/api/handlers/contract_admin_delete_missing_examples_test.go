package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

// Send the exact named path from both descriptions through the
// registered delete routes. A missing target must not remove another row or
// create the target it failed to find.
func TestAdminDeletes_DocumentedMissingExamples(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Delete struct {
				OperationID string `yaml:"operationId"`
				Parameters  []struct {
					Name     string `yaml:"name"`
					In       string `yaml:"in"`
					Required bool   `yaml:"required"`
					Examples map[string]struct {
						Value uint `yaml:"value"`
					} `yaml:"examples"`
				} `yaml:"parameters"`
				RequestBody map[string]interface{} `yaml:"requestBody"`
				Responses   map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"delete"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	data, err := restmail.Examples.ReadFile("openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]map[string]struct {
		Request struct {
			Path map[string]uint `json:"path"`
			Body interface{}     `json:"body"`
		} `json:"request"`
		Response struct {
			Status int         `json:"status"`
			Body   interface{} `json:"body"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		resource, operation, code, message string
	}{
		{"domains", "deleteDomain", "domain_not_found", "Domain not found"},
		{"mailboxes", "deleteMailbox", "mailbox_not_found", "Mailbox not found"},
		{"aliases", "deleteAlias", "not_found", "Alias not found"},
		{"custom-filters", "deleteCustomFilter", "not_found", "Custom filter not found"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			path := "/api/v1/admin/" + tc.resource + "/{id}"
			op := doc.Paths[path].Delete
			if op.OperationID != tc.operation || len(op.Parameters) != 1 {
				t.Fatalf("unexpected delete operation or parameters: %+v", op)
			}
			parameter := op.Parameters[0]
			missing := parameter.Examples["missing"].Value
			if parameter.Name != "id" || parameter.In != "path" || !parameter.Required || missing != 2147483647 || parameter.Examples["found"].Value != 1 {
				t.Fatalf("unexpected ID parameter: %+v", parameter)
			}
			if len(op.RequestBody) != 0 {
				t.Fatalf("delete example must not need a request body: %+v", op.RequestBody)
			}
			want := map[string]interface{}{"error": map[string]interface{}{"code": tc.code, "message": tc.message}}
			refusals := op.Responses["404"].Content["application/json"].Examples
			if len(refusals) != 1 || !reflect.DeepEqual(jsonValue(t, refusals["missing"].Value), want) {
				t.Fatalf("unexpected 404 examples: %+v", refusals)
			}
			pair := generated[tc.operation]["missing"]
			if pair.Request.Path["id"] != missing || pair.Request.Body != nil || pair.Response.Status != 404 || !reflect.DeepEqual(pair.Response.Body, want) {
				t.Fatalf("generated-description pair differs: %+v", pair)
			}

			t.Run("PostgreSQL", func(t *testing.T) {
				gdb, err := connectTestDB()
				if err != nil {
					t.Skipf("admin update contract test requires PostgreSQL: %v", err)
				}
				if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.QuotaUsage{}, &models.Alias{}, &models.CustomFilter{}); err != nil {
					t.Fatal(err)
				}
				tx := gdb.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				t.Cleanup(func() { tx.Rollback() })
				domain := &models.Domain{Name: "contract-delete-missing.test", ServerType: "restmail", SendingPolicy: "bulk"}
				if err := tx.Create(domain).Error; err != nil {
					t.Fatal(err)
				}
				var record interface{} = domain
				var routes http.Handler
				switch tc.resource {
				case "domains":
					routes = domainRoutes(NewDomainHandler(tx, nil))
				case "mailboxes":
					record = &models.Mailbox{DomainID: domain.ID, LocalPart: "unchanged", Address: "unchanged@" + domain.Name, DisplayName: "Before"}
					routes = mailboxRoutes(NewMailboxHandler(tx, nil, nil))
				case "aliases":
					record = &models.Alias{DomainID: domain.ID, SourceAddress: "sales@" + domain.Name, DestinationAddress: "before@example.test", Active: true}
					routes = aliasRoutes(NewAliasHandler(tx))
				case "custom-filters":
					record = &models.CustomFilter{DomainID: domain.ID, Name: "unchanged", Description: "Before", FilterType: "action", Direction: "inbound", Config: json.RawMessage(`{"script":"original"}`)}
					routes = pipelineRoutes(NewPipelineHandler(tx, nil, filters.JSSidecar{}))
				}
				if tc.resource != "domains" {
					if err := tx.Create(record).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := tx.First(record).Error; err != nil {
					t.Fatal(err)
				}
				before := reflect.New(reflect.TypeOf(record).Elem()).Interface()
				storedID := reflect.ValueOf(record).Elem().FieldByName("ID").Interface()
				if err := tx.First(before, storedID).Error; err != nil {
					t.Fatal(err)
				}
				model := reflect.New(reflect.TypeOf(record).Elem()).Interface()
				var absent, countBefore, countAfter int64
				if err := tx.Model(model).Where("id = ?", missing).Count(&absent).Error; err != nil || absent != 0 {
					t.Fatalf("missing ID must be absent: count %d, error %v", absent, err)
				}
				if err := tx.Model(model).Count(&countBefore).Error; err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodDelete, strings.Replace(path, "{id}", fmt.Sprint(missing), 1), nil)
				rr := httptest.NewRecorder()
				routes.ServeHTTP(rr, request)
				if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
					t.Fatalf("missing delete answered %d %s, want404 %s", rr.Code, rr.Body, mustJSON(want))
				}
				after := reflect.New(reflect.TypeOf(record).Elem()).Interface()
				if err := tx.First(after, storedID).Error; err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("refusal changed the existing resource: before %+v, after %+v, error %v", before, after, err)
				}
				if err := tx.Model(model).Where("id = ?", missing).Count(&absent).Error; err != nil || absent != 0 {
					t.Fatalf("refusal created missing ID: count %d, error %v", absent, err)
				}
				if err := tx.Model(model).Count(&countAfter).Error; err != nil || countAfter != countBefore {
					t.Fatalf("refusal changed resource count: before %d, after %d, error %v", countBefore, countAfter, err)
				}
			})
		})
	}
}
