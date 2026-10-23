package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

const bulkRetry400Path = "/api/v1/admin/queue/bulk-retry"

// A nil database makes any lookup panic. Every request here must return its
// own refusal first; no valid, database-reaching request is sent (#388).
func TestBulkRetryQueue_PreLookupRefusals(t *testing.T) {
	ids := make([]uint, 1001)
	for i := range ids {
		ids[i] = uint(i + 1)
	}
	oversized, err := json.Marshal(map[string]interface{}{"ids": ids})
	if err != nil {
		t.Fatal(err)
	}
	oversizedWithFilter, err := json.Marshal(map[string]interface{}{"ids": ids, "filter": map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, message string
	}{
		{"empty_body", "", "Invalid request body"},
		{"malformed", `{"ids":`, "Invalid request body"},
		{"array_body", `[]`, "Invalid request body"},
		{"string_body", `"ids"`, "Invalid request body"},
		{"wrong_ids_shape", `{"ids":"1"}`, "Invalid request body"},
		{"wrong_id_type", `{"ids":["1"]}`, "Invalid request body"},
		{"wrong_filter_shape", `{"filter":[]}`, "Invalid request body"},
		{"absent_selection", `{}`, "ids or filter required"},
		{"null_body", `null`, "Invalid request body"},
		{"empty_ids", `{"ids":[]}`, "ids or filter required"},
		{"null_ids", `{"ids":null}`, "ids or filter required"},
		{"null_filter", `{"filter":null}`, "ids or filter required"},
		{"empty_ids_null_filter", `{"ids":[],"filter":null}`, "ids or filter required"},
		{"1001_ids", string(oversized), "max 1000 IDs per request"},
		{"1001_ids_with_filter", string(oversizedWithFilter), "max 1000 IDs per request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			queueRoutes(NewQueueHandler(nil)).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, bulkRetry400Path, strings.NewReader(tc.body)))
			fields := map[string]map[string]string{
				"wrong_ids_shape":      {"ids": "must be an array"},
				"wrong_id_type":        {"ids[0]": "must be a non-negative integer"},
				"wrong_filter_shape":   {"filter": "must be an object"},
				"null_ids":             {"ids": "must not be null"},
				"1001_ids":             {"ids": "max 1000 IDs per request"},
				"1001_ids_with_filter": {"ids": "max 1000 IDs per request"},
			}[tc.name]
			status := http.StatusBadRequest
			if fields != nil {
				status = http.StatusUnprocessableEntity
			}
			if rr.Code != status {
				t.Fatalf("status %d, want %d; body %s", rr.Code, status, rr.Body)
			}
			if got := rr.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type %q, want application/json", got)
			}
			var got map[string]interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			want := map[string]interface{}{"error": map[string]interface{}{"code": "bad_request", "message": tc.message}}
			if fields != nil {
				fieldValues := map[string]interface{}{}
				for k, v := range fields {
					fieldValues[k] = v
				}
				want = map[string]interface{}{"error": map[string]interface{}{"code": "validation_failed", "message": "Request body failed validation", "details": map[string]interface{}{"fields": fieldValues}}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("response %s, want %v", rr.Body, want)
			}
		})
	}
}

func TestBulkRetryQueue400_Spec(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths struct {
			Queue struct {
				Post struct {
					OperationID string                `yaml:"operationId"`
					Security    []map[string][]string `yaml:"security"`
					RequestBody struct {
						Required bool `yaml:"required"`
						Content  map[string]struct {
							Schema struct {
								Ref string `yaml:"$ref"`
							} `yaml:"schema"`
						} `yaml:"content"`
					} `yaml:"requestBody"`
					Responses map[string]struct {
						Content map[string]struct {
							Schema struct {
								Ref string `yaml:"$ref"`
							} `yaml:"schema"`
							Examples map[string]struct {
								Value map[string]interface{} `yaml:"value"`
							} `yaml:"examples"`
						} `yaml:"content"`
					} `yaml:"responses"`
				} `yaml:"post"`
			} `yaml:"/api/v1/admin/queue/bulk-retry"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	op := doc.Paths.Queue.Post
	if op.OperationID != "bulkRetryQueue" {
		t.Fatalf("operationId %q, want bulkRetryQueue", op.OperationID)
	}
	if !op.RequestBody.Required || op.RequestBody.Content["application/json"].Schema.Ref != "#/components/schemas/BulkQueueRequest" {
		t.Error("request body must remain required and use BulkQueueRequest")
	}
	wantSecurity := []map[string][]string{
		{"CookieAuth": {"queue:manage"}},
		{"BearerAuth": {"queue:manage"}},
		{"APIKeyAuth": {"queue:manage"}},
	}
	if !reflect.DeepEqual(op.Security, wantSecurity) {
		t.Errorf("security %v, want %v", op.Security, wantSecurity)
	}
	if _, ok := op.Responses["200"]; !ok {
		t.Error("missing existing 200 response")
	}
	if len(op.Responses) != 3 {
		t.Errorf("responses %v, want 200, 400 and 422", op.Responses)
	}
	content := op.Responses["400"].Content["application/json"]
	if content.Schema.Ref != "#/components/schemas/ErrorResponse" {
		t.Errorf("400 schema %q, want ErrorResponse", content.Schema.Ref)
	}
	wantMessages := map[string]string{
		"invalid_body":      "Invalid request body",
		"selection_missing": "ids or filter required",
	}
	if len(content.Examples) != len(wantMessages) {
		t.Errorf("400 has %d examples, want exactly two", len(content.Examples))
	}
	for name, message := range wantMessages {
		// yaml/v2 decodes nested interface maps using interface keys.
		want := map[string]interface{}{"error": map[interface{}]interface{}{"code": "bad_request", "message": message}}
		if got := content.Examples[name].Value; !reflect.DeepEqual(got, want) {
			t.Errorf("400 example %s = %v, want %v", name, got, want)
		}
	}
}
