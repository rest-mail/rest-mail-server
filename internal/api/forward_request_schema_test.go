package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type forwardEnvelopeCase struct {
	name, body string
	valid      bool
	keywords   []string
}

var forwardEnvelopeCases = []forwardEnvelopeCase{
	{"missing_all", `{}`, false, []string{"required"}},
	{"empty_note_only", `{"body_text":""}`, false, []string{"required"}},
	{"empty_sender_only", `{"from":""}`, false, []string{"required", "format"}},
	{"on_behalf_true_only", `{"on_behalf":true}`, false, []string{"required"}},
	{"on_behalf_false_only", `{"on_behalf":false}`, false, []string{"required"}},
	{"empty_recipients_only", `{"to":[]}`, false, []string{"required", "minItems"}},
	{"missing_sender", `{"to":["target@remote.test"]}`, false, nil},
	{"missing_recipients", `{"from":"sender@example.test"}`, false, nil},
	{"empty_sender", `{"from":"","to":["target@remote.test"]}`, false, nil},
	{"empty_recipients", `{"from":"sender@example.test","to":[]}`, false, nil},
	{"invalid_sender", `{"from":"x","to":["target@remote.test"]}`, false, nil},
	{"invalid_recipient", `{"from":"sender@example.test","to":["x"]}`, false, nil},
	{"minimal", `{"from":"sender@example.test","to":["target@remote.test"]}`, true, nil},
	{"with_note", `{"from":"sender@example.test","to":["target@remote.test"],"body_text":"See below."}`, true, nil},
	{"on_behalf_true", `{"from":"sender@example.test","to":["target@remote.test"],"on_behalf":true}`, true, nil},
	{"on_behalf_false", `{"from":"sender@example.test","to":["target@remote.test"],"on_behalf":false}`, true, nil},
}

const forwardEnvelopeRefusal = `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"from":"required","to":"required"}}}}`

func publishedContractPairs(t *testing.T) oapifly.ExampleFile {
	t.Helper()
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	return pairs
}

func historicalForwardPair(t *testing.T, pairs oapifly.ExampleFile, scenario forwardEnvelopeCase) oapifly.ExamplePair {
	t.Helper()
	pair, ok := pairs["forwardMessage"][scenario.name]
	if !ok || pair.Response.Status != http.StatusUnprocessableEntity || !pair.Request.Invalid || len(pair.Requires) != 0 {
		t.Fatalf("forwardMessage/%s must be a published invalid 422 pair without fault or other prerequisites", scenario.name)
	}
	if target := messageCollectionReadURI("/api/v1/messages/{id}/forward", pair); target != "/api/v1/messages/7/forward" {
		t.Fatalf("forwardMessage/%s must target the accessible source, got %s", scenario.name, target)
	}
	assertPublicExampleBody(t, []byte(scenario.body), pair.Request.Body)
	assertPublicExampleBody(t, []byte(forwardEnvelopeRefusal), pair.Response.Body)
	return pair
}

func TestForwardRequestSchemaMatchesRequiredEnvelope(t *testing.T) {
	pairs := publishedContractPairs(t)
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
					raw := []byte(scenario.body)
					if len(scenario.keywords) > 0 {
						raw = historicalForwardPair(t, pairs, scenario).Request.Body
					}
					var body any
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Fatal(err)
					}
					validation := schema.Validate(body)
					if valid := validation == nil; valid != scenario.valid {
						t.Fatalf("forward schema accepts %s: got %t, want %t (expected keywords: %v)", scenario.body, valid, scenario.valid, scenario.keywords)
					}
					if len(scenario.keywords) > 0 {
						var found []string
						for _, unit := range validation.(*jsonschema.ValidationError).BasicOutput().Errors {
							found = append(found, unit.KeywordLocation[strings.LastIndex(unit.KeywordLocation, "/")+1:])
						}
						for _, keyword := range scenario.keywords {
							if !slices.Contains(found, keyword) {
								t.Errorf("forward schema missed keyword %s for %s: got %v", keyword, scenario.body, found)
							}
						}
					}
					if scenario.name == "empty_sender_only" {
						sender := schema.Properties["from"]
						if sender == nil || sender.MinLength == nil || *sender.MinLength != 1 {
							t.Error("forward schema must declare from.minLength=1 separately from its asserted format")
						}
					}
				})
			}
		})
	}
}

func TestForwardRequiredEnvelopePreservesSourceAuthorizationOrder(t *testing.T) {
	useRealGrants(t)
	pairs := publishedContractPairs(t)
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
					named := len(scenario.keywords) > 0
					var pair oapifly.ExamplePair
					if named {
						pair = historicalForwardPair(t, pairs, scenario)
						request = requirementRequest(requirementRoute{http.MethodPost, "/api/v1/messages/{id}/forward"}, pair)
					}
					if principal.name != "anonymous" {
						request.Header.Set("Authorization", "Bearer "+fixture.token)
					}
					response := httptest.NewRecorder()
					fixture.router.ServeHTTP(response, request)
					if response.Code != principal.status {
						t.Errorf("forward response %d, want %d: %s", response.Code, principal.status, response.Body.String())
					}
					if len(fixture.c.state.messages) != 0 || len(fixture.c.queue) != 0 || len(fixture.c.attachments) != 0 || fixture.c.state.quota != 0 {
						t.Fatal("refused forward changed Sent, queue, attachments or quota")
					}
					if named && principal.status == http.StatusUnprocessableEntity {
						assertRequirementResponse(t, pair, response)
						examplepairs.Proven(t, "forwardMessage", scenario.name)
					}
				})
			}
		})
	}
}

func TestForwardHistoricalExamplesCompileWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for whole-description compilation")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs := publishedContractPairs(t)
	for index, generator := range gapGenerators(t) {
		t.Run(fmt.Sprintf("registration_%d", index), func(t *testing.T) {
			transactions := compileFullContract(t, binary, generator)
			for _, scenario := range forwardEnvelopeCases {
				if len(scenario.keywords) == 0 {
					continue
				}
				t.Run(scenario.name, func(t *testing.T) {
					pair := historicalForwardPair(t, pairs, scenario)
					var matches int
					for _, transaction := range transactions {
						if transaction.Request.URI != "/api/v1/messages/7/forward" || transaction.Response.Status != "422" {
							continue
						}
						var body bytes.Buffer
						if json.Compact(&body, []byte(transaction.Request.Body)) != nil || body.String() != scenario.body {
							continue
						}
						assertPublicExampleBody(t, pair.Response.Body, []byte(transaction.Response.Body))
						matches++
					}
					if matches != 1 {
						t.Fatalf("compiled %d forward 422 exchanges for %s; want one", matches, scenario.body)
					}
				})
			}
		})
	}
}
