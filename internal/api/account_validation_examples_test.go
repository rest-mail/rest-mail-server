package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// The account and credential pairs that the registration refuses before the handler
// runs are sent through the production router with a signed-in session (#635). A pair
// that names no account is sent for the caller's own, as the live run does. A pair that
// requires a malformed body is sent its raw bytes, not the JSON string the file holds.
func TestAccountValidationExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"createAccount", "address_missing"}, {"createAccount", "body_array"}, {"createAccount", "malformed_json"},
		{"createAppPassword", "name_control"}, {"createAppPassword", "body_array"}, {"createAppPassword", "malformed_json"},
		{"createAPIKey", "name_missing"}, {"createAPIKey", "body_array"}, {"createAPIKey", "malformed_json"},
		{"linkAccount", "address_blank"}, {"linkAccount", "body_array"}, {"linkAccount", "malformed_json"},
		{"unlinkAccount", "invalid_id"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			pair, ok := pairs[tc.op][tc.name]
			// name_control fits the schema: only the handler can refuse a control character.
			if !ok || (!pair.Request.Invalid && tc.name != "name_control") {
				t.Fatalf("%s/%s is not an invalid request pair", tc.op, tc.name)
			}
			if pair.Requires["body"] == "malformed" {
				pair.Request.Body = []byte("{")
			}
			c := &mailRequirementConnection{authRequirementConnection: &authRequirementConnection{t: t, op: tc.op, name: tc.name, password: requirementHash(t, "current-password")}}
			jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
			r := NewRouters(requirementDB(t, c), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey}, nil)
			t.Cleanup(r.Close)
			session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
			if err != nil {
				t.Fatal(err)
			}
			route := routes[tc.op]
			if _, named := pair.Request.Path["id"]; !named {
				route.path = strings.ReplaceAll(route.path, "{id}", "1")
			}
			req := requirementRequest(route, pair)
			req.Header.Set("Authorization", "Bearer "+session.AccessToken)
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			assertRequirementResponse(t, pair, rr)
			if len(c.messages) != 0 || len(c.queue) != 0 || c.removed {
				t.Fatal("a refused request changed state")
			}
			for _, write := range c.writes {
				if strings.HasPrefix(write, `INSERT INTO "credentials"`) {
					t.Fatal("a refused request stored a credential")
				}
			}
		})
	}
}

// Strict mode is on for the account area (#635), so the generator reports no warning for
// any of its operations in any configuration the gap report builds.
func TestAccountAreaReportsNoWarnings(t *testing.T) {
	area := map[string]bool{
		"createAPIKey": true, "createAccount": true, "createAppPassword": true, "getAccount": true,
		"linkAccount": true, "listAPIKeys": true, "listAccounts": true, "listCredentials": true,
		"revokeAPIKey": true, "revokeCredential": true, "unlinkAccount": true,
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if area[warning.OperationID] {
				t.Errorf("account area warning: %v", warning)
			}
		}
	}
}
