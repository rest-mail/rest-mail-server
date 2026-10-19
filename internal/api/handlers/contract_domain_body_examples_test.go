package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

const createDomainPath = "/api/v1/admin/domains"

// domainBodyArrayRefused is the body the handler answers a request that is
// not a JSON object of its fields with: the canonical bad_request.
const domainBodyArrayRefused = `{"error":{"code":"bad_request","message":"Invalid request body"}}`

// createDomain's body_array request goes with its 400, and only there:
// body_array is JSON, but an array where the request is an object, so it is
// refused as it is read, before any field is looked at. The created and
// name_missing pairs are kept as they were; other refusals named on the 422
// are left to their own tests.
func TestDomainCreate_BodyArrayPair(t *testing.T) {
	requests, responses := postExamples(t, createDomainPath, "createDomain")
	for _, problem := range domainBodyArrayPairProblems(t, requests, responses) {
		t.Error(problem)
	}

	// The 400 is described as the error body the handler writes.
	if ref := domainCreateResponseSchemaRef(t, "400"); ref != "#/components/schemas/ErrorResponse" {
		t.Errorf("createDomain's 400 schema is %q, want the ErrorResponse", ref)
	}
}

func domainBodyArrayPairProblems(t *testing.T, requests map[string]interface{}, responses map[string]map[string]interface{}) []string {
	t.Helper()
	var problems []string

	if !reflect.DeepEqual(requests["body_array"], []interface{}{}) {
		problems = append(problems, fmt.Sprintf("body_array is %s, want the empty JSON array []",
			mustJSON(requests["body_array"])))
	}
	if got := names(responses["400"]); !reflect.DeepEqual(got, []string{"body_array"}) {
		problems = append(problems, fmt.Sprintf("the 400 names %v, want [body_array]", got))
	}
	for status, examples := range responses {
		if _, ok := examples["body_array"]; ok && status != "400" {
			problems = append(problems, fmt.Sprintf("body_array is named on the %s too", status))
		}
	}
	if !reflect.DeepEqual(responses["400"]["body_array"], parseShapeJSON(t, domainBodyArrayRefused)) {
		problems = append(problems, fmt.Sprintf("the 400 body_array is %s, want %s",
			mustJSON(responses["400"]["body_array"]), domainBodyArrayRefused))
	}

	// The pairs that were there before body_array, as they were.
	created := `{"name":"example.org","server_type":"restmail"}`
	if !reflect.DeepEqual(requests["created"], parseShapeJSON(t, created)) {
		problems = append(problems, fmt.Sprintf("created is %s, want %s", mustJSON(requests["created"]), created))
	}
	createdBody := `{"data":{"id":1,"name":"example.org","server_type":"restmail","active":false,` +
		`"default_quota_bytes":0,"sending_policy":"","created_at":"2026-01-01T00:00:00Z",` +
		`"updated_at":"2026-01-01T00:00:00Z"}}`
	if !reflect.DeepEqual(responses["201"]["created"], parseShapeJSON(t, createdBody)) {
		problems = append(problems, fmt.Sprintf("the 201 created is %s, want %s",
			mustJSON(responses["201"]["created"]), createdBody))
	}
	if !reflect.DeepEqual(requests["name_missing"], map[string]interface{}{}) {
		problems = append(problems, fmt.Sprintf("name_missing is %s, want {}", mustJSON(requests["name_missing"])))
	}
	nameMissing := `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
		`"details":{"fields":{"name":"required"}}}}`
	if !reflect.DeepEqual(responses["422"]["name_missing"], parseShapeJSON(t, nameMissing)) {
		problems = append(problems, fmt.Sprintf("the 422 name_missing is %s, want %s",
			mustJSON(responses["422"]["name_missing"]), nameMissing))
	}
	return problems
}

// domainCreateResponseSchemaRef is the schema $ref of createDomain's response
// with status, as the served spec says.
func domainCreateResponseSchemaRef(t *testing.T, status string) string {
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
	return doc.Paths[createDomainPath].Post.Responses[status].Content["application/json"].Schema.Ref
}

// createDomainNoDB sends body to the real handler, which has no database, and
// reports whether it got as far as the database.
func createDomainNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, createDomainPath, bytes.NewReader([]byte(body)))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			if recover() != nil {
				reached = true
			}
		}()
		NewDomainHandler(nil, nil).Create(rr, req)
	}()
	return rr, reached
}

// The real handler, with no database, answers body_array with exactly the
// 400 the spec documents for it, before anything is looked up or stored.
func TestDomainCreate_BodyArrayRefused(t *testing.T) {
	requests, responses := postExamples(t, createDomainPath, "createDomain")
	want := parseShapeJSON(t, domainBodyArrayRefused)
	if !reflect.DeepEqual(responses["400"]["body_array"], want) {
		t.Errorf("the spec documents body_array's 400 as %s, want %s",
			mustJSON(responses["400"]["body_array"]), domainBodyArrayRefused)
	}

	body := string(mustJSON(requests["body_array"]))
	if body != "[]" {
		t.Fatalf("body_array is sent as %s, want []", body)
	}
	rr, reached := createDomainNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("%s: status %d, want 400; body %s", body, rr.Code, rr.Body)
	}
	got := decodeShapeBody(t, rr)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered %s, want %s", body, rr.Body, domainBodyArrayRefused)
	}

	// Controls. The answer is none of the documented 422s. An array is refused
	// whatever it holds, the created request in one too: the 400 is the
	// array's, not its emptiness'. And each request named on a 422, {} among
	// them, is read and refused on its fields with exactly its documented 422
	// instead, not body_array's 400.
	if len(responses["422"]) == 0 {
		t.Fatal("createDomain's 422 names no example to hold the 400 apart from")
	}
	for name, refused := range responses["422"] {
		if reflect.DeepEqual(got, refused) {
			t.Errorf("body_array is answered with %s's 422", name)
		}
	}
	wrapped := "[" + string(mustJSON(requests["created"])) + "]"
	if rr, reached := createDomainNoDB(t, wrapped); reached || rr.Code != http.StatusBadRequest ||
		!reflect.DeepEqual(decodeShapeBody(t, rr), want) {
		t.Errorf("%s answered %d %s, want body_array's 400", wrapped, rr.Code, rr.Body)
	}
	for name, refused := range responses["422"] {
		object := string(mustJSON(requests[name]))
		rr, reached := createDomainNoDB(t, object)
		if reached || rr.Code != http.StatusUnprocessableEntity || !reflect.DeepEqual(decodeShapeBody(t, rr), refused) {
			t.Errorf("%s (%s) answered %d %s, want its documented 422 %s, not body_array's 400",
				name, object, rr.Code, rr.Body, mustJSON(refused))
		}
	}
}

// Each way the body_array pair can be wrong is caught, and the pairs kept
// with it: the request or the 400 missing or misnamed, the 400 documented
// under another status or naming another request too, with another code,
// message or body, a request that is not the empty array, and created or
// name_missing changed or dropped. The mutations are made on copies, and the
// spec's examples are the same afterwards.
func TestDomainCreate_BodyArrayPairMutations(t *testing.T) {
	requests, responses := postExamples(t, createDomainPath, "createDomain")
	if problems := domainBodyArrayPairProblems(t, requests, responses); len(problems) != 0 {
		t.Fatalf("the spec's pairs are wrong before any mutation: %v", problems)
	}
	before := mustJSON([]interface{}{requests, responses})

	refused := func(res map[string]map[string]interface{}) map[string]interface{} {
		return res["400"]["body_array"].(map[string]interface{})["error"].(map[string]interface{})
	}
	mutations := map[string]func(map[string]interface{}, map[string]map[string]interface{}){
		"request missing": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			delete(req, "body_array")
		},
		"request misnamed": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["array_body"] = req["body_array"]
			delete(req, "body_array")
		},
		"400 missing": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			delete(res, "400")
		},
		"400 misnamed": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["400"]["array_body"] = res["400"]["body_array"]
			delete(res["400"], "body_array")
		},
		"400 under 422": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["422"]["body_array"] = res["400"]["body_array"]
			delete(res, "400")
		},
		"also under 422": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["422"]["body_array"] = res["400"]["body_array"]
		},
		"400 names another": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["400"]["name_missing"] = res["400"]["body_array"]
		},
		"400 code": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			refused(res)["code"] = "invalid_domain"
		},
		"400 message": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			refused(res)["message"] = "Request body failed validation"
		},
		"400 body": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["400"]["body_array"] = res["422"]["name_missing"]
		},
		"request an object": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["body_array"] = map[string]interface{}{}
		},
		"request not empty": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["body_array"] = []interface{}{req["created"]}
		},
		"created changed": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["created"] = map[string]interface{}{"name": "example.org"}
		},
		"created pair dropped": func(req map[string]interface{}, res map[string]map[string]interface{}) {
			delete(req, "created")
			delete(res, "201")
		},
		"name_missing changed": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["name_missing"] = map[string]interface{}{"server_type": "restmail"}
		},
		"name_missing pair dropped": func(req map[string]interface{}, res map[string]map[string]interface{}) {
			delete(req, "name_missing")
			delete(res["422"], "name_missing")
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var copied []json.RawMessage
			var req map[string]interface{}
			var res map[string]map[string]interface{}
			if err := json.Unmarshal(before, &copied); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(copied[0], &req); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(copied[1], &res); err != nil {
				t.Fatal(err)
			}
			mutate(req, res)
			if problems := domainBodyArrayPairProblems(t, req, res); len(problems) == 0 {
				t.Errorf("the mutation %q is not caught", name)
			}
		})
	}

	requests, responses = postExamples(t, createDomainPath, "createDomain")
	if after := mustJSON([]interface{}{requests, responses}); !bytes.Equal(after, before) {
		t.Errorf("the spec's examples changed:\n  before %s\n  after  %s", before, after)
	}
}
