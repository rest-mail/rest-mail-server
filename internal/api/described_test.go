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

func describedAPI(t *testing.T) (map[string]map[string]describedOperation, map[string]map[string]interface{}) {
	t.Helper()
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{CORSAllowedOrigins: []string{"http://localhost:3000"}, Environment: "test"}
	routers := NewRouters(newFailingGormDB(t), jwtSvc, cfg, nil)
	t.Cleanup(routers.Close)

	doc, warnings := routers.Description("test")
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
		"listDomains":    {"/api/v1/admin/domains", "get", "domains:read", []string{"200", "401", "403", "413", "422", "500", "503"}},
		"createDomain":   {"/api/v1/admin/domains", "post", "domains:write", []string{"201", "400", "401", "403", "409", "413", "422", "500", "503"}},
		"getDomain":      {"/api/v1/admin/domains/{id}", "get", "domains:read", []string{"200", "401", "403", "404", "413", "422", "500", "503"}},
		"updateDomain":   {"/api/v1/admin/domains/{id}", "patch", "domains:write", []string{"200", "400", "401", "403", "404", "409", "413", "422", "500", "503"}},
		"deleteDomain":   {"/api/v1/admin/domains/{id}", "delete", "domains:delete", []string{"204", "401", "403", "404", "409", "413", "422", "500", "503"}},
		"domainDNSCheck": {"/api/v1/admin/domains/{id}/dns", "get", "domains:read", []string{"200", "401", "403", "404", "413", "422", "500", "503"}},
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
