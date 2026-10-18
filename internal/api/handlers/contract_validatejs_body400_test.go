package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

// validateCustomFilter's 400 pair (#386): body_array is a body that is not an
// object at all, named on the 400 and nowhere else, and the 400 names it
// alone. The 400 is the handler's own refusal of a body it cannot decode, so
// it is documented as an ErrorResponse, not as a validation failure.
func TestValidateCustomFilter_BodyArrayPair(t *testing.T) {
	requests, responses := postExamples(t, validateJSPath, "validateCustomFilter")

	if got, want := requests["body_array"], []interface{}{}; !reflect.DeepEqual(got, want) {
		t.Errorf("body_array is %s, want an empty array %s", mustJSON(got), mustJSON(want))
	}
	if got := names(responses["400"]); !reflect.DeepEqual(got, []string{"body_array"}) {
		t.Errorf("the 400 names %v, want only body_array", got)
	}
	for status, examples := range responses {
		if _, ok := examples["body_array"]; ok && status != "400" {
			t.Errorf("body_array is named on the %s too: it pairs with that response as well", status)
		}
	}
	want := map[string]interface{}{
		"error": map[string]interface{}{"code": "bad_request", "message": "Invalid request body"},
	}
	if got := responses["400"]["body_array"]; !reflect.DeepEqual(got, want) {
		t.Errorf("the 400's body_array is %s, want %s", mustJSON(got), mustJSON(want))
	}

	schemas := validateJSResponseSchemas(t)
	for status, ref := range map[string]string{
		"400": "#/components/schemas/ErrorResponse",
		"422": "#/components/schemas/ValidationErrorResponse",
		"503": "#/components/schemas/ErrorResponse",
	} {
		if schemas[status] != ref {
			t.Errorf("the %s's schema is %q, want %q", status, schemas[status], ref)
		}
	}
}

// body_array is refused by the real handler with exactly its documented 400.
// The handler has no database and no sidecar: one asked would answer 503 and
// a database touched would panic, so the 400 is proved to come first. A
// malformed body gets the same refusal, and an object without a script
// (script_missing) is decoded and gets its 422 instead, so the 400 is for the
// body's shape, not for what it lacks.
func TestValidateCustomFilter_BodyArrayRefused(t *testing.T) {
	requests, responses := postExamples(t, validateJSPath, "validateCustomFilter")
	request, ok := requests["body_array"]
	if !ok {
		t.Fatal("validateCustomFilter names no body_array request example")
	}
	want, ok := responses["400"]["body_array"]
	if !ok {
		t.Fatal("validateCustomFilter's 400 names no body_array example")
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name   string
		body   string
		status int
		want   interface{}
	}{
		{"body_array", string(body), http.StatusBadRequest, want},
		{"malformed", "[", http.StatusBadRequest, want},
		{"script_missing", "{}", http.StatusUnprocessableEntity, responses["422"]["script_missing"]},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.want == nil {
				t.Fatalf("the spec documents no answer for %s", c.body)
			}
			rr := httptest.NewRecorder()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s reached the database before it was refused: %v", c.body, r)
					}
				}()
				NewPipelineHandler(nil, nil, filters.JSSidecar{}).ValidateCustomFilter(rr,
					httptest.NewRequest(http.MethodPost, validateJSPath, bytes.NewReader([]byte(c.body))))
			}()
			if rr.Code != c.status {
				t.Fatalf("%s: status %d, want %d; body %s", c.body, rr.Code, c.status, rr.Body)
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s: Content-Type %q, want application/json", c.body, ct)
			}
			assertDocumentedBody(t, c.body, rr, c.want)
		})
	}
}

// validateJSResponseSchemas reads the schema each of validateCustomFilter's
// responses refers to, by status, from the spec the API serves.
func validateJSResponseSchemas(t *testing.T) map[string]string {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]struct {
			Post struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Ref string `yaml:"$ref"`
						} `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := map[string]string{}
	for status, response := range doc.Paths[validateJSPath].Post.Responses {
		out[status] = response.Content["application/json"].Schema.Ref
	}
	return out
}
