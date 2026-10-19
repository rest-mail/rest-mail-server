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

const aliasesPath = "/api/v1/admin/aliases"

// aliasBodyArrayRefused is the body the handler answers a request that is not
// a JSON object of its fields with: the canonical bad_request.
const aliasBodyArrayRefused = `{"error":{"code":"bad_request","message":"Invalid request body"}}`

// aliasSourceInvalidRefused is the body the handler answers a source that is
// not one mail address with: the canonical 422 naming source_address.
const aliasSourceInvalidRefused = `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
	`"details":{"fields":{"source_address":"must be a valid email address"}}}}`

// The pairs vertrag compiles for createAlias: created with the 201,
// body_array with the 400, source_missing and source_invalid with the 422s,
// nothing else.
// body_array is JSON, but an array where the request is an object, so it is
// refused as it is read, before any field is looked at.
func TestAliasCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, aliasesPath, "createAlias")
	for _, problem := range aliasCreatePairProblems(t, requests, responses) {
		t.Error(problem)
	}

	// The 400 is described as the error body the handler writes.
	if ref := aliasCreateResponseSchemaRef(t, "400"); ref != "#/components/schemas/ErrorResponse" {
		t.Errorf("createAlias's 400 schema is %q, want the ErrorResponse", ref)
	}
}

func aliasCreatePairProblems(t *testing.T, requests map[string]interface{}, responses map[string]map[string]interface{}) []string {
	t.Helper()
	var problems []string
	want := map[string][]string{
		"request": {"body_array", "created", "source_invalid", "source_missing"},
		"201":     {"created"},
		"400":     {"body_array"},
		"422":     {"source_invalid", "source_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		problems = append(problems, fmt.Sprintf("createAlias's named examples are %v, want %v", got, want))
	}

	created := map[string]interface{}{
		"source_address":      "sales@restmail.test",
		"destination_address": "admin@restmail.test",
	}
	if !reflect.DeepEqual(requests["created"], created) {
		problems = append(problems, fmt.Sprintf("created is %s, want the valid alias request %s",
			mustJSON(requests["created"]), mustJSON(created)))
	}
	if !reflect.DeepEqual(requests["source_missing"], map[string]interface{}{"destination_address": "admin@restmail.test"}) {
		problems = append(problems, fmt.Sprintf("source_missing is %s, want created without its source_address",
			mustJSON(requests["source_missing"])))
	}
	invalid := map[string]interface{}{
		"source_address":      "not-an-email",
		"destination_address": "admin@restmail.test",
	}
	if !reflect.DeepEqual(requests["source_invalid"], invalid) {
		problems = append(problems, fmt.Sprintf("source_invalid is %s, want created with a source that is not an address %s",
			mustJSON(requests["source_invalid"]), mustJSON(invalid)))
	}
	if !reflect.DeepEqual(requests["body_array"], []interface{}{}) {
		problems = append(problems, fmt.Sprintf("body_array is %s, want the empty JSON array []",
			mustJSON(requests["body_array"])))
	}

	if !reflect.DeepEqual(responses["400"]["body_array"], parseShapeJSON(t, aliasBodyArrayRefused)) {
		problems = append(problems, fmt.Sprintf("the 400 body_array is %s, want %s",
			mustJSON(responses["400"]["body_array"]), aliasBodyArrayRefused))
	}
	sourceMissing := `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
		`"details":{"fields":{"source_address":"required"}}}}`
	if !reflect.DeepEqual(responses["422"]["source_missing"], parseShapeJSON(t, sourceMissing)) {
		problems = append(problems, fmt.Sprintf("the 422 source_missing is %s, want %s",
			mustJSON(responses["422"]["source_missing"]), sourceMissing))
	}
	if !reflect.DeepEqual(responses["422"]["source_invalid"], parseShapeJSON(t, aliasSourceInvalidRefused)) {
		problems = append(problems, fmt.Sprintf("the 422 source_invalid is %s, want %s",
			mustJSON(responses["422"]["source_invalid"]), aliasSourceInvalidRefused))
	}
	createdBody, _ := responses["201"]["created"].(map[string]interface{})
	createdData, _ := createdBody["data"].(map[string]interface{})
	if createdData["source_address"] != created["source_address"] ||
		createdData["destination_address"] != created["destination_address"] {
		problems = append(problems, fmt.Sprintf("the 201 created is %s, want the alias created describes",
			mustJSON(responses["201"]["created"])))
	}
	return problems
}

// aliasCreateResponseSchemaRef is the schema $ref of createAlias's response
// with status, as the served spec says.
func aliasCreateResponseSchemaRef(t *testing.T, status string) string {
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
	return doc.Paths[aliasesPath].Post.Responses[status].Content["application/json"].Schema.Ref
}

// createAliasNoDB sends body to the real handler, which has no database, and
// reports whether it got as far as the database.
func createAliasNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, aliasesPath, bytes.NewReader([]byte(body)))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			if recover() != nil {
				reached = true
			}
		}()
		NewAliasHandler(nil).Create(rr, req)
	}()
	return rr, reached
}

// The real handler, with no database, answers body_array with exactly the
// 400 the spec documents for it, before anything is looked up or stored.
func TestAliasCreate_BodyArrayRefused(t *testing.T) {
	requests, responses := postExamples(t, aliasesPath, "createAlias")
	want := parseShapeJSON(t, aliasBodyArrayRefused)
	if !reflect.DeepEqual(responses["400"]["body_array"], want) {
		t.Errorf("the spec documents body_array's 400 as %s, want %s",
			mustJSON(responses["400"]["body_array"]), aliasBodyArrayRefused)
	}

	body := string(mustJSON(requests["body_array"]))
	if body != "[]" {
		t.Fatalf("body_array is sent as %s, want []", body)
	}
	rr, reached := createAliasNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("%s: status %d, want 400; body %s", body, rr.Code, rr.Body)
	}
	got := decodeShapeBody(t, rr)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered %s, want %s", body, rr.Body, aliasBodyArrayRefused)
	}

	// Controls: the answer is not source_missing's documented 422, and an
	// object, even an empty one, is read and refused on its fields with a
	// 422 instead: the 400 is the array's, not any refusal's.
	if reflect.DeepEqual(got, responses["422"]["source_missing"]) {
		t.Error("body_array is answered with source_missing's 422")
	}
	for _, object := range []string{`{}`, string(mustJSON(requests["source_missing"]))} {
		rr, reached := createAliasNoDB(t, object)
		if reached || rr.Code != http.StatusUnprocessableEntity || reflect.DeepEqual(decodeShapeBody(t, rr), want) {
			t.Errorf("%s answered %d %s, want a 422, not body_array's 400", object, rr.Code, rr.Body)
		}
	}
}

// The real handler, with no database, answers source_invalid with exactly the
// 422 the spec documents for it: after the required fields and before the
// source's domain is looked up or anything is stored.
func TestAliasCreate_SourceInvalidRefused(t *testing.T) {
	requests, responses := postExamples(t, aliasesPath, "createAlias")
	want := parseShapeJSON(t, aliasSourceInvalidRefused)
	if !reflect.DeepEqual(responses["422"]["source_invalid"], want) {
		t.Errorf("the spec documents source_invalid's 422 as %s, want %s",
			mustJSON(responses["422"]["source_invalid"]), aliasSourceInvalidRefused)
	}

	body := string(mustJSON(requests["source_invalid"]))
	rr, reached := createAliasNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
	}
	got := decodeShapeBody(t, rr)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered %s, want %s", body, rr.Body, aliasSourceInvalidRefused)
	}

	// Controls: the answer is not source_missing's 422 (the field is named
	// for being malformed, not for being absent), and the same request with a
	// well-formed source is not refused at all but goes on to the lookup.
	if reflect.DeepEqual(got, responses["422"]["source_missing"]) {
		t.Error("source_invalid is answered with source_missing's 422")
	}
	valid := map[string]interface{}{"source_address": "sales@restmail.test", "destination_address": "admin@restmail.test"}
	if rr, lookedUp := postAliasSource(t, mustJSON(valid)); !lookedUp {
		t.Errorf("%s was answered %d %s before the domain lookup", mustJSON(valid), rr.Code, rr.Body)
	}
}

// Each way the body_array and source_invalid pairs can be wrong is caught, and
// the existing pairs with them: the request or the 400 missing or misnamed, the 400 documented
// under another status, with another code, message or body, a request that is
// not the empty array, created changed and source_missing dropped. The
// mutations are made on copies, and the spec's examples are the same
// afterwards.
func TestAliasCreate_ExamplePairMutations(t *testing.T) {
	requests, responses := postExamples(t, aliasesPath, "createAlias")
	if problems := aliasCreatePairProblems(t, requests, responses); len(problems) != 0 {
		t.Fatalf("the spec's pairs are wrong before any mutation: %v", problems)
	}
	before := mustJSON([]interface{}{requests, responses})

	refused := func(res map[string]map[string]interface{}) map[string]interface{} {
		return res["400"]["body_array"].(map[string]interface{})["error"].(map[string]interface{})
	}
	invalid422 := func(res map[string]map[string]interface{}) map[string]interface{} {
		return res["422"]["source_invalid"].(map[string]interface{})["error"].(map[string]interface{})
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
		"400 code": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			refused(res)["code"] = "validation_failed"
		},
		"400 message": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			refused(res)["message"] = "Request body failed validation"
		},
		"400 body": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["400"]["body_array"] = res["422"]["source_missing"]
		},
		"request an object": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["body_array"] = map[string]interface{}{}
		},
		"request not empty": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["body_array"] = []interface{}{req["created"]}
		},
		"created changed": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["created"] = map[string]interface{}{"source_address": "sales@restmail.test"}
		},
		"source_missing dropped": func(req map[string]interface{}, res map[string]map[string]interface{}) {
			delete(req, "source_missing")
			delete(res, "422")
		},
		"source_invalid request missing": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			delete(req, "source_invalid")
		},
		"source_invalid request misnamed": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["invalid_source"] = req["source_invalid"]
			delete(req, "source_invalid")
		},
		"source_invalid request well-formed": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["source_invalid"] = req["created"]
		},
		"source_invalid request destination dropped": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			delete(req["source_invalid"].(map[string]interface{}), "destination_address")
		},
		"source_invalid 422 missing": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			delete(res["422"], "source_invalid")
		},
		"source_invalid 422 misnamed": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["422"]["invalid_source"] = res["422"]["source_invalid"]
			delete(res["422"], "source_invalid")
		},
		"source_invalid 422 under 400": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["400"]["source_invalid"] = res["422"]["source_invalid"]
			delete(res["422"], "source_invalid")
		},
		"source_invalid 422 code": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			invalid422(res)["code"] = "bad_request"
		},
		"source_invalid 422 message": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			invalid422(res)["message"] = "Invalid request body"
		},
		"source_invalid 422 field message": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			invalid422(res)["details"].(map[string]interface{})["fields"].(map[string]interface{})["source_address"] = "required"
		},
		"source_invalid 422 wrong field": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			fields := invalid422(res)["details"].(map[string]interface{})["fields"].(map[string]interface{})
			fields["destination_address"] = fields["source_address"]
			delete(fields, "source_address")
		},
		"source_invalid 422 body": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["422"]["source_invalid"] = res["422"]["source_missing"]
		},
		"source_invalid pair dropped": func(req map[string]interface{}, res map[string]map[string]interface{}) {
			delete(req, "source_invalid")
			delete(res["422"], "source_invalid")
		},
		"created pair dropped": func(req map[string]interface{}, res map[string]map[string]interface{}) {
			delete(req, "created")
			delete(res, "201")
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
			if problems := aliasCreatePairProblems(t, req, res); len(problems) == 0 {
				t.Errorf("the mutation %q is not caught", name)
			}
		})
	}

	requests, responses = postExamples(t, aliasesPath, "createAlias")
	if after := mustJSON([]interface{}{requests, responses}); !bytes.Equal(after, before) {
		t.Errorf("the spec's examples changed:\n  before %s\n  after  %s", before, after)
	}
}
