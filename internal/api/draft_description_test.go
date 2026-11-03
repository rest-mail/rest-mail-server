package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// servedDraftBody is the schema the served description gives a draft body's properties,
// reading the document as a contract run does: from the route, following the body's $ref.
func servedDraftBody(t *testing.T, path, method string) map[string]json.RawMessage {
	t.Helper()
	recorder := httptest.NewRecorder()
	describedRouters(t).Public.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/docs/openapi.json", nil))
	type schema struct {
		Ref                  string                     `json:"$ref"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	var served struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct {
					Schema schema `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	body := served.Paths[path][method].RequestBody.Content["application/json"].Schema
	const prefix = "#/components/schemas/"
	if len(body.Ref) > len(prefix) {
		body = served.Components.Schemas[body.Ref[len(prefix):]]
	}
	if len(body.Properties) == 0 {
		t.Fatalf("%s %s has no described body", method, path)
	}
	return body.Properties
}

// Each field of a draft body says what the handlers hold it to (#1163). The answers themselves
// are in internal/api/handlers/draft_fields_test.go, against the real handlers.
func TestDraftBodiesDescribeWhatTheHandlersAccept(t *testing.T) {
	const address = `{"items":{"format":"idn-email","type":"string"},"type":"array"}`
	for _, tc := range []struct {
		name, path, method, property, want string
	}{
		{"saveDraft/to", "/api/v1/messages/draft", "post", "to", address},
		{"saveDraft/cc", "/api/v1/messages/draft", "post", "cc", address},
		// An empty from is accepted, and means none: a format would call that body invalid.
		{"saveDraft/from", "/api/v1/messages/draft", "post", "from", `{"type":"string"}`},
		{"updateDraft/to", "/api/v1/messages/draft/{id}", "put", "to", address},
		{"updateDraft/cc", "/api/v1/messages/draft/{id}", "put", "cc", address},
		{"updateDraft/from", "/api/v1/messages/draft/{id}", "put", "from", `{"type":["string","null"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(servedDraftBody(t, tc.path, tc.method)[tc.property]); got != tc.want {
				t.Errorf("%s is %s, want %s", tc.property, got, tc.want)
			}
		})
	}
	// A draft has no blind copies: a body naming one is refused (bcc_not_allowed).
	for _, tc := range []struct{ path, method string }{{"/api/v1/messages/draft", "post"}, {"/api/v1/messages/draft/{id}", "put"}} {
		if got, declared := servedDraftBody(t, tc.path, tc.method)["bcc"]; declared {
			t.Errorf("%s %s describes bcc as %s, and the handler refuses it", tc.method, tc.path, got)
		}
	}
}
