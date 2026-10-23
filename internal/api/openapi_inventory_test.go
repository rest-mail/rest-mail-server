package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

type inventoryOperation struct {
	ID       string                `yaml:"operationId"`
	Security []map[string][]string `yaml:"security"`
}

type inventoryPath struct {
	Get    *inventoryOperation `yaml:"get"`
	Post   *inventoryOperation `yaml:"post"`
	Put    *inventoryOperation `yaml:"put"`
	Patch  *inventoryOperation `yaml:"patch"`
	Delete *inventoryOperation `yaml:"delete"`
}

func inventorySurface(path string) bool {
	return strings.HasPrefix(path, "/api/v1/admin/") ||
		path == "/api/v1/auth/2fa" || strings.HasPrefix(path, "/api/v1/auth/2fa/")
}

// Check the runtime registration table in both directions: a documented
// operation reaching a missing-resource 404 is not proof that a route exists.
// Include test-only admin routes so production cannot silently lose a route or
// acquire an undocumented one when the test guard moves.
func TestOpenAPI_AdminAndTwoFactorRouteInventory(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]inventoryPath `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	handler, _ := newTestRouter(t)
	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("the public router no longer exposes its registration table")
	}
	registered := map[string]bool{}
	if err := chi.Walk(routes, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if inventorySurface(path) {
			registered[method+" "+path] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(registered) == 0 {
		t.Fatal("empty admin/2FA registration table")
	}
	documented, ids := map[string]bool{}, map[string]string{}
	production, testOnly, twoFactor := 0, 0, 0
	for path, item := range doc.Paths {
		if !inventorySurface(path) {
			continue
		}
		for method, op := range map[string]*inventoryOperation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put,
			"PATCH": item.Patch, "DELETE": item.Delete,
		} {
			if op == nil {
				continue
			}
			key := method + " " + path
			documented[key] = true
			if op.ID == "" {
				t.Errorf("%s has no operationId", key)
			} else if previous, duplicate := ids[op.ID]; duplicate {
				t.Errorf("operationId %s belongs to both %s and %s", op.ID, previous, key)
			}
			ids[op.ID] = key
			if !registered[key] {
				t.Errorf("documented but not registered: %s (%s)", key, op.ID)
			}
			switch {
			case strings.HasPrefix(path, "/api/v1/admin/test/"):
				testOnly++
			case strings.HasPrefix(path, "/api/v1/admin/"):
				production++
			default:
				twoFactor++
				// All own-2FA operations require a person who signed in; no
				// anonymous, Basic or API-key alternative may slip into the spec.
				got := map[string]bool{}
				for _, alternative := range op.Security {
					if len(alternative) != 1 {
						t.Errorf("%s has an invalid session alternative: %v", key, alternative)
					}
					for scheme, roles := range alternative {
						got[scheme] = true
						if len(roles) != 0 {
							t.Errorf("%s unexpectedly needs capabilities: %v", key, roles)
						}
					}
				}
				if len(op.Security) != 2 || len(got) != 2 || !got["CookieAuth"] || !got["BearerAuth"] {
					t.Errorf("%s must document exactly CookieAuth or BearerAuth: %v", key, op.Security)
				}
			}
		}
	}
	for key := range registered {
		if !documented[key] {
			t.Errorf("registered but not documented: %s", key)
		}
	}
	t.Logf("catalog checked: %d production admin, %d test-only admin, %d own-2FA operations", production, testOnly, twoFactor)
}
