package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A response-only pair lets Vertrag synthesize the optional send body. That
// synthesis fills list_unsubscribe with empty targets, which the send correctly
// refuses. Publish the empty object the successful draft send actually uses.
func TestSentDraftPublishesItsEmptyRequestBody(t *testing.T) {
	doc, warnings := describedRouters(t).Description("draft contract request")
	if len(warnings) != 0 {
		t.Fatalf("description warnings: %v", warnings)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var description struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]struct {
					Examples map[string]struct {
						Value json.RawMessage `json:"value"`
					} `json:"examples"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(encoded, &description); err != nil {
		t.Fatal(err)
	}
	sent := description.Paths["/api/v1/messages/draft/{id}/send"]["post"].
		RequestBody.Content["application/json"].Examples["sent"]
	var body map[string]any
	if err := json.Unmarshal(sent.Value, &body); err != nil {
		t.Fatalf("the successful draft send needs a named request body: %s (%v)", sent.Value, err)
	}
	if !reflect.DeepEqual(body, map[string]any{}) {
		t.Fatalf("sent draft body = %s; want {} with no unsubscribe targets", sent.Value)
	}
}
