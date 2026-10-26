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

// createPipeline's filters are required and are an array of objects
// (CreatePipelineRequest, #388). These tests send bodies to the real handler
// with no database: a refusal must come before anything is stored, and an
// accepted body must get as far as storing it.

// createPipelineNoDB sends body to CreatePipeline on a handler with no
// database. reached reports whether the handler got to gorm's request session (which
// panics on the nil database), checked against the panic's own stack so no
// other panic passes for it.
func createPipelineNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	h := NewPipelineHandler(nil, nil, filters.JSSidecar{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/pipelines", strings.NewReader(body))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			stack := string(debug.Stack())
			if !strings.Contains(stack, "gorm.io/gorm.(*DB).WithContext(") ||
				!strings.Contains(stack, "handlers.(*PipelineHandler).createPipeline(") {
				t.Fatalf("%s: panicked outside gorm's request session: %v\n%s", body, r, stack)
			}
			reached = true
		}()
		pipelineRoutes(h).ServeHTTP(rr, req)
	}()
	return rr, reached
}

func decodeShapeBody(t *testing.T, rr *httptest.ResponseRecorder) interface{} {
	t.Helper()
	var got interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	return got
}

func TestCreatePipelineFilterShape_Refused(t *testing.T) {
	want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation",`+
		`"details":{"fields":{"filters":"must be an array of filter objects"}}}}`)
	for name, filters := range map[string]string{
		"missing":        ``,
		"null":           `,"filters":null`,
		"object":         `,"filters":{"name":"header_validate","config":{}}`,
		"empty object":   `,"filters":{}`,
		"string":         `,"filters":"header_validate"`,
		"number":         `,"filters":1`,
		"true":           `,"filters":true`,
		"false":          `,"filters":false`,
		"null item":      `,"filters":[null]`,
		"string item":    `,"filters":["header_validate"]`,
		"number item":    `,"filters":[1]`,
		"boolean item":   `,"filters":[true]`,
		"array item":     `,"filters":[[]]`,
		"one bad item":   `,"filters":[{"name":"header_validate","config":{}},null]`,
		"nested arrays":  `,"filters":[[{"name":"header_validate"}]]`,
		"numeric name":   `,"filters":[{"name":12345}]`,
		"null name":      `,"filters":[{"name":null}]`,
		"boolean name":   `,"filters":[{"name":true}]`,
		"object name":    `,"filters":[{"name":{}}]`,
		"array name":     `,"filters":[{"name":[]}]`,
		"null config":    `,"filters":[{"config":null}]`,
		"string config":  `,"filters":[{"config":"bad"}]`,
		"numeric config": `,"filters":[{"config":12345}]`,
		"boolean config": `,"filters":[{"config":true}]`,
		"array config":   `,"filters":[{"config":[]}]`,
		"bad later name": `,"filters":[{"name":"header_validate","config":{}},{"name":12345}]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"domain_id":1,"direction":"inbound"` + filters + `}`
			rr, reached := createPipelineNoDB(t, body)
			if reached {
				t.Fatalf("%s reached the database before it was refused", body)
			}
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
			}
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
				t.Errorf("%s answered %s, want %s", body, rr.Body, mustJSON(want))
			}
		})
	}
}

// An empty list, an empty object and objects with keys beyond name and config
// are all the documented shape: each gets as far as storing the pipeline.
func TestCreatePipelineFilterShape_Accepted(t *testing.T) {
	for name, filters := range map[string]string{
		"empty":            `[]`,
		"empty object":     `[{}]`,
		"empty name":       `[{"name":"","config":{}}]`,
		"arbitrary config": `[{"config":{"nested":[null,1,"x"],"enabled":false}}]`,
		"documented":       `[{"name":"header_validate","config":{}}]`,
		"extra keys":       `[{"name":"header_validate","type":"check","enabled":true,"config":{}}]`,
		"several": `[{"name":"header_validate","config":{}},` +
			`{"name":"duplicate","enabled":false,"config":{"webhook_url":"https://example.com/hook"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"domain_id":1,"direction":"inbound","filters":` + filters + `}`
			rr, reached := createPipelineNoDB(t, body)
			if !reached {
				t.Fatalf("%s never reached the database: status %d, body %s", body, rr.Code, rr.Body)
			}
			if rr.Body.Len() != 0 {
				t.Errorf("%s answered %s before it was stored", body, rr.Body)
			}
		})
	}
}

// A body with neither a direction nor filters is refused for its direction
// alone, with exactly the 422 createPipeline's direction_missing documents.
func TestCreatePipelineFilterShape_DirectionFirst(t *testing.T) {
	want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation",`+
		`"details":{"fields":{"direction":"must be 'inbound' or 'outbound'"}}}}`)
	for _, body := range []string{`{"domain_id":1}`, `{"domain_id":1,"filters":null}`} {
		rr, reached := createPipelineNoDB(t, body)
		if reached {
			t.Fatalf("%s reached the database before it was refused", body)
		}
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
		}
		if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
			t.Errorf("%s answered %s, want %s", body, rr.Body, mustJSON(want))
		}
	}
}

// A well-shaped list is still refused for what is in it: a broken Sieve script
// and a duplicate filter's queue_recipient keep their 400s, and a body that is
// not JSON keeps its 400.
func TestCreatePipelineFilterShape_OtherRefusalsKept(t *testing.T) {
	for name, tc := range map[string]struct {
		body, code string
	}{
		"broken sieve": {
			`{"domain_id":1,"direction":"inbound","filters":` + missingRequireFilters + `}`, "invalid_sieve",
		},
		"queue recipient": {
			`{"domain_id":1,"direction":"inbound","filters":` + duplicateQueueFilters + `}`, "unsupported_filter",
		},
		"not json": {`{"domain_id":1,"direction":"inbound","filters":`, "bad_request"},
	} {
		t.Run(name, func(t *testing.T) {
			rr, reached := createPipelineNoDB(t, tc.body)
			if reached {
				t.Fatalf("%s reached the database before it was refused", tc.body)
			}
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400; body %s", tc.body, rr.Code, rr.Body)
			}
			var got struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", rr.Body, err)
			}
			if got.Error.Code != tc.code {
				t.Errorf("%s answered %s, want code %s", tc.body, rr.Body, tc.code)
			}
		})
	}
}

func parseShapeJSON(t *testing.T, s string) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
