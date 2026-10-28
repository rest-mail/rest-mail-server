package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

// #672 distinguishes JSON parsing from validation. A malformed JSON body is
// 400 bad_request; a parsed root of the wrong shape or a failing field rule is
// 422 validation_failed with details.fields, before database or engine work.

// pipelineBodyRefusal is the 400 CreatePipeline sends for a body it cannot
// decode.
const pipelineBodyRefusal = `{"error":{"code":"bad_request","message":"Invalid request body"}}`

// The spec's body_array request is refused by the real handler, with no
// database, with exactly the 422 the spec documents for it.
func TestPipelineCreate_BodyArrayRefused(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`)
	if !reflect.DeepEqual(responses["422"]["body_array"], want) {
		t.Errorf("the spec documents body_array's 422 as %s, want %s",
			mustJSON(responses["422"]["body_array"]), pipelineBodyRefusal)
	}

	body := string(mustJSON(requests["body_array"]))
	if body != "[]" {
		t.Fatalf("body_array is %s, want []", body)
	}
	rr, reached := createPipelineNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
	}
	if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, responses["422"]["body_array"]) {
		t.Errorf("%s answered %s, the spec documents %s", body, rr.Body, mustJSON(responses["422"]["body_array"]))
	}
}

// Syntax failures stay 400. Parsed root-shape and field-type failures are
// 422 with the failing body or field, before any database access.
func TestPipelineCreate_UndecodableBodiesRefused(t *testing.T) {
	want := parseShapeJSON(t, pipelineBodyRefusal)
	const filters = `"filters":[{"name":"header_validate","config":{}}]`
	for name, body := range map[string]string{
		"empty":                         ``,
		"null":                          `null`,
		"truncated":                     `{"domain_id":1,"direction":"inbound",`,
		"not json":                      `domain_id=1&direction=inbound`,
		"array of objects":              `[{"domain_id":1,"direction":"inbound",` + filters + `}]`,
		"string":                        `"inbound"`,
		"number":                        `1`,
		"boolean":                       `true`,
		"domain_id string":              `{"domain_id":"1","direction":"inbound",` + filters + `}`,
		"domain_id negative":            `{"domain_id":-1,"direction":"inbound",` + filters + `}`,
		"domain_id fraction":            `{"domain_id":1.5,"direction":"inbound",` + filters + `}`,
		"direction number":              `{"domain_id":1,"direction":1,` + filters + `}`,
		"direction array":               `{"domain_id":1,"direction":["inbound"],` + filters + `}`,
		"active string":                 `{"domain_id":1,"direction":"inbound",` + filters + `,"active":"yes"}`,
		"direction boolean, no filters": `{"domain_id":1,"direction":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			rr, reached := createPipelineNoDB(t, body)
			if reached {
				t.Fatalf("%s reached the database before it was refused", body)
			}
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
				"domain_id string":              {"domain_id": "must be a non-negative integer"},
				"domain_id negative":            {"domain_id": "must be a non-negative integer"},
				"domain_id fraction":            {"domain_id": "must be a non-negative integer"},
				"direction number":              {"direction": "must be a string"},
				"direction array":               {"direction": "must be a string"},
				"direction boolean, no filters": {"direction": "must be a string"},
				"active string":                 {"active": "must be a boolean"},
			}
			if fields := fieldTypes[name]; fields != nil {
				status = http.StatusUnprocessableEntity
				expected = parseShapeJSON(t, pipelineValidation(fields))
			}
			if rr.Code != status {
				t.Fatalf("%s: status %d, want %d; body %s", body, rr.Code, status, rr.Body)
			}
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, expected) {
				t.Errorf("%s answered %s, want %s", body, rr.Body, pipelineBodyRefusal)
			}
		})
	}
}

// Controls: a body that decodes, with every field of its JSON type, is not
// refused as undecodable. Without its direction it gets direction_missing's
// documented 422, distinct from malformed JSON's 400.
func TestPipelineCreate_DecodableBodyNot400(t *testing.T) {
	_, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	directionMissing := responses["422"]["direction_missing"]
	if directionMissing == nil {
		t.Fatal("the spec documents no direction_missing 422")
	}
	for name, body := range map[string]string{
		"no direction": `{"domain_id":1,"filters":[{"name":"header_validate","config":{}}],"active":true}`,
		"empty object": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			rr, reached := createPipelineNoDB(t, body)
			if reached {
				t.Fatalf("%s reached the database before it was refused", body)
			}
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
			}
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, directionMissing) {
				t.Errorf("%s answered %s, want direction_missing's %s", body, rr.Body, mustJSON(directionMissing))
			}
		})
	}
}

// pipelineCreate400 is createPipeline's documented shape-validation response: its schema and its
// description.
type pipelineCreate400 struct {
	Schema      string
	Description string
}

func readPipelineCreate400(t *testing.T) pipelineCreate400 {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]struct {
			Post struct {
				Responses map[string]struct {
					Description string `yaml:"description"`
					Content     map[string]struct {
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
	r := doc.Paths["/api/v1/admin/pipelines"].Post.Responses["422"]
	return pipelineCreate400{Schema: r.Content["application/json"].Schema.Ref, Description: r.Description}
}

// pipelineBody400Problems is what is wrong with createPipeline's body_array
// pair: the request must be an array, named on the 400 alone, whose example is
// the handler's decoding refusal in an ErrorResponse. The description keeps
// the other 400s the handler sends, so it does not claim every 400 is
// bad_request.
func pipelineBody400Problems(requests map[string]interface{}, responses map[string]map[string]interface{}, doc pipelineCreate400) []string {
	var problems []string
	if body, ok := requests["body_array"].([]interface{}); !ok || len(body) != 0 {
		problems = append(problems, fmt.Sprintf("body_array is %s, want []", mustJSON(requests["body_array"])))
	}
	if got := names(responses["422"]); !reflect.DeepEqual(got, []string{"active_wrong_type", "body_array", "direction_missing", "filters_missing", "null_body"}) {
		problems = append(problems, fmt.Sprintf("the 400 names %v, want [body_array null_body]", got))
	}
	for status, examples := range responses {
		if _, ok := examples["body_array"]; ok && status != "422" {
			problems = append(problems, fmt.Sprintf("body_array is named on the %s too", status))
		}
	}
	var want interface{}
	_ = json.Unmarshal([]byte(`{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`), &want)
	if !reflect.DeepEqual(responses["422"]["body_array"], want) {
		problems = append(problems, fmt.Sprintf("the 400 body_array is %s, want %s",
			mustJSON(responses["422"]["body_array"]), pipelineBodyRefusal))
	}
	if doc.Schema != "#/components/schemas/ValidationErrorResponse" {
		problems = append(problems, fmt.Sprintf("the 400's schema is %q, want ErrorResponse", doc.Schema))
	}
	for _, code := range []string{"validation_failed", "details.fields"} {
		if !strings.Contains(doc.Description, code) {
			problems = append(problems, fmt.Sprintf("the 400's description does not name %s", code))
		}
	}
	return problems
}

func TestPipelineCreate_BodyArrayPair(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	for _, problem := range pipelineBody400Problems(requests, responses, readPipelineCreate400(t)) {
		t.Error(problem)
	}
}

// Each way the body_array pair can be wrong is caught: the request or the
// 400 missing or misnamed, a request that is an object, the pair under
// another status, another code, message or schema, and a description that
// drops the other 400s. The mutations are made on copies, and the spec's
// examples are the same afterwards.
func TestPipelineCreate_BodyArrayPairMutations(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	doc := readPipelineCreate400(t)
	if problems := pipelineBody400Problems(requests, responses, doc); len(problems) != 0 {
		t.Fatalf("the spec's pair is wrong before any mutation: %v", problems)
	}
	before := mustJSON([]interface{}{requests, responses, doc})

	refusal := func(res map[string]map[string]interface{}) map[string]interface{} {
		body, _ := res["422"]["body_array"].(map[string]interface{})
		e, _ := body["error"].(map[string]interface{})
		return e
	}
	mutations := map[string]func(map[string]interface{}, map[string]map[string]interface{}, *pipelineCreate400){
		"request missing": func(req map[string]interface{}, _ map[string]map[string]interface{}, _ *pipelineCreate400) {
			delete(req, "body_array")
		},
		"request an object": func(req map[string]interface{}, _ map[string]map[string]interface{}, _ *pipelineCreate400) {
			req["body_array"] = map[string]interface{}{}
		},
		"400 missing": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			delete(res["422"], "body_array")
		},
		"400 misnamed": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			res["422"]["body_not_object"] = res["422"]["body_array"]
			delete(res["422"], "body_array")
		},
		"400 pairs with created": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			res["422"]["created"] = res["422"]["body_array"]
		},
		"under the 422": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			res["400"] = map[string]interface{}{"body_array": res["422"]["body_array"]}
			delete(res["422"], "body_array")
		},
		"another code": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			refusal(res)["code"] = "bad_request"
		},
		"another message": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			refusal(res)["message"] = "Invalid request body"
		},
		"details added": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			refusal(res)["details"] = map[string]interface{}{"fields": map[string]interface{}{"direction": "required"}}
		},
		"validation schema": func(_ map[string]interface{}, _ map[string]map[string]interface{}, doc *pipelineCreate400) {
			doc.Schema = "#/components/schemas/ErrorResponse"
		},
		"description drops the other 400s": func(_ map[string]interface{}, _ map[string]map[string]interface{}, doc *pipelineCreate400) {
			doc.Description = "The body is not JSON (`bad_request`)"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var copied struct {
				Requests  map[string]interface{}
				Responses map[string]map[string]interface{}
				Doc       pipelineCreate400
			}
			if err := json.Unmarshal(mustJSON(map[string]interface{}{
				"Requests": requests, "Responses": responses, "Doc": doc,
			}), &copied); err != nil {
				t.Fatal(err)
			}
			mutate(copied.Requests, copied.Responses, &copied.Doc)
			if problems := pipelineBody400Problems(copied.Requests, copied.Responses, copied.Doc); len(problems) == 0 {
				t.Errorf("the mutation %q is not caught", name)
			}
		})
	}

	if after := mustJSON([]interface{}{requests, responses, doc}); string(after) != string(before) {
		t.Errorf("the mutations changed the spec's examples:\n  before %s\n  after  %s", before, after)
	}
}
