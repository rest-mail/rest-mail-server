package api

import (
	"encoding/json"
	"testing"
)

func TestQueueIsDescribedFromItsTypes(t *testing.T) {
	paths, schemas := describedAPI(t)
	for _, want := range []struct {
		path, method, id, capability string
		statuses                     []string
	}{
		{"", "get", "listQueue", "queue:read", []string{"200", "401", "403", "413", "422", "429", "500", "503"}},
		{"/stats", "get", "getQueueStats", "queue:read", []string{"200", "401", "403", "413", "429", "500", "503"}},
		{"/bulk-retry", "post", "bulkRetryQueue", "queue:manage", []string{"200", "400", "401", "403", "413", "422", "429", "500", "503"}},
		{"/bulk-bounce", "post", "bulkBounceQueue", "queue:manage", []string{"200", "400", "401", "403", "413", "422", "429", "500", "503"}},
		{"/bulk-delete", "delete", "bulkDeleteQueue", "queue:manage", []string{"200", "400", "401", "403", "413", "422", "429", "500", "503"}},
		{"/{id}", "get", "getQueueEntry", "queue:read", []string{"200", "401", "403", "404", "413", "422", "429", "500", "503"}},
		{"/{id}/retry", "post", "retryQueueEntry", "queue:manage", []string{"200", "400", "401", "403", "404", "413", "422", "429", "500", "503"}},
		{"/{id}/bounce", "post", "bounceQueueEntry", "queue:manage", []string{"200", "401", "403", "404", "413", "422", "429", "500", "503"}},
		{"/{id}", "delete", "deleteQueueEntry", "queue:manage", []string{"204", "401", "403", "404", "413", "422", "429", "500", "503"}},
	} {
		t.Run(want.id, func(t *testing.T) {
			op := paths["/api/v1/admin/queue"+want.path][want.method]
			if op.OperationID != want.id {
				t.Fatalf("operation %q, want %q", op.OperationID, want.id)
			}
			security, _ := json.Marshal(op.Security)
			if string(security) != `[{"CookieAuth":["`+want.capability+`"]},{"BearerAuth":["`+want.capability+`"]},{"APIKeyAuth":["`+want.capability+`"]}]` {
				t.Errorf("security: %s", security)
			}
			for _, status := range want.statuses {
				if _, ok := op.Responses[status]; !ok {
					t.Errorf("missing response %s", status)
				}
			}
			if len(op.Responses) != len(want.statuses) {
				t.Errorf("responses: %v", op.Responses)
			}
		})
	}
	body := paths["/api/v1/admin/queue/bulk-retry"]["post"].RequestBody.Content["application/json"].Schema
	request := schemas[refName(t, body)]
	ids := request["properties"].(map[string]interface{})["ids"].(map[string]interface{})
	if ids["maxItems"] != float64(1000) || request["additionalProperties"] != false {
		t.Errorf("bulk request: %v", request)
	}
	entry := schemas["QueueEntry"]
	if entry == nil {
		t.Fatal("QueueEntry must be the API's own response type")
	}
	if _, leaked := entry["properties"].(map[string]interface{})["raw_message"]; leaked {
		t.Error("raw message leaked into API")
	}
}
