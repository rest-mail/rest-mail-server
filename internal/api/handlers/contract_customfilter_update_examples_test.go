package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

// Read this PATCH locally so the POST example reader stays unchanged.
func customFilterUpdateExamples(t *testing.T) (map[string]interface{}, map[string]map[string]interface{}) {
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
	op := doc.Paths["/api/v1/admin/custom-filters/{id}"].Patch
	if op.OperationID != "updateCustomFilter" {
		t.Fatalf("PATCH operation = %q, want updateCustomFilter", op.OperationID)
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

func TestCustomFilterUpdate_ExamplePairs(t *testing.T) {
	requests, responses := customFilterUpdateExamples(t)
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	want := map[string][]string{
		"request": {"config_array", "config_null", "direction_invalid", "filter_type_invalid", "name_empty", "updated"},
		"200":     {"updated"},
		"422":     {"config_array", "config_null", "direction_invalid", "filter_type_invalid", "name_empty"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updateCustomFilter pairs = %v, want %v", got, want)
	}
	for name, body := range map[string]string{
		"name_empty":          `{"name":"","enabled":true}`,
		"filter_type_invalid": `{"filter_type":"invalid","enabled":true}`,
		"direction_invalid":   `{"direction":"sideways","enabled":true}`,
		"config_array":        `{"config":[],"enabled":true}`,
		"config_null":         `{"config":null,"enabled":true}`,
		"updated":             `{"description":"Tags mail about invoices and receipts","config":{"script":"function filter(email) { return { action: \"continue\" }; }"},"enabled":true}`,
	} {
		if !reflect.DeepEqual(requests[name], parseShapeJSON(t, body)) {
			t.Errorf("%s request = %s, want %s", name, mustJSON(requests[name]), body)
		}
	}
	for name, fields := range map[string]string{
		"name_empty":          `{"name":"required"}`,
		"filter_type_invalid": `{"filter_type":"must be action or transform"}`,
		"direction_invalid":   `{"direction":"must be inbound, outbound or both"}`,
		"config_array":        `{"config":"must be an object"}`,
		"config_null":         `{"config":"must be an object"}`,
	} {
		want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":`+fields+`}}}`)
		if !reflect.DeepEqual(responses["422"][name], want) {
			t.Errorf("%s refusal = %s, want %s", name, mustJSON(responses["422"][name]), mustJSON(want))
		}
	}
}

func TestCustomFilterUpdate_DocumentedExamples(t *testing.T) {
	requests, responses := customFilterUpdateExamples(t)
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("custom filter update contract test requires PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.CustomFilter{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "customfilter-update-contract.test"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	cf := models.CustomFilter{DomainID: domain.ID, Name: "tag-invoices", Description: "Tags mail about invoices", FilterType: "action", Direction: "inbound", Config: json.RawMessage(`{"script":"original"}`), Enabled: false}
	if err := tx.Create(&cf).Error; err != nil {
		t.Fatal(err)
	}
	// GORM applies the model's default:true on create, even for a Go false.
	if err := tx.Model(&cf).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	h := NewPipelineHandler(tx, nil, filters.JSSidecar{})
	patch := func(t *testing.T, name string) *httptest.ResponseRecorder {
		t.Helper()
		body, ok := requests[name]
		if !ok {
			t.Fatalf("missing request %q", name)
		}
		id := uintToStr(cf.ID)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/custom-filters/"+id, bytes.NewReader(mustJSON(body)))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		pipelineRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	row := func(t *testing.T) models.CustomFilter {
		t.Helper()
		var got models.CustomFilter
		if err := tx.First(&got, cf.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := row(t)
	if before.Enabled {
		t.Fatal("seed must be disabled so every refusal also checks enabled:true was not stored")
	}
	for _, name := range names(responses["422"]) {
		t.Run(name, func(t *testing.T) {
			rr := patch(t, name)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, responses["422"][name]) {
				t.Errorf("response = %s, want %s", rr.Body, mustJSON(responses["422"][name]))
			}
			if after := row(t); !reflect.DeepEqual(after, before) {
				t.Errorf("refusal changed stored filter: before %+v, after %+v", before, after)
			}
		})
	}
	t.Run("updated", func(t *testing.T) {
		rr := patch(t, "updated")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
		}
		var response struct {
			Data models.CustomFilter `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		documented := responses["200"]["updated"].(map[string]interface{})["data"].(map[string]interface{})
		// IDs and timestamps vary, but the response and stored fields must match
		// the documented successful update and preserve the filter's identity.
		for _, got := range []models.CustomFilter{response.Data, row(t)} {
			if got.ID != cf.ID || got.DomainID != domain.ID || got.Name != documented["name"] || got.Description != documented["description"] ||
				got.FilterType != documented["filter_type"] || got.Direction != documented["direction"] || got.Enabled != documented["enabled"] ||
				!sameCustomFilterJSON(t, got.Config, mustJSON(documented["config"])) {
				t.Errorf("updated filter = %+v, want %s", got, mustJSON(documented))
			}
		}
	})
}
