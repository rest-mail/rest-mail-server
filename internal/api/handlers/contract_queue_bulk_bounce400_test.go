package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const bulkBounce400Path = "/api/v1/admin/queue/bulk-bounce"

// A nil database makes any lookup panic. Every request here must return its
// own refusal first; no valid, database-reaching request is sent (#388).
func TestBulkBounceQueue_PreLookupRefusals(t *testing.T) {
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
			queueRoutes(NewQueueHandler(nil)).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, bulkBounce400Path, strings.NewReader(tc.body)))
			fields := map[string]map[string]string{
				"array_body":            {"body": "the body is not a JSON object"},
				"string_body":           {"body": "the body is not a JSON object"},
				"null_body":             {"body": "the body is null"},
				"wrong_ids_shape":       {"ids": "must be an array"},
				"wrong_id_type":         {"ids[0]": "must be a non-negative integer"},
				"wrong_filter_shape":    {"filter": "must be an object"},
				"null_ids":              {"ids": "must not be null"},
				"absent_selection":      {"ids": "ids or filter required"},
				"empty_ids":             {"ids": "ids or filter required"},
				"null_filter":           {"ids": "ids or filter required"},
				"empty_ids_null_filter": {"ids": "ids or filter required"},
				"1001_ids":              {"ids": "max 1000 IDs per request"},
				"1001_ids_with_filter":  {"ids": "max 1000 IDs per request"},
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

func TestBulkBounceQueue400_Spec(t *testing.T) {
	for _, problem := range bulkPostPairProblems(t, "bulkBounceQueue", readBulkPostSpec(t, bulkBounce400Path)) {
		t.Error(problem)
	}
}
