package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline/filters"
)

// createCustomFilter's config is required and is an object
// (CreateCustomFilterRequest, #388). These tests send bodies to the real
// handler with no database: a refusal must come before anything is stored, and
// an accepted body must get as far as storing it.

// createCustomFilterNoDB sends body to CreateCustomFilter on a handler with no
// database. reached reports whether the handler got to its insert (gorm's
// Table, which panics on the nil database), checked against the panic's own
// stack so no other panic passes for it.
func createCustomFilterNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	h := NewPipelineHandler(nil, nil, filters.JSSidecar{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/custom-filters", strings.NewReader(body))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			stack := string(debug.Stack())
			if !strings.Contains(stack, "gorm.io/gorm.(*DB).Table(") ||
				!strings.Contains(stack, "handlers.(*PipelineHandler).createCustomFilter(") {
				t.Fatalf("%s: panicked outside gorm's Table: %v\n%s", body, r, stack)
			}
			reached = true
		}()
		pipelineRoutes(h).ServeHTTP(rr, req)
	}()
	return rr, reached
}

// refusedCustomFilter checks body was refused before the database with status
// and exactly the error body want.
func refusedCustomFilter(t *testing.T, body string, status int, want string) {
	t.Helper()
	rr, reached := createCustomFilterNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != status {
		t.Fatalf("%s: status %d, want %d; body %s", body, rr.Code, status, rr.Body)
	}
	if got, want := decodeShapeBody(t, rr), parseShapeJSON(t, want); !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered %s, want %s", body, rr.Body, mustJSON(want))
	}
}

const customFilterFields = `{"domain_id":1,"name":"tag-invoices","filter_type":"action","direction":"inbound"`

func TestCreateCustomFilterConfig_Refused(t *testing.T) {
	want := `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
		`"details":{"fields":{"config":"must be an object"}}}}`
	for name, config := range map[string]string{
		"missing":      ``,
		"null":         `,"config":null`,
		"empty array":  `,"config":[]`,
		"object array": `,"config":[{}]`,
		"string":       `,"config":"function filter(email) {}"`,
		"empty string": `,"config":""`,
		"number":       `,"config":1`,
		"true":         `,"config":true`,
		"false":        `,"config":false`,
	} {
		t.Run(name, func(t *testing.T) {
			refusedCustomFilter(t, customFilterFields+config+`}`, http.StatusUnprocessableEntity, want)
		})
	}
}

// An empty object and objects of any keys are the documented shape, with or
// without enabled: each gets as far as storing the filter.
func TestCreateCustomFilterConfig_Accepted(t *testing.T) {
	for name, config := range map[string]string{
		"empty":         `,"config":{}`,
		"script":        `,"config":{"script":"function filter(email) { return { action: \"continue\" }; }"}`,
		"any keys":      `,"config":{"tag":"invoice","rules":[1,"two",null],"nested":{"on":false}}`,
		"enabled false": `,"config":{},"enabled":false`,
		"enabled true":  `,"config":{"script":"noop"},"enabled":true`,
	} {
		t.Run(name, func(t *testing.T) {
			body := customFilterFields + config + `}`
			rr, reached := createCustomFilterNoDB(t, body)
			if !reached {
				t.Fatalf("%s never reached the database: status %d, body %s", body, rr.Code, rr.Body)
			}
			if rr.Body.Len() != 0 {
				t.Errorf("%s answered %s before it was stored", body, rr.Body)
			}
		})
	}
}

// The checks before config keep their answers: a body missing a required field
// or naming a type or direction outside the spec is refused for that alone,
// whatever its config, and a body that is not JSON keeps its 400.
func TestCreateCustomFilterConfig_EarlierRefusalsFirst(t *testing.T) {
	required := `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
		`"details":{"fields":{"name":"required","filter_type":"required","direction":"required"}}}}`
	for name, tc := range map[string]struct {
		body   string
		status int
		want   string
	}{
		"nothing": {`{}`, http.StatusUnprocessableEntity, required},
		"no name or config": {
			`{"domain_id":1,"filter_type":"action","direction":"inbound"}`, http.StatusUnprocessableEntity, required,
		},
		"no direction, null config": {
			`{"domain_id":1,"name":"tag-invoices","filter_type":"action","config":null}`,
			http.StatusUnprocessableEntity, required,
		},
		"bad type, array config": {
			`{"domain_id":1,"name":"tag-invoices","filter_type":"script","direction":"inbound","config":[]}`,
			http.StatusUnprocessableEntity,
			`{"error":{"code":"validation_failed","message":"Request body failed validation",` +
				`"details":{"fields":{"filter_type":"must be action or transform"}}}}`,
		},
		"bad direction, no config": {
			`{"domain_id":1,"name":"tag-invoices","filter_type":"action","direction":"sideways"}`,
			http.StatusUnprocessableEntity,
			`{"error":{"code":"validation_failed","message":"Request body failed validation",` +
				`"details":{"fields":{"direction":"must be inbound, outbound or both"}}}}`,
		},
		"not json": {
			customFilterFields + `,"config":`, http.StatusBadRequest,
			`{"error":{"code":"bad_request","message":"Invalid request body"}}`,
		},
		"config not json": {
			customFilterFields + `,"config":{]}`, http.StatusBadRequest,
			`{"error":{"code":"bad_request","message":"Invalid request body"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			refusedCustomFilter(t, tc.body, tc.status, tc.want)
		})
	}
}

func TestIsJSONObject(t *testing.T) {
	for raw, want := range map[string]bool{
		``:               false,
		`null`:           false,
		`[]`:             false,
		`[{}]`:           false,
		`"x"`:            false,
		`0`:              false,
		`true`:           false,
		`{"a":1`:         false,
		`{"a":1}{"b":2}`: false,
		`{}`:             true,
		` {"a":1} `:      true,
		`{"a":null}`:     true,
		`{"a":{"b":[]}}`: true,
	} {
		if got := isJSONObject(json.RawMessage(raw)); got != want {
			t.Errorf("isJSONObject(%q) = %v, want %v", raw, got, want)
		}
	}
}
