package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

func TestComposeAttachmentRefusalContract(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	document := generatedDocument(t)
	paths := document["paths"].(map[string]interface{})
	oracle := oracleDocument(t)
	oraclePaths := oracle["paths"].(map[string]interface{})
	for _, route := range []struct{ operation, method, path string }{
		{"sendMessage", http.MethodPost, "/api/v1/messages/send"},
		{"saveDraft", http.MethodPost, "/api/v1/messages/draft"},
		{"updateDraft", http.MethodPut, "/api/v1/messages/draft/{id}"},
	} {
		t.Run(route.operation, func(t *testing.T) {
			pair, ok := pairs[route.operation]["attachments_not_supported"]
			if !ok || !pair.Request.Invalid || pair.Response.Status != http.StatusUnprocessableEntity {
				t.Fatal("missing named 422 attachments_not_supported invalid request/response pair")
			}
			operation := paths[route.path].(map[string]interface{})[strings.ToLower(route.method)].(map[string]interface{})
			if !strings.Contains(operation["description"].(string), "attachments cannot be sent through this route yet") {
				t.Fatal("generated description omits the attachment refusal")
			}
			oracleOperation := oraclePaths[route.path].(map[string]interface{})[strings.ToLower(route.method)].(map[string]interface{})
			for _, source := range []struct {
				document, operation map[string]interface{}
			}{{document, operation}, {oracle, oracleOperation}} {
				body := source.operation["requestBody"].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})
				schema := resolved(source.document, body["schema"]).(map[string]interface{})
				if schema["additionalProperties"] != false || schema["properties"].(map[string]interface{})["attachments"] != nil {
					t.Fatal("attachment inputs must remain outside the closed compose schema")
				}
			}
			fixture := newCredentialFixture(t)
			response := fixture.do(route.method, messageCollectionReadURI(route.path, pair), fixture.session, string(pair.Request.Body))
			assertRequirementResponse(t, pair, response)
			examplepairs.Proven(t, route.operation, "attachments_not_supported")
		})
	}
}
