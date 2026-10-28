package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

const testCustomFilterPath = "/api/v1/admin/custom-filters/{id}/test"

// testCustomFilterNoDB sends body to TestCustomFilter for filter 1 on a handler
// with no database. reached reports whether it got as far as looking the
// filter up (which panics on the nil database).
func testCustomFilterNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	h := NewPipelineHandler(nil, nil, filters.JSSidecar{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/custom-filters/1/test", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				reached = true
			}
		}()
		pipelineRoutes(h).ServeHTTP(rr, req)
	}()
	return rr, reached
}

// The pairs vertrag compiles for testCustomFilter (#386): valid with the 200
// invalid_body with the 400, and unknown_field with the 422.
func TestTestCustomFilter_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, testCustomFilterPath, "testCustomFilter")

	want := map[string][]string{
		"request": {"invalid_body", "unknown_field", "valid"},
		"200":     {"valid"},
		"422":     {"invalid_body", "unknown_field"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("testCustomFilter's named examples are %v, want %v", got, want)
	}
	if want := []interface{}{}; !reflect.DeepEqual(requests["invalid_body"], want) {
		t.Errorf("invalid_body is %s, want []", mustJSON(requests["invalid_body"]))
	}
}

// invalid_body is refused by the real handler with exactly its documented 400,
// before the filter is looked up: the handler has no database.
func TestTestCustomFilter_DocumentedRefusal(t *testing.T) {
	requests, responses := postExamples(t, testCustomFilterPath, "testCustomFilter")
	if len(responses["422"]) == 0 {
		t.Fatalf("testCustomFilter's 400 names no example: the contract run cannot provoke it")
	}
	for name, want := range responses["422"] {
		t.Run(name, func(t *testing.T) {
			request, ok := requests[name]
			if !ok {
				t.Fatalf("the 400 example %q has no request example of the same name", name)
			}
			body := string(mustJSON(request))
			rr, reached := testCustomFilterNoDB(t, body)
			if reached {
				t.Fatalf("%s reached the database before it was refused", body)
			}
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status %d, want 400; body %s", body, rr.Code, rr.Body)
			}
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, parseShapeJSON(t, string(mustJSON(want)))) {
				t.Errorf("%s answered %s, want %s", body, rr.Body, mustJSON(want))
			}
		})
	}
}

// The body is optional: none, or an object, goes on to the filter's lookup.
// Anything else that does not decode is the 400.
func TestTestCustomFilter_Body(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		refused bool
	}{
		"no body":      {``, false},
		"empty object": {`{}`, false},
		"with email":   {`{"email":{"subject":"hi"}}`, false},
		"array":        {`[]`, true},
		"not json":     {`not json`, true},
		"truncated":    {`{"email":`, true},
		"email string": {`{"email":"someone@example.com"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			rr, reached := testCustomFilterNoDB(t, tc.body)
			if tc.refused {
				status := http.StatusBadRequest
				if name == "array" {
					status = http.StatusUnprocessableEntity
				}
				if reached || rr.Code != status {
					t.Fatalf("%q: reached %v, status %d, want a 400 before the lookup", tc.body, reached, rr.Code)
				}
				return
			}
			if !reached {
				t.Fatalf("%q: answered %d %s before the lookup", tc.body, rr.Code, rr.Body)
			}
		})
	}
}
