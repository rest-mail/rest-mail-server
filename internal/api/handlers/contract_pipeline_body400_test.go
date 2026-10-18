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

// createPipeline refuses a body that does not decode as CreatePipelineRequest
// with a 400 bad_request, before any field is validated (#388). The spec names
// one such request, body_array, and pairs it with the 400 of the same name, so
// the contract run sends it and expects exactly that answer.

// pipelineBodyRefusal is the 400 CreatePipeline sends for a body it cannot
// decode.
const pipelineBodyRefusal = `{"error":{"code":"bad_request","message":"Invalid request body"}}`

// The spec's body_array request is refused by the real handler, with no
// database, with exactly the 400 the spec documents for it.
func TestPipelineCreate_BodyArrayRefused(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	want := parseShapeJSON(t, pipelineBodyRefusal)
	if !reflect.DeepEqual(responses["400"]["body_array"], want) {
		t.Errorf("the spec documents body_array's 400 as %s, want %s",
			mustJSON(responses["400"]["body_array"]), pipelineBodyRefusal)
	}

	body := string(mustJSON(requests["body_array"]))
	if body != "[]" {
		t.Fatalf("body_array is %s, want []", body)
	}
	rr, reached := createPipelineNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("%s: status %d, want 400; body %s", body, rr.Code, rr.Body)
	}
	if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, responses["400"]["body_array"]) {
		t.Errorf("%s answered %s, the spec documents %s", body, rr.Body, mustJSON(responses["400"]["body_array"]))
	}
}

// Every body that does not decode as the request gets the same 400: one that
// is not JSON, one that is not an object, and an object with a field of the
// wrong JSON type. The fields are otherwise those of a valid request, so the
// type is the only reason it is refused; the last also has no filters, and is
// still refused as undecodable, before the filters are looked at.
func TestPipelineCreate_UndecodableBodiesRefused(t *testing.T) {
	want := parseShapeJSON(t, pipelineBodyRefusal)
	const filters = `"filters":[{"name":"header_validate","config":{}}]`
	for name, body := range map[string]string{
		"empty":                         ``,
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
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400; body %s", body, rr.Code, rr.Body)
			}
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
				t.Errorf("%s answered %s, want %s", body, rr.Body, pipelineBodyRefusal)
			}
		})
	}
}

// Controls: a body that decodes, with every field of its JSON type, is not
// refused as undecodable. Without its direction it gets direction_missing's
// documented 422, never the 400; so does null, which decodes as an empty body.
func TestPipelineCreate_DecodableBodyNot400(t *testing.T) {
	_, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	directionMissing := responses["422"]["direction_missing"]
	if directionMissing == nil {
		t.Fatal("the spec documents no direction_missing 422")
	}
	for name, body := range map[string]string{
		"no direction": `{"domain_id":1,"filters":[{"name":"header_validate","config":{}}],"active":true}`,
		"empty object": `{}`,
		"null":         `null`,
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

// pipelineCreate400 is createPipeline's documented 400: its schema and its
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
	r := doc.Paths["/api/v1/admin/pipelines"].Post.Responses["400"]
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
	if got := names(responses["400"]); !reflect.DeepEqual(got, []string{"body_array"}) {
		problems = append(problems, fmt.Sprintf("the 400 names %v, want [body_array]", got))
	}
	for status, examples := range responses {
		if _, ok := examples["body_array"]; ok && status != "400" {
			problems = append(problems, fmt.Sprintf("body_array is named on the %s too", status))
		}
	}
	var want interface{}
	_ = json.Unmarshal([]byte(pipelineBodyRefusal), &want)
	if !reflect.DeepEqual(responses["400"]["body_array"], want) {
		problems = append(problems, fmt.Sprintf("the 400 body_array is %s, want %s",
			mustJSON(responses["400"]["body_array"]), pipelineBodyRefusal))
	}
	if doc.Schema != "#/components/schemas/ErrorResponse" {
		problems = append(problems, fmt.Sprintf("the 400's schema is %q, want ErrorResponse", doc.Schema))
	}
	for _, code := range []string{"bad_request", "invalid_sieve", "unsupported_filter"} {
		if !strings.Contains(doc.Description, "`"+code+"`") {
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
		body, _ := res["400"]["body_array"].(map[string]interface{})
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
			delete(res["400"], "body_array")
		},
		"400 misnamed": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			res["400"]["body_not_object"] = res["400"]["body_array"]
			delete(res["400"], "body_array")
		},
		"400 pairs with created": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			res["400"]["created"] = res["400"]["body_array"]
		},
		"under the 422": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			res["422"]["body_array"] = res["400"]["body_array"]
			delete(res["400"], "body_array")
		},
		"another code": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			refusal(res)["code"] = "validation_failed"
		},
		"another message": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			refusal(res)["message"] = "Request body failed validation"
		},
		"details added": func(_ map[string]interface{}, res map[string]map[string]interface{}, _ *pipelineCreate400) {
			refusal(res)["details"] = map[string]interface{}{"fields": map[string]interface{}{"direction": "required"}}
		},
		"validation schema": func(_ map[string]interface{}, _ map[string]map[string]interface{}, doc *pipelineCreate400) {
			doc.Schema = "#/components/schemas/ValidationErrorResponse"
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
