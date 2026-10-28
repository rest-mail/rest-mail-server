package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

// #672 distinguishes JSON parsing from validation. A malformed JSON body is
// 400 bad_request; a parsed root of the wrong shape or a failing field rule is
// 422 validation_failed with details.fields, before database or engine work.

const testFilterBodyPath = "/api/v1/admin/pipelines/test-filter"

// testFilterBodyRefusal is the 400 TestFilter sends for a body it cannot
// decode.
const testFilterBodyRefusal = `{"error":{"code":"bad_request","message":"Invalid request body"}}`

// testFilterFieldsMissing is the 422 TestFilter sends for a body that decodes
// without its filter name or its email: it names both either way.
const testFilterFieldsMissing = `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
	`"details":{"fields":{"filter_name":"required","email":"required"}}}}`

// testFilterNoEngine sends body to TestFilter on a handler with no database
// and no engine. reached reports whether the handler got to the engine's
// TestFilter (which panics on the nil engine), checked against the panic's own
// stack so no other panic passes for it.
func testFilterNoEngine(t *testing.T, body string) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	h := NewPipelineHandler(nil, nil, filters.JSSidecar{})
	req := httptest.NewRequest(http.MethodPost, testFilterBodyPath, strings.NewReader(body))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			stack := string(debug.Stack())
			if !strings.Contains(stack, "pipeline.(*Engine).TestFilter(") ||
				!strings.Contains(stack, "handlers.(*PipelineHandler).testFilter(") {
				t.Fatalf("%s: panicked outside the engine's TestFilter: %v\n%s", body, r, stack)
			}
			reached = true
		}()
		pipelineRoutes(h).ServeHTTP(rr, req)
	}()
	return rr, reached
}

// testFilterAnswerProblems is what is wrong with the handler's answer to a
// body: it must not reach the engine, and must be exactly the status and the
// JSON body wanted.
func testFilterAnswerProblems(rr *httptest.ResponseRecorder, reached bool, status int, want interface{}) []string {
	var problems []string
	if reached {
		problems = append(problems, "it reached the engine before it was refused")
	}
	if rr.Code != status {
		problems = append(problems, fmt.Sprintf("status %d, want %d", rr.Code, status))
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		problems = append(problems, fmt.Sprintf("Content-Type %q, want application/json", ct))
	}
	var got interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		problems = append(problems, fmt.Sprintf("body %q is not JSON: %v", rr.Body, err))
	} else if !reflect.DeepEqual(got, want) {
		problems = append(problems, fmt.Sprintf("answered %s, want %s", rr.Body, mustJSON(want)))
	}
	return problems
}

// testFilterSpec is what the spec documents for testFilter: its security, its
// request's schema, and each response's description, schema and named
// examples.
type testFilterSpec struct {
	OperationID string
	Security    []map[string][]string
	Request     string
	Responses   map[string]testFilterSpecResponse
}

type testFilterSpecResponse struct {
	Description string
	Schema      string
	Data        string
	Examples    map[string]interface{}
}

func readTestFilterSpec(t *testing.T) testFilterSpec {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	type schema struct {
		Ref        string `yaml:"$ref"`
		Properties map[string]struct {
			Ref string `yaml:"$ref"`
		} `yaml:"properties"`
	}
	var doc struct {
		Paths map[string]struct {
			Post struct {
				OperationID string                `yaml:"operationId"`
				Security    []map[string][]string `yaml:"security"`
				RequestBody struct {
					Content map[string]struct {
						Schema schema `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"requestBody"`
				Responses map[string]struct {
					Description string `yaml:"description"`
					Content     map[string]struct {
						Schema   schema `yaml:"schema"`
						Examples map[string]struct {
							Value interface{} `yaml:"value"`
						} `yaml:"examples"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths[testFilterBodyPath].Post
	spec := testFilterSpec{
		OperationID: op.OperationID,
		Security:    op.Security,
		Request:     op.RequestBody.Content["application/json"].Schema.Ref,
		Responses:   map[string]testFilterSpecResponse{},
	}
	for status, r := range op.Responses {
		c := r.Content["application/json"]
		res := testFilterSpecResponse{
			Description: r.Description,
			Schema:      c.Schema.Ref,
			Data:        c.Schema.Properties["data"].Ref,
			Examples:    map[string]interface{}{},
		}
		for name, example := range c.Examples {
			res.Examples[name] = jsonValue(t, example.Value)
		}
		spec.Responses[status] = res
	}
	return spec
}

// testFilterBody400Problems is what is wrong with testFilter's documented
// 400: an ErrorResponse whose invalid_body example is the handler's decoding
// refusal, named on the 400 alone, and a description that names both 400s the
// handler sends, so it does not claim every 400 is a decoding failure. The
// rest of the operation is as it was: its security, its request's schema and
// its 200.
func testFilterBody400Problems(spec testFilterSpec) []string {
	var problems []string
	if spec.OperationID != "testFilter" {
		problems = append(problems, fmt.Sprintf("POST %s is %q, want testFilter", testFilterBodyPath, spec.OperationID))
	}
	wantSecurity := []map[string][]string{
		{"CookieAuth": {"pipelines:write"}},
		{"BearerAuth": {"pipelines:write"}},
		{"APIKeyAuth": {"pipelines:write"}},
	}
	if !reflect.DeepEqual(spec.Security, wantSecurity) {
		problems = append(problems, fmt.Sprintf("security is %v, want %v", spec.Security, wantSecurity))
	}
	if spec.Request != "#/components/schemas/TestFilterRequest" {
		problems = append(problems, fmt.Sprintf("the request's schema is %q, want TestFilterRequest", spec.Request))
	}
	if success := spec.Responses["200"]; success.Data != "#/components/schemas/FilterResult" {
		problems = append(problems, fmt.Sprintf("the 200's data is %q, want FilterResult", success.Data))
	}

	bad := spec.Responses["422"]
	if bad.Schema != "#/components/schemas/ValidationErrorResponse" {
		problems = append(problems, fmt.Sprintf("the 400's schema is %q, want ErrorResponse", bad.Schema))
	}
	var want interface{}
	_ = json.Unmarshal([]byte(`{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`), &want)
	if !reflect.DeepEqual(bad.Examples["invalid_body"], want) {
		problems = append(problems, fmt.Sprintf("the 400's invalid_body is %s, want %s",
			mustJSON(bad.Examples["invalid_body"]), testFilterBodyRefusal))
	}
	for status, res := range spec.Responses {
		if _, ok := res.Examples["invalid_body"]; ok && status != "422" {
			problems = append(problems, fmt.Sprintf("invalid_body is named on the %s too", status))
		}
	}
	for _, phrase := range []string{"validation_failed", "details.fields"} {
		if !strings.Contains(bad.Description, phrase) {
			problems = append(problems, fmt.Sprintf("the 400's description does not say %s", phrase))
		}
	}
	return problems
}

func TestTestFilterBody400_Documented(t *testing.T) {
	for _, problem := range testFilterBody400Problems(readTestFilterSpec(t)) {
		t.Error(problem)
	}
}

// Every body that does not decode as the request gets exactly the documented
// invalid_body 400: one that is empty or not JSON, one that is not an object,
// and an object with a filter_name or an email (at any depth) of the wrong
// JSON type. The last has no email, and is still refused as undecodable,
// before the missing email is looked at.
func TestTestFilterBody400_UndecodableBodiesRefused(t *testing.T) {
	want := parseShapeJSON(t, testFilterBodyRefusal)
	if want == nil {
		t.Fatal("the spec documents no invalid_body 400")
	}
	const email = `"email":{"envelope":{"mail_from":"someone@example.com","rcpt_to":["eve@restmail.test"]}}`
	for name, body := range map[string]string{
		"empty":                      ``,
		"not json":                   `filter_name=header_validate`,
		"truncated":                  `{"filter_name":"header_validate",`,
		"empty array":                `[]`,
		"array of objects":           `[{"filter_name":"header_validate","config":{},` + email + `}]`,
		"string":                     `"header_validate"`,
		"number":                     `1`,
		"boolean":                    `true`,
		"filter_name number":         `{"filter_name":1,"config":{},` + email + `}`,
		"filter_name object":         `{"filter_name":{"name":"header_validate"},"config":{},` + email + `}`,
		"email string":               `{"filter_name":"header_validate","config":{},"email":"someone@example.com"}`,
		"email array":                `{"filter_name":"header_validate","config":{},"email":[]}`,
		"rcpt_to string":             `{"filter_name":"header_validate","config":{},"email":{"envelope":{"rcpt_to":"eve@restmail.test"}}}`,
		"filter_name bool, no email": `{"filter_name":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			rr, reached := testFilterNoEngine(t, body)
			status, expected := http.StatusBadRequest, want
			if json.Valid([]byte(body)) {
				status = http.StatusUnprocessableEntity
				message := "the body is not a JSON object"
				if body == "null" {
					message = "the body is null"
				}
				expected = parseShapeJSON(t, pipelineValidation(map[string]string{"body": message}))
			}
			fieldTypes := map[string]map[string]string{
				"filter_name number":         {"filter_name": "must be a string"},
				"filter_name object":         {"filter_name": "must be a string"},
				"filter_name bool, no email": {"filter_name": "must be a string"},
				"email string":               {"email": "must be an object"},
				"email array":                {"email": "must be an object"},
				"rcpt_to string":             {"email.envelope.rcpt_to": "must be an array"},
			}
			if fields := fieldTypes[name]; fields != nil {
				status = http.StatusUnprocessableEntity
				expected = parseShapeJSON(t, pipelineValidation(fields))
			}
			for _, problem := range testFilterAnswerProblems(rr, reached, status, expected) {
				t.Errorf("%s: %s", body, problem)
			}
		})
	}
}

// Controls: a body that decodes, without its filter name or its email, is not
// refused as undecodable; it gets the 422 naming both fields, never the 400.
// A config of the wrong JSON type also decodes; when the name or email is
// missing, those required fields are reported before config is validated.
func TestTestFilterBody400_DecodableBodiesGet422(t *testing.T) {
	want := parseShapeJSON(t, testFilterFieldsMissing)
	const email = `"email":{"envelope":{"mail_from":"someone@example.com","rcpt_to":["eve@restmail.test"]}}`
	for name, body := range map[string]string{
		"empty object":       `{}`,
		"null":               `null`,
		"no email":           `{"filter_name":"header_validate","config":{}}`,
		"email null":         `{"filter_name":"header_validate","config":{},"email":null}`,
		"no filter_name":     `{"config":{},` + email + `}`,
		"filter_name empty":  `{"filter_name":"","config":{},` + email + `}`,
		"config a number":    `{"config":1}`,
		"config an array":    `{"config":[],"email":null}`,
		"unknown field only": `{"filter":"header_validate"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rr, reached := testFilterNoEngine(t, body)
			status, expected := http.StatusUnprocessableEntity, want
			if name == "null" {
				status = http.StatusUnprocessableEntity
				expected = parseShapeJSON(t, pipelineValidation(map[string]string{"body": "the body is null"}))
			}
			if name == "unknown field only" {
				expected = parseShapeJSON(t, pipelineValidation(map[string]string{"filter": "not allowed"}))
			}
			for _, problem := range testFilterAnswerProblems(rr, reached, status, expected) {
				t.Errorf("%s: %s", body, problem)
			}
		})
	}
}

// Each way the documented 400 can be wrong is caught: invalid_body missing or
// moved under the 422, another code or message, details added, another schema,
// a description that drops either 400 or the decoding, and the operation's
// 200, request schema or security changed. The mutations are made on copies,
// and the spec is the same afterwards.
func TestTestFilterBody400_DocumentedMutations(t *testing.T) {
	spec := readTestFilterSpec(t)
	if problems := testFilterBody400Problems(spec); len(problems) != 0 {
		t.Fatalf("the spec's 400 is wrong before any mutation: %v", problems)
	}
	before := mustJSON(spec)

	refusal := func(s *testFilterSpec) map[string]interface{} {
		body, _ := s.Responses["422"].Examples["invalid_body"].(map[string]interface{})
		e, _ := body["error"].(map[string]interface{})
		return e
	}
	response := func(s *testFilterSpec, status string, change func(*testFilterSpecResponse)) {
		r := s.Responses[status]
		change(&r)
		s.Responses[status] = r
	}
	mutations := map[string]func(*testFilterSpec){
		"API key auth missing": func(s *testFilterSpec) {
			s.Security = s.Security[:2]
		},
		"API key capability changed": func(s *testFilterSpec) {
			s.Security[2] = map[string][]string{"APIKeyAuth": {"pipelines:read"}}
		},
		"invalid_body missing": func(s *testFilterSpec) {
			delete(s.Responses["422"].Examples, "invalid_body")
		},
		"invalid_body under the 422 too": func(s *testFilterSpec) {
			s.Responses["400"] = testFilterSpecResponse{Examples: map[string]interface{}{
				"invalid_body": s.Responses["422"].Examples["invalid_body"],
			}}
		},
		"another code": func(s *testFilterSpec) {
			refusal(s)["code"] = "filter_error"
		},
		"another message": func(s *testFilterSpec) {
			refusal(s)["message"] = "Invalid request body"
		},
		"details added": func(s *testFilterSpec) {
			refusal(s)["details"] = map[string]interface{}{"fields": map[string]interface{}{"email": "required"}}
		},
		"validation schema": func(s *testFilterSpec) {
			response(s, "422", func(r *testFilterSpecResponse) { r.Schema = "#/components/schemas/ErrorResponse" })
		},
		"description drops filter_error": func(s *testFilterSpec) {
			response(s, "422", func(r *testFilterSpecResponse) {
				r.Description = "`bad_request` when the body does not decode as the request."
			})
		},
		"description claims every 400": func(s *testFilterSpec) {
			response(s, "422", func(r *testFilterSpecResponse) { r.Description = "Invalid request body" })
		},
		"description drops the decoding": func(s *testFilterSpec) {
			response(s, "422", func(r *testFilterSpecResponse) {
				r.Description = "`bad_request` or `filter_error`."
			})
		},
		"200 data changed": func(s *testFilterSpec) {
			response(s, "200", func(r *testFilterSpecResponse) { r.Data = "#/components/schemas/ExecutionResult" })
		},
		"request schema changed": func(s *testFilterSpec) {
			s.Request = "#/components/schemas/TestPipelineRequest"
		},
		"security dropped": func(s *testFilterSpec) {
			s.Security = nil
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var copied testFilterSpec
			if err := json.Unmarshal(before, &copied); err != nil {
				t.Fatal(err)
			}
			mutate(&copied)
			if problems := testFilterBody400Problems(copied); len(problems) == 0 {
				t.Errorf("%s is not caught", name)
			}
		})
	}
	if after := mustJSON(spec); string(after) != string(before) {
		t.Errorf("the mutations changed the spec's 400:\n%s\nwant\n%s", after, before)
	}
}

// Each way the handler's answer can be wrong is caught: a 422 or a
// filter_error where the decoding refusal belongs, another content type, and
// a refusal that comes only after the engine is reached.
func TestTestFilterBody400_AnswerMutations(t *testing.T) {
	want := parseShapeJSON(t, testFilterBodyRefusal)
	answer := func(status int, contentType, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		rr.Header().Set("Content-Type", contentType)
		rr.WriteHeader(status)
		_, _ = rr.WriteString(body)
		return rr
	}
	if problems := testFilterAnswerProblems(answer(http.StatusBadRequest, "application/json", testFilterBodyRefusal),
		false, http.StatusBadRequest, want); len(problems) != 0 {
		t.Fatalf("the documented refusal is not accepted: %v", problems)
	}
	for name, tc := range map[string]struct {
		rr      *httptest.ResponseRecorder
		reached bool
	}{
		"a 422 for the missing fields": {rr: answer(http.StatusUnprocessableEntity, "application/json", testFilterFieldsMissing)},
		"the 422's status alone":       {rr: answer(http.StatusUnprocessableEntity, "application/json", testFilterBodyRefusal)},
		"a filter_error":               {rr: answer(http.StatusBadRequest, "application/json", `{"error":{"code":"filter_error","message":"create filter : unknown filter"}}`)},
		"text/plain":                   {rr: answer(http.StatusBadRequest, "text/plain; charset=utf-8", testFilterBodyRefusal)},
		"not JSON":                     {rr: answer(http.StatusBadRequest, "application/json", "Invalid request body\n")},
		"after the engine":             {rr: answer(http.StatusBadRequest, "application/json", testFilterBodyRefusal), reached: true},
	} {
		t.Run(name, func(t *testing.T) {
			if problems := testFilterAnswerProblems(tc.rr, tc.reached, http.StatusBadRequest, want); len(problems) == 0 {
				t.Errorf("%s is not caught", name)
			}
		})
	}
}
