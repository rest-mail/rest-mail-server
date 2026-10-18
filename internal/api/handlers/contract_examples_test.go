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

// The contract run (vertrag) pairs a request example with the response example
// of the same name, so a create's named examples say which body produces which
// answer (#386). Each test here holds an operation's refusals to its handler:
// each request example named on the 422 is sent to the real handler and gets
// exactly the documented body. The handler has no database, so the refusal is
// proved to come before anything is looked up or stored.
func TestDomainCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/domains", "createDomain", NewDomainHandler(nil, nil).Create)
}

func TestAliasCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/aliases", "createAlias", NewAliasHandler(nil).Create)
}

func documentedRefusals(t *testing.T, path, operationID string, create http.HandlerFunc) {
	t.Helper()
	requests, responses := postExamples(t, path, operationID)
	if len(responses["422"]) == 0 {
		t.Fatalf("%s's 422 names no example: the contract run cannot provoke it", operationID)
	}
	// A response naming no example pairs with every request, so an unnamed 201
	// would be expected for the refused requests too.
	if len(responses["201"]) == 0 {
		t.Errorf("%s's 201 names no example: it pairs with the refused requests too", operationID)
	}

	for name := range requests {
		var statuses []string
		for status, examples := range responses {
			if _, ok := examples[name]; ok {
				statuses = append(statuses, status)
			}
		}
		if len(statuses) != 1 {
			t.Errorf("request example %q is named on responses %v, want exactly one", name, statuses)
		}
	}

	for name, want := range responses["422"] {
		t.Run(name, func(t *testing.T) {
			request, ok := requests[name]
			if !ok {
				t.Fatalf("the 422 example %q has no request example of the same name", name)
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			rr := httptest.NewRecorder()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s reached the database before it was refused: %v", body, r)
					}
				}()
				create(rr, req)
			}()

			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
			}
			var got interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", rr.Body, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s answered\n  %s\nthe spec documents\n  %s", body, rr.Body, mustJSON(want))
			}
		})
	}
}

type namedExamples struct {
	Examples map[string]struct {
		Value interface{} `yaml:"value"`
	} `yaml:"examples"`
}

// postExamples reads the named application/json examples of the POST on path
// from the spec the API serves: the request's by name, and each response's by
// status and name, as JSON values.
func postExamples(t *testing.T, path, operationID string) (map[string]interface{}, map[string]map[string]interface{}) {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]struct {
			Post struct {
				OperationID string `yaml:"operationId"`
				RequestBody struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"requestBody"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths[path].Post
	if op.OperationID != operationID {
		t.Fatalf("POST %s is %q, want %s", path, op.OperationID, operationID)
	}

	requests := map[string]interface{}{}
	for name, example := range op.RequestBody.Content["application/json"].Examples {
		requests[name] = jsonValue(t, example.Value)
	}
	responses := map[string]map[string]interface{}{}
	for status, response := range op.Responses {
		for name, example := range response.Content["application/json"].Examples {
			if responses[status] == nil {
				responses[status] = map[string]interface{}{}
			}
			responses[status][name] = jsonValue(t, example.Value)
		}
	}
	return requests, responses
}

// jsonValue turns a value decoded from YAML into the one encoding/json would
// decode from the same document, so the two compare with reflect.DeepEqual.
func jsonValue(t *testing.T, v interface{}) interface{} {
	t.Helper()
	var out interface{}
	if err := json.Unmarshal(mustJSON(yamlToJSON(v)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func yamlToJSON(v interface{}) interface{} {
	switch v := v.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(v))
		for k, e := range v {
			m[fmt.Sprint(k)] = yamlToJSON(e)
		}
		return m
	case []interface{}:
		for i, e := range v {
			v[i] = yamlToJSON(e)
		}
		return v
	}
	return v
}
