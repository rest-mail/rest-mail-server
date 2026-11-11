package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var forwardEnvelopeCases = []struct {
	name, body string
	valid      bool
}{
	{"missing_all", `{}`, false},
	{"empty_note_only", `{"body_text":""}`, false},
	{"empty_sender_only", `{"from":""}`, false},
	{"on_behalf_true_only", `{"on_behalf":true}`, false},
	{"on_behalf_false_only", `{"on_behalf":false}`, false},
	{"empty_recipients_only", `{"to":[]}`, false},
	{"missing_sender", `{"to":["target@remote.test"]}`, false},
	{"missing_recipients", `{"from":"sender@example.test"}`, false},
	{"empty_sender", `{"from":"","to":["target@remote.test"]}`, false},
	{"empty_recipients", `{"from":"sender@example.test","to":[]}`, false},
	{"invalid_sender", `{"from":"x","to":["target@remote.test"]}`, false},
	{"invalid_recipient", `{"from":"sender@example.test","to":["x"]}`, false},
	{"minimal", `{"from":"sender@example.test","to":["target@remote.test"]}`, true},
	{"with_note", `{"from":"sender@example.test","to":["target@remote.test"],"body_text":"See below."}`, true},
	{"on_behalf_true", `{"from":"sender@example.test","to":["target@remote.test"],"on_behalf":true}`, true},
	{"on_behalf_false", `{"from":"sender@example.test","to":["target@remote.test"],"on_behalf":false}`, true},
}

func TestForwardRequestSchemaMatchesRequiredEnvelope(t *testing.T) {
	for index, generator := range gapGenerators(t) {
		t.Run(fmt.Sprintf("registration_%d", index), func(t *testing.T) {
			compiler := fuzzFindingCompiler()
			const resource = "https://rest-mail.test/forward-envelope.json"
			if err := compiler.AddResource(resource, contractDocument(t, generator)); err != nil {
				t.Fatal(err)
			}
			schema, err := compiler.Compile(resource + "#/paths/~1api~1v1~1messages~1{id}~1forward/post/requestBody/content/application~1json/schema")
			if err != nil {
				t.Fatal(err)
			}
			for _, scenario := range forwardEnvelopeCases {
				t.Run(scenario.name, func(t *testing.T) {
					var body any
					if err := json.Unmarshal([]byte(scenario.body), &body); err != nil {
						t.Fatal(err)
					}
					if valid := schema.Validate(body) == nil; valid != scenario.valid {
						t.Errorf("forward schema accepts %s: got %t, want %t", scenario.body, valid, scenario.valid)
					}
				})
			}
		})
	}
}

func TestForwardRequiredEnvelopePreservesSourceAuthorizationOrder(t *testing.T) {
	useRealGrants(t)
	for _, principal := range []struct {
		name, mode string
		status     int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"inaccessible_source", "denied", http.StatusNotFound},
		{"accessible_source", "", http.StatusUnprocessableEntity},
	} {
		t.Run(principal.name, func(t *testing.T) {
			fixture := newForwardFixture(t, principal.mode)
			for _, scenario := range forwardEnvelopeCases {
				if scenario.valid {
					continue
				}
				t.Run(scenario.name, func(t *testing.T) {
					request := httptest.NewRequest(http.MethodPost, "/api/v1/messages/7/forward", strings.NewReader(scenario.body))
					request.Header.Set("Content-Type", "application/json")
					if principal.name != "anonymous" {
						request.Header.Set("Authorization", "Bearer "+fixture.token)
					}
					response := httptest.NewRecorder()
					fixture.router.ServeHTTP(response, request)
					if response.Code != principal.status {
						t.Errorf("forward response %d, want %d: %s", response.Code, principal.status, response.Body.String())
					}
					if len(fixture.c.state.messages) != 0 || len(fixture.c.queue) != 0 {
						t.Fatal("refused forward created a sent message or recipient work")
					}
				})
			}
		})
	}
}
