package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// Read the PATCH examples from the served spec so the handler checks exercise
// exactly the requests vertrag pairs with each response (#386).
func domainUpdateExamples(t *testing.T) (map[string]interface{}, map[string]map[string]interface{}) {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Patch struct {
				OperationID string `yaml:"operationId"`
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
	op := doc.Paths["/api/v1/admin/domains/{id}"].Patch
	if op.OperationID != "updateDomain" {
		t.Fatalf("PATCH domain operation = %q, want updateDomain", op.OperationID)
	}
	requests := map[string]interface{}{}
	for name, example := range op.RequestBody.Content["application/json"].Examples {
		requests[name] = jsonValue(t, example.Value)
	}
	responses := map[string]map[string]interface{}{}
	for status, response := range op.Responses {
		for name, example := range response.Content["application/json"].Examples {
			if responses[status] == nil {
				responses[status] = map[string]interface{}{}
			}
			responses[status][name] = jsonValue(t, example.Value)
		}
	}
	return requests, responses
}

func TestDomainUpdate_ExamplePairs(t *testing.T) {
	requests, responses := domainUpdateExamples(t)
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	want := map[string][]string{
		"request": {"dmarc_policy_invalid", "missing", "sending_policy_invalid", "updated"},
		"404":     {"missing"},
		"200":     {"updated"},
		"422":     {"dmarc_policy_invalid", "sending_policy_invalid"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updateDomain's named examples = %v, want %v", got, want)
	}
	for name, policy := range map[string]string{"updated": "transactional", "sending_policy_invalid": "newsletter"} {
		if want := map[string]interface{}{"sending_policy": policy}; !reflect.DeepEqual(requests[name], want) {
			t.Errorf("%s request = %s, want %s", name, mustJSON(requests[name]), mustJSON(want))
		}
	}
	if want := map[string]interface{}{"dmarc_policy": "strict"}; !reflect.DeepEqual(requests["dmarc_policy_invalid"], want) {
		t.Errorf("dmarc_policy_invalid request = %s, want %s", mustJSON(requests["dmarc_policy_invalid"]), mustJSON(want))
	}
}

func TestDomainUpdate_DocumentedExamples(t *testing.T) {
	requests, responses := domainUpdateExamples(t)
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("domain update examples need Postgres: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "contract-domain-update.test", ServerType: "restmail", SendingPolicy: "bulk"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	// Reload timestamps at PostgreSQL's precision before comparing persistence.
	if err := tx.First(&domain, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	h := NewDomainHandler(tx, nil)
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"sending_policy_invalid", http.StatusUnprocessableEntity},
		{"dmarc_policy_invalid", http.StatusUnprocessableEntity},
		{"updated", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, ok := requests[tc.name]
			if !ok {
				t.Fatalf("missing request example %q", tc.name)
			}
			rr := httptest.NewRecorder()
			domainRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPatch, "/api/v1/admin/domains/"+uintToStr(domain.ID), 0,
				map[string]string{"id": uintToStr(domain.ID)}, string(mustJSON(request))))
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body)
			}
			var stored models.Domain
			if err := tx.First(&stored, domain.ID).Error; err != nil {
				t.Fatal(err)
			}
			if tc.status == http.StatusUnprocessableEntity {
				var got interface{}
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if want := responses["422"][tc.name]; !reflect.DeepEqual(got, want) {
					t.Errorf("refusal = %s, want %s", rr.Body, mustJSON(want))
				}
				if !reflect.DeepEqual(stored, domain) {
					t.Errorf("refused update changed domain: got %+v, want %+v", stored, domain)
				}
			} else {
				var got struct {
					Data models.Domain `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if stored.SendingPolicy != "transactional" || got.Data.SendingPolicy != stored.SendingPolicy || got.Data.ID != domain.ID {
					t.Errorf("success did not return and persist the policy: response %s, stored %+v", rr.Body, stored)
				}
			}
		})
	}
}
