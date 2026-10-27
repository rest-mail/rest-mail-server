package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

// Send the exact named body and path from both descriptions through the
// registered update routes. A missing target must not update another row or
// create the target it failed to find.
func TestAdminUpdates_DocumentedMissingExamples(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Patch struct {
				OperationID string `yaml:"operationId"`
				Parameters  []struct {
					Name     string `yaml:"name"`
					In       string `yaml:"in"`
					Required bool   `yaml:"required"`
					Examples map[string]struct {
						Value any `yaml:"value"`
					} `yaml:"examples"`
				} `yaml:"parameters"`
				RequestBody struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"requestBody"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"patch"`
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
			// Raw, because other operations' examples use non-integer IDs on purpose.
			Path map[string]json.RawMessage `json:"path"`
			Body interface{}                `json:"body"`
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
		{"domains", "updateDomain", "domain_not_found", "Domain not found"},
		{"mailboxes", "updateMailbox", "mailbox_not_found", "Mailbox not found"},
		{"aliases", "updateAlias", "not_found", "Alias not found"},
		{"custom-filters", "updateCustomFilter", "not_found", "Custom filter not found"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			path := "/api/v1/admin/" + tc.resource + "/{id}"
			op := doc.Paths[path].Patch
			if op.OperationID != tc.operation || len(op.Parameters) != 1 {
				t.Fatalf("unexpected update operation or parameters: %+v", op)
			}
			parameter := op.Parameters[0]
			missing := contractPathExampleID(t, parameter.Examples["missing"].Value)
			if parameter.Name != "id" || parameter.In != "path" || !parameter.Required || missing != 2147483647 || contractPathExampleID(t, parameter.Examples["found"].Value) != 1 {
				t.Fatalf("unexpected ID parameter: %+v", parameter)
			}
			bodies := op.RequestBody.Content["application/json"].Examples
			body := jsonValue(t, bodies["missing"].Value)
			if body == nil || !reflect.DeepEqual(body, jsonValue(t, bodies["updated"].Value)) {
				t.Fatalf("missing-target request must carry the valid update body: %+v", bodies)
			}
			want := map[string]interface{}{"error": map[string]interface{}{"code": tc.code, "message": tc.message}}
			refusals := op.Responses["404"].Content["application/json"].Examples
			if len(refusals) != 1 || !reflect.DeepEqual(jsonValue(t, refusals["missing"].Value), want) {
				t.Fatalf("unexpected 404 examples: %+v", refusals)
			}
			pair := generated[tc.operation]["missing"]
			if string(pair.Request.Path["id"]) != strconv.FormatUint(uint64(missing), 10) || !reflect.DeepEqual(pair.Request.Body, body) || pair.Response.Status != 404 || !reflect.DeepEqual(pair.Response.Body, want) {
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
				domain := &models.Domain{Name: "contract-update-missing.test", ServerType: "restmail", SendingPolicy: "bulk"}
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
				request := httptest.NewRequest(http.MethodPatch, strings.Replace(path, "{id}", fmt.Sprint(missing), 1), bytes.NewReader(mustJSON(body)))
				request.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				routes.ServeHTTP(rr, request)
				if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
					t.Fatalf("missing update answered %d %s, want404 %s", rr.Code, rr.Body, mustJSON(want))
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
