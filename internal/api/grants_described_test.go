package api

import (
	"encoding/json"
	"testing"
)

func TestGrantsDescribeTheirAuthorization(t *testing.T) {
	paths, _ := describedAPI(t)
	for _, tc := range []struct {
		path, method, id, capability string
		apiKey                       bool
	}{
		{"/api/v1/admin/roles", "get", "listRoles", "users:read", true},
		{"/api/v1/admin/capabilities", "get", "listCapabilities", "users:read", true},
		{"/api/v1/admin/grants", "get", "listGrants", "users:read", true},
		{"/api/v1/admin/grants", "post", "createGrant", "users:write", false},
		{"/api/v1/admin/grants/{id}", "delete", "deleteGrant", "users:write", false},
		{"/api/v1/admin/grants/mine", "get", "listMyGrants", "", false},
		{"/api/v1/admin/grants/{id}/activate", "post", "activateGrant", "", false},
		{"/api/v1/admin/grants/{id}/deactivate", "post", "deactivateGrant", "", false},
		{"/api/v1/admin/grants/{id}/make-eligible", "post", "makeGrantEligible", "", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			op := paths[tc.path][tc.method]
			if op.OperationID != tc.id {
				t.Fatalf("operation = %s", op.OperationID)
			}
			roles := []string{}
			if tc.capability != "" {
				roles = append(roles, tc.capability)
			}
			want := []map[string][]string{{"CookieAuth": roles}, {"BearerAuth": roles}}
			if tc.apiKey {
				want = append(want, map[string][]string{"APIKeyAuth": roles})
			}
			gotJSON, _ := json.Marshal(op.Security)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("security = %s, want %s", gotJSON, wantJSON)
			}
			for _, status := range []string{"401", "403", "413", "500", "503"} {
				if _, ok := op.Responses[status]; !ok {
					t.Errorf("missing middleware response %s", status)
				}
			}
			_, throttled := op.Responses["429"]
			if throttled != (tc.id == "activateGrant" || tc.id == "makeGrantEligible") {
				t.Errorf("unexpected throttle description: %v", throttled)
			}
		})
	}
}

func TestGrantListBlankAndNonPositivePagination(t *testing.T) {
	router, jwt := newTestRouter(t)
	token := adminToken(t, jwt, []string{"users:read"})
	for _, query := range []string{"?holder_id=&limit=&offset=", "?limit=0&offset=-1", "?limit=-1&offset=0"} {
		rr := doRequest(router, "GET", "/api/v1/admin/grants"+query, token, "")
		if rr.Code != 500 || grantErrorCode(rr) != "internal_error" {
			t.Fatalf("%s should reach storage: %d %s", query, rr.Code, rr.Body.String())
		}
	}
}
