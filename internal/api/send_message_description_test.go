package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// The served description says what the send handler requires (#1443): a coverage probe built
// from it reaches a real send. The bytes are read from the route, as a contract run reads them.
func TestSendMessageDescribesWhatTheHandlerRequires(t *testing.T) {
	recorder := httptest.NewRecorder()
	describedRouters(t).Public.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/docs/openapi.json", nil))
	var served struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Ref        string                     `json:"$ref"`
						Required   []string                   `json:"required"`
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}

	body := served.Paths["/api/v1/messages/send"]["post"].RequestBody.Content["application/json"].Schema
	// Vertrag's fuzz.pin reads a body's properties without following a $ref at its root, and
	// a pin that holds nothing here leaves the generator's 2020 date in every probe.
	if body.Ref != "" {
		t.Fatalf("the send body is %s, want it inline", body.Ref)
	}
	if !slices.Contains(body.Required, "from") || !slices.Contains(body.Required, "to") || len(body.Required) != 2 {
		t.Errorf("required is %v, want from and to", body.Required)
	}
	for property, want := range map[string]string{
		"from": `{"format":"idn-email","minLength":1,"type":"string"}`,
		"to":   `{"items":{"format":"idn-email","type":"string"},"minItems":1,"type":"array"}`,
		"cc":   `{"items":{"format":"idn-email","type":"string"},"type":"array"}`,
		"bcc":  `{"items":{"format":"idn-email","type":"string"},"type":"array"}`,
	} {
		if got := string(body.Properties[property]); got != want {
			t.Errorf("%s is %s, want %s", property, got, want)
		}
	}

	// The description is narrower than the handler on purpose, and says so; it binds nothing.
	if err := rmail.ValidateAddress("Someone <someone@example.test>"); err != nil {
		t.Errorf("a display name is no longer accepted, so the description's words are wrong: %v", err)
	}

	// The coverage run's pin of scheduled_at takes probes away from whatever declares it.
	for path, methods := range served.Paths {
		for method, operation := range methods {
			for media, content := range operation.RequestBody.Content {
				if _, declared := content.Schema.Properties["scheduled_at"]; declared && operation.OperationID != "sendMessage" {
					t.Errorf("%s %s (%s, %s) declares scheduled_at, which tests/contract/probes.py pins", method, path, operation.OperationID, media)
				}
			}
		}
	}
}

// What the coverage run's pin takes away is proved by named examples, sent as the examples
// phase sends them, through the production router.
func TestSendMessage_ScheduledAtRefusalExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, name := range []string{"scheduled_at_malformed", "scheduled_at_wrong_type"} {
		t.Run(name, func(t *testing.T) {
			pair := pairs["sendMessage"][name]
			routers, jwt, connection := newGapPairRouter(t, name)
			session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			request := requirementRequest(routes["sendMessage"], pair)
			request.Header.Set("Authorization", "Bearer "+session.AccessToken)
			recorder := httptest.NewRecorder()
			routers.Public.ServeHTTP(recorder, request)
			assertRequirementResponse(t, pair, recorder)
			if connection.reads != 0 || connection.mutations != 0 {
				t.Fatalf("refused after reading: reads=%d writes=%v", connection.reads, connection.writes)
			}
		})
	}
}
