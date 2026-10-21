package handlers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

// testPipeline refuses a body that is not JSON with a 400 and one without an
// email with a 422, both before it looks the pipeline up (#388). These tests
// send bodies to the real handler with no database, so a body that got as far
// as the lookup panics and fails the test; none of them is a valid request.

// testPipelineNoDB sends body to TestPipeline on a handler with no database.
func testPipelineNoDB(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewPipelineHandler(nil, nil, filters.JSSidecar{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/pipelines/test", strings.NewReader(body))
	rr := httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s reached the database before it was refused: %v", body, r)
			}
		}()
		h.TestPipeline(rr, req)
	}()
	return rr
}

func checkTestPipelineRefusal(t *testing.T, body string, status int, want string) {
	t.Helper()
	rr := testPipelineNoDB(t, body)
	if rr.Code != status {
		t.Fatalf("%s: status %d, want %d; body %s", body, rr.Code, status, rr.Body)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: Content-Type %q, want application/json", body, ct)
	}
	if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, parseShapeJSON(t, want)) {
		t.Errorf("%s answered %s, want %s", body, rr.Body, want)
	}
}

// A body that is not JSON, or JSON of the wrong types, is the documented 400,
// even when it also has no email.
func TestPipelineTestErrorContract_NotJSONIs400(t *testing.T) {
	const want = `{"error":{"code":"bad_request","message":"Invalid request body"}}`
	for name, body := range map[string]string{
		"empty":                ``,
		"not json":             `not json`,
		"truncated":            `{"pipeline_id":1,"email":{"envelope":`,
		"truncated no email":   `{"pipeline_id":1`,
		"pipeline_id string":   `{"pipeline_id":"1","email":{}}`,
		"pipeline_id negative": `{"pipeline_id":-1,"email":{}}`,
		"email string":         `{"pipeline_id":1,"email":"someone@example.com"}`,
		"email array":          `{"pipeline_id":1,"email":[]}`,
		"array body":           `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			checkTestPipelineRefusal(t, body, http.StatusBadRequest, want)
		})
	}
}

// A JSON body with no email, or a null one, is the documented 422 naming the
// email field.
func TestPipelineTestErrorContract_NoEmailIs422(t *testing.T) {
	const want = `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
		`"details":{"fields":{"email":"required"}}}}`
	for name, body := range map[string]string{
		"empty object":        `{}`,
		"missing":             `{"pipeline_id":1}`,
		"null":                `{"pipeline_id":1,"email":null}`,
		"null no pipeline_id": `{"email":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			checkTestPipelineRefusal(t, body, http.StatusUnprocessableEntity, want)
		})
	}
}

// The spec documents exactly those two refusals beside the 200: the 400 as an
// ErrorResponse and the 422 as the ValidationErrorResponse the 422 above is.
func TestPipelineTestErrorContract_Documented(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	type media map[string]struct {
		Schema map[string]interface{} `yaml:"schema"`
	}
	type response struct {
		Content media `yaml:"content"`
	}
	var doc struct {
		Paths map[string]struct {
			Post struct {
				OperationID string              `yaml:"operationId"`
				Responses   map[string]response `yaml:"responses"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths["/api/v1/admin/pipelines/test"].Post
	if op.OperationID != "testPipeline" {
		t.Fatalf("POST /api/v1/admin/pipelines/test is %q, want testPipeline", op.OperationID)
	}

	var statuses []string
	for status := range op.Responses {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	if want := []string{"200", "400", "422"}; !reflect.DeepEqual(statuses, want) {
		t.Fatalf("testPipeline documents %v, want %v", statuses, want)
	}

	schemaRef := func(r response) interface{} {
		return r.Content["application/json"].Schema["$ref"]
	}
	if got := schemaRef(op.Responses["400"]); got != "#/components/schemas/ErrorResponse" {
		t.Errorf("testPipeline's 400 is %v, want an ErrorResponse", got)
	}
	if got := schemaRef(op.Responses["422"]); got != "#/components/schemas/ValidationErrorResponse" {
		t.Errorf("testPipeline's 422 is %v, want a ValidationErrorResponse", got)
	}
}
