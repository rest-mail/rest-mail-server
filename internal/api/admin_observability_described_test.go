package api

import (
	"encoding/json"
	"testing"
)

func TestAdminObservabilityIsDescribedFromItsTypes(t *testing.T) {
	paths, schemas := describedAPI(t)
	cases := []struct {
		id, path, method, capability string
		statuses                     []string
	}{
		{"listBans", "/api/v1/admin/bans", "get", "bans:read", []string{"200", "401", "403", "413", "422", "500", "503"}},
		{"createBan", "/api/v1/admin/bans", "post", "bans:write", []string{"200", "201", "400", "401", "403", "413", "422", "500", "503"}},
		{"deleteBan", "/api/v1/admin/bans/{id}", "delete", "bans:delete", []string{"204", "401", "403", "413", "422", "500", "503"}},
		{"unbanIP", "/api/v1/admin/bans/ip/{ip}", "delete", "bans:delete", []string{"204", "401", "403", "404", "413", "500", "503"}},
		{"getDeliveryLogs", "/api/v1/admin/logs/delivery", "get", "messages:read", []string{"200", "401", "403", "413", "422", "500", "503"}},
		{"listAuditEntries", "/api/v1/admin/audit", "get", "audit:read", []string{"200", "401", "403", "413", "422", "500", "503"}},
		{"verifyAuditLog", "/api/v1/admin/audit/verify", "get", "audit:read", []string{"200", "401", "403", "413", "422", "500", "503"}},
		{"getDashboardStats", "/api/v1/admin/stats", "get", "domains:read", []string{"200", "401", "403", "413", "500", "503"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			op := paths[tc.path][tc.method]
			if op.OperationID != tc.id {
				t.Fatalf("operation: %q", op.OperationID)
			}
			security, _ := json.Marshal(op.Security)
			if string(security) != `[{"CookieAuth":["`+tc.capability+`"]},{"BearerAuth":["`+tc.capability+`"]},{"APIKeyAuth":["`+tc.capability+`"]}]` {
				t.Errorf("security: %s", security)
			}
			for _, status := range tc.statuses {
				if _, ok := op.Responses[status]; !ok {
					t.Errorf("missing status %s", status)
				}
			}
			if len(op.Responses) != len(tc.statuses) {
				t.Errorf("unexpected responses: %v", op.Responses)
			}
		})
	}
	for _, name := range []string{"Ban", "DeliveryLogEntry", "AuditEntry", "DashboardStats"} {
		if schemas[name] == nil || schemas[name]["additionalProperties"] != false {
			t.Errorf("%s is not a closed API type: %v", name, schemas[name])
		}
	}
	props := schemas["DeliveryLogEntry"]["properties"].(map[string]interface{})
	if _, leaked := props["raw_message"]; leaked {
		t.Error("delivery log exposes message content")
	}
	if props["body_type"] == nil || props["failure_category"] == nil {
		t.Error("delivery log lost existing fields")
	}
}
