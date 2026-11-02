package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestBulkQueueSelectionMatchesDescription(t *testing.T) {
	paths, schemas := describedAPI(t)
	scenarios := bulkQueueExampleScenarios(t)
	oracle := oracleDocument(t)["components"].(map[string]interface{})["schemas"].(map[string]interface{})["BulkQueueRequest"]
	oracleCompiler := jsonschema.NewCompiler()
	const oracleResource = "https://rest-mail.test/oracle-bulk-queue-request.json"
	if err := oracleCompiler.AddResource(oracleResource, oracle); err != nil {
		t.Fatal(err)
	}
	oracleSchema, err := oracleCompiler.Compile(oracleResource)
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range bulkQueueExamples {
		t.Run(operation, func(t *testing.T) {
			body := paths[route.path][strings.ToLower(route.method)].RequestBody.Content["application/json"].Schema
			compiler := jsonschema.NewCompiler()
			const resource = "https://rest-mail.test/bulk-queue-request.json"
			if err := compiler.AddResource(resource, schemas[refName(t, body)]); err != nil {
				t.Fatal(err)
			}
			schema, err := compiler.Compile(resource)
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, body string
				status     int
			}{
				{"null_filter", `{"filter":null}`, http.StatusUnprocessableEntity},
				{"empty_ids_null_filter", `{"ids":[],"filter":null}`, http.StatusUnprocessableEntity},
				{"ids_null_filter", `{"ids":[987654],"filter":null}`, http.StatusUnprocessableEntity},
				{"selection_missing", `{}`, http.StatusUnprocessableEntity},
				{"empty_ids", `{"ids":[]}`, http.StatusUnprocessableEntity},
				{"empty_filter", `{"filter":{}}`, http.StatusOK},
				{"empty_ids_empty_filter", `{"ids":[],"filter":{}}`, http.StatusOK},
				{"by_ids", `{"ids":[987654]}`, http.StatusOK},
				{"ids_take_precedence", `{"ids":[987654],"filter":{"domain":"no-match.test"}}`, http.StatusOK},
			} {
				t.Run(test.name, func(t *testing.T) {
					var value interface{}
					if err := json.Unmarshal([]byte(test.body), &value); err != nil {
						t.Fatal(err)
					}
					scenario := scenarios["by_ids"]
					before := len(scenario.store.snapshot())
					request := httptest.NewRequest(route.method, route.path, bytes.NewBufferString(test.body))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Authorization", "Bearer "+scenario.token)
					response := httptest.NewRecorder()
					scenario.handler.ServeHTTP(response, request)
					t.Logf("%s %s => %d %s", route.method, test.body, response.Code, response.Body.String())
					if response.Code != test.status {
						t.Errorf("status %d, want %d", response.Code, test.status)
					}
					valid := schema.Validate(value) == nil
					if valid != (test.status == http.StatusOK) {
						t.Errorf("generated JSON Schema accepts=%t, want %t", valid, test.status == http.StatusOK)
					}
					if oracleValid := oracleSchema.Validate(value) == nil; oracleValid != valid {
						t.Errorf("oracle JSON Schema accepts=%t, generated accepts=%t", oracleValid, valid)
					}
					if strings.Contains(test.name, "null_filter") {
						assertPublicExampleBody(t, json.RawMessage(`{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"filter":"must not be null"}}}}`), response.Body.Bytes())
					}
					mutations := scenario.store.snapshot()[before:]
					if test.status == http.StatusUnprocessableEntity && len(mutations) != 0 {
						t.Fatalf("refused selection reached SQL: %+v", mutations)
					}
					if test.status == http.StatusOK && len(mutations) != 1 {
						t.Fatalf("accepted selection made %d mutations, want 1", len(mutations))
					}
				})
			}
		})
	}
}
