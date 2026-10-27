package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

type describedOperation struct {
	OperationID string                `json:"operationId"`
	Security    []map[string][]string `json:"security"`
	Responses   map[string]struct {
		Content map[string]struct {
			Schema map[string]interface{} `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
	RequestBody *struct {
		Content map[string]struct {
			Schema map[string]interface{} `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

// describedRouters builds the routers the way the server does, on a database that fails
// every query, for what they describe.
func describedRouters(t *testing.T) *Routers {
	t.Helper()
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{CORSAllowedOrigins: []string{"http://localhost:3000"}, Environment: "test"}
	routers := NewRouters(newFailingGormDB(t), jwtSvc, cfg, nil)
	t.Cleanup(routers.Close)
	return routers
}

func describedAPI(t *testing.T) (map[string]map[string]describedOperation, map[string]map[string]interface{}) {
	t.Helper()
	doc, warnings := describedRouters(t).Description("test")
	if len(warnings) > 0 {
		t.Fatalf("the registered routes describe with warnings: %v", warnings)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Paths      map[string]map[string]describedOperation `json:"paths"`
		Components struct {
			Schemas map[string]map[string]interface{} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed.Paths, parsed.Components.Schemas
}

// The domain routes are described from the types they execute (#645): each needs its
// capability on every way an admin can authenticate, answers what its middleware can (the
// group's body cap refuses an over-long declared body on any method, so 413 is everywhere),
// and states the rules binding enforces on its body.
func TestDomainsAreDescribedFromTheirTypes(t *testing.T) {
	paths, schemas := describedAPI(t)
	want := map[string]struct {
		path, method, capability string
		statuses                 []string
	}{
		"listDomains":    {"/api/v1/admin/domains", "get", "domains:read", []string{"200", "401", "403", "413", "422", "429", "500", "503"}},
		"createDomain":   {"/api/v1/admin/domains", "post", "domains:write", []string{"201", "400", "401", "403", "409", "413", "422", "429", "500", "503"}},
		"getDomain":      {"/api/v1/admin/domains/{id}", "get", "domains:read", []string{"200", "401", "403", "404", "413", "422", "429", "500", "503"}},
		"updateDomain":   {"/api/v1/admin/domains/{id}", "patch", "domains:write", []string{"200", "400", "401", "403", "404", "409", "413", "422", "429", "500", "503"}},
		"deleteDomain":   {"/api/v1/admin/domains/{id}", "delete", "domains:delete", []string{"204", "401", "403", "404", "409", "413", "422", "429", "500", "503"}},
		"domainDNSCheck": {"/api/v1/admin/domains/{id}/dns", "get", "domains:read", []string{"200", "401", "403", "404", "413", "422", "429", "500", "503"}},
	}
	for id, w := range want {
		op, ok := paths[w.path][w.method]
		if !ok || op.OperationID != id {
			t.Errorf("%s is not described at %s %s", id, w.method, w.path)
			continue
		}
		security, _ := json.Marshal(op.Security)
		if string(security) != `[{"CookieAuth":["`+w.capability+`"]},{"BearerAuth":["`+w.capability+`"]},{"APIKeyAuth":["`+w.capability+`"]}]` {
			t.Errorf("%s security: %s", id, security)
		}
		for _, status := range w.statuses {
			if _, ok := op.Responses[status]; !ok {
				t.Errorf("%s does not describe %s", id, status)
			}
		}
		if len(op.Responses) != len(w.statuses) {
			t.Errorf("%s describes %d responses, want %v", id, len(op.Responses), w.statuses)
		}
	}

	create := paths["/api/v1/admin/domains"]["post"].RequestBody.Content["application/json"].Schema
	body := schemas[refName(t, create)]
	props := body["properties"].(map[string]interface{})
	name := props["name"].(map[string]interface{})
	if name["minLength"] != float64(1) || body["additionalProperties"] != false {
		t.Errorf("createDomain's body: %v", body)
	}
	policy, _ := json.Marshal(props["sending_policy"])
	if string(policy) != `{"enum":["","transactional","bulk"],"type":"string"}` {
		t.Errorf("sending_policy: %s", policy)
	}
	domain := schemas["Domain"]
	if domain == nil || domain["additionalProperties"] != false {
		t.Errorf("the Domain the API answers with is its own type: %v", domain)
	}
	if _, leaked := domain["properties"].(map[string]interface{})["dkim_private_key"]; leaked {
		t.Error("the private key is not part of the API's Domain")
	}
}

func refName(t *testing.T, schema map[string]interface{}) string {
	t.Helper()
	ref, _ := schema["$ref"].(string)
	const prefix = "#/components/schemas/"
	if len(ref) <= len(prefix) {
		t.Fatalf("not a reference: %v", schema)
	}
	return ref[len(prefix):]
}

func TestDomainMailPolicyIsDescribed(t *testing.T) {
	paths, schemas := describedAPI(t)
	for _, tc := range []struct {
		path, method, id, capability string
		statuses                     []string
	}{
		{"/api/v1/admin/domains/{id}/allowlist", "get", "listAllowlist", "domains:read", []string{"200"}},
		{"/api/v1/admin/domains/{id}/allowlist", "post", "addToAllowlist", "domains:write", []string{"201", "400", "404", "409"}},
		{"/api/v1/admin/domains/{id}/allowlist/{eid}", "delete", "removeFromAllowlist", "domains:write", []string{"204", "404"}},
		{"/api/v1/admin/domains/{id}/blocklist", "get", "listBlocklist", "domains:read", []string{"200"}},
		{"/api/v1/admin/domains/{id}/blocklist", "post", "addToBlocklist", "domains:write", []string{"201", "400", "404", "409"}},
		{"/api/v1/admin/domains/{id}/blocklist/{eid}", "delete", "removeFromBlocklist", "domains:write", []string{"204", "404"}},
		{"/api/v1/admin/domains/{id}/suppressions", "get", "listSuppressions", "domains:read", []string{"200"}},
		{"/api/v1/admin/domains/{id}/suppressions/{sid}", "delete", "clearSuppression", "domains:write", []string{"204", "404"}},
		{"/api/v1/admin/domains/{id}/mta-sts", "get", "getMtaStsPolicy", "domains:read", []string{"200", "404"}},
		{"/api/v1/admin/domains/{id}/mta-sts", "put", "setMtaStsPolicy", "domains:write", []string{"200", "201", "400", "404"}},
		{"/api/v1/admin/domains/{id}/mta-sts", "delete", "deleteMtaStsPolicy", "domains:write", []string{"204", "404"}},
		{"/api/v1/admin/domains/{id}/relay", "get", "getDomainRelay", "domains:read", []string{"200", "404"}},
		{"/api/v1/admin/domains/{id}/relay", "put", "setDomainRelay", "domains:write", []string{"200", "400", "404", "409"}},
		{"/api/v1/admin/tls-reports", "get", "listTlsReports", "domains:read", []string{"200"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			op := paths[tc.path][tc.method]
			if op.OperationID != tc.id {
				t.Fatalf("operationId = %q", op.OperationID)
			}
			security, _ := json.Marshal(op.Security)
			want := `[{"CookieAuth":["` + tc.capability + `"]},{"BearerAuth":["` + tc.capability + `"]},{"APIKeyAuth":["` + tc.capability + `"]}]`
			if string(security) != want {
				t.Errorf("security = %s, want %s", security, want)
			}
			statuses := append([]string{"401", "403", "413", "422", "429", "500", "503"}, tc.statuses...)
			for _, status := range statuses {
				if _, ok := op.Responses[status]; !ok {
					t.Errorf("missing response %s", status)
				}
			}
			if len(op.Responses) != len(statuses) {
				t.Errorf("describes %d responses, want %v", len(op.Responses), statuses)
			}
		})
	}
	// Check the generated response types, in addition to the runtime credential round trip.
	doc := generatedDocument(t)
	for _, method := range []string{"get", "put"} {
		op := paths["/api/v1/admin/domains/{id}/relay"][method]
		shape := normalSchema(doc, op.Responses["200"].Content["application/json"].Schema, map[string]bool{}).(map[string]interface{})
		data := shape["properties"].(map[string]interface{})["data"].(map[string]interface{})
		props := data["properties"].(map[string]interface{})
		for _, key := range []string{"password", "password_enc"} {
			if _, leaked := props[key]; leaked {
				t.Errorf("%s relay response describes %s", method, key)
			}
		}
		if props["password_set"] == nil {
			t.Error("relay response must describe password_set")
		}
	}
	if schemas["SenderRule"] == nil || schemas["Suppression"] == nil || schemas["MTASTSPolicy"] == nil || schemas["TLSReport"] == nil {
		t.Error("API-owned policy response types are missing")
	}
}
