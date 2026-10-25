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
func pipelineUpdateExamples(t *testing.T) (map[string]interface{}, map[string]map[string]interface{}) {
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
	op := doc.Paths["/api/v1/admin/pipelines/{id}"].Patch
	if op.OperationID != "updatePipeline" {
		t.Fatalf("PATCH operation = %q, want updatePipeline", op.OperationID)
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

func TestPipelineUpdate_ExamplePairs(t *testing.T) {
	requests, responses := pipelineUpdateExamples(t)
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	want := map[string][]string{
		"request": {"filters_null", "filters_object", "missing", "updated"},
		"200":     {"updated"},
		"404":     {"missing"},
		"422":     {"filters_null", "filters_object"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updatePipeline pairs = %v, want %v", got, want)
	}
	wantRequests := map[string]interface{}{
		"missing":        parseShapeJSON(t, `{"active":false}`),
		"updated":        parseShapeJSON(t, `{"filters":[{"name":"spf_check","config":{}}],"active":false}`),
		"filters_object": parseShapeJSON(t, `{"filters":{"name":"spf_check","config":{}},"active":false}`),
		"filters_null":   parseShapeJSON(t, `{"filters":null,"active":false}`),
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Errorf("requests = %s, want %s", mustJSON(requests), mustJSON(wantRequests))
	}
	wantRefusal := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"filters":"must be an array of filter objects"}}}}`)
	for name, refusal := range responses["422"] {
		if !reflect.DeepEqual(refusal, wantRefusal) {
			t.Errorf("%s's 422 = %s, want %s", name, mustJSON(refusal), mustJSON(wantRefusal))
		}
	}
}

func TestPipelineUpdate_DocumentedExamples(t *testing.T) {
	requests, responses := pipelineUpdateExamples(t)
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("pipeline update contract test requires PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "pipeline-update-contract.test"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	p := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[{"name":"header_validate","config":{}}]`), Active: true}
	if err := tx.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	h := NewPipelineHandler(tx, nil, filters.JSSidecar{})
	patch := func(t *testing.T, name string) *httptest.ResponseRecorder {
		t.Helper()
		body, ok := requests[name]
		if !ok {
			t.Fatalf("missing request %q", name)
		}
		id := uintToStr(p.ID)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/pipelines/"+id, bytes.NewReader(mustJSON(body)))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		pipelineRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	row := func(t *testing.T) models.Pipeline {
		t.Helper()
		var got models.Pipeline
		if err := tx.First(&got, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := row(t)
	for _, name := range []string{"filters_object", "filters_null"} {
		t.Run(name, func(t *testing.T) {
			rr := patch(t, name)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, responses["422"][name]) {
				t.Errorf("response = %s, want %s", rr.Body, mustJSON(responses["422"][name]))
			}
			if after := row(t); !reflect.DeepEqual(after, before) {
				t.Errorf("refusal changed stored pipeline: before %+v, after %+v", before, after)
			}
		})
	}
	t.Run("updated", func(t *testing.T) {
		rr := patch(t, "updated")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
		}
		var response struct {
			Data models.Pipeline `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		stored := row(t)
		for _, got := range []models.Pipeline{response.Data, stored} {
			if got.ID != p.ID || got.DomainID != domain.ID || got.Direction != before.Direction || got.Active ||
				!sameJSON(t, got.Filters, mustJSON(requests["updated"].(map[string]interface{})["filters"])) {
				t.Errorf("updated pipeline = %+v, want the requested filters and active:false", got)
			}
		}
		// Dynamic IDs and timestamps vary; the documented success must still
		// describe the same filters and active flag the handler returned.
		documented := responses["200"]["updated"].(map[string]interface{})["data"].(map[string]interface{})
		if !sameJSON(t, response.Data.Filters, mustJSON(documented["filters"])) || documented["active"] != response.Data.Active {
			t.Errorf("successful response differs from documented update: %s", rr.Body)
		}
	})
}
