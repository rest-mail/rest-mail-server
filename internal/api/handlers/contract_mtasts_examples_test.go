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
	yaml "go.yaml.in/yaml/v2"
)

// putExamples reads the named application/json examples of the PUT on path,
// as postExamples does for a POST.
func putExamples(t *testing.T, path, operationID string) (map[string]interface{}, map[string]map[string]interface{}) {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]struct {
			Put struct {
				OperationID string `yaml:"operationId"`
				RequestBody struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"requestBody"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"put"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths[path].Put
	if op.OperationID != operationID {
		t.Fatalf("PUT %s is %q, want %s", path, op.OperationID, operationID)
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

// The pairs vertrag compiles for setMtaStsPolicy: policy with the 201 and
// 200, each refusal with the 422 of its name, nothing else. Each refused
// request is policy with one field changed, so that field is the only reason
// it is refused.
func TestSetMtaStsPolicy_ExamplePairs(t *testing.T) {
	requests, responses := putExamples(t, "/api/v1/admin/domains/{id}/mta-sts", "setMtaStsPolicy")

	want := map[string][]string{
		"request": {"max_age_negative", "mode_invalid", "policy"},
		"200":     {"policy"},
		"201":     {"policy"},
		"422":     {"max_age_negative", "mode_invalid"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("setMtaStsPolicy's named examples are %v, want %v", got, want)
	}

	policy, _ := requests["policy"].(map[string]interface{})
	for name, field := range map[string]string{"mode_invalid": "mode", "max_age_negative": "max_age"} {
		refused, _ := requests[name].(map[string]interface{})
		if len(refused) != len(policy) {
			t.Errorf("%s is %s, want policy's fields", name, mustJSON(refused))
			continue
		}
		for k, v := range policy {
			if same := reflect.DeepEqual(refused[k], v); same == (k == field) {
				t.Errorf("%s.%s is %v, want policy's %v changed only in %s", name, k, refused[k], v, field)
			}
		}
	}
}

// Each refusal is answered by the real handler with exactly its documented
// 422 before the domain is looked up: the handler has no database.
func TestSetMtaStsPolicy_DocumentedRefusals(t *testing.T) {
	requests, responses := putExamples(t, "/api/v1/admin/domains/{id}/mta-sts", "setMtaStsPolicy")
	h := NewMTASTSHandler(nil)

	for name, want := range responses["422"] {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/domains/1/mta-sts", bytes.NewReader(mustJSON(requests[name])))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", "1")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rr := httptest.NewRecorder()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s reached the database before it was refused: %v", name, r)
					}
				}()
				policyRoutes(h).ServeHTTP(rr, req)
			}()
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
			var body interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("body = %s, want the documented %s", rr.Body, mustJSON(want))
			}
		})
	}
}
