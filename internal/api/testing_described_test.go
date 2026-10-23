package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

func TestTestRoutesDescribeWildcardAndMiddleware(t *testing.T) {
	doc := generatedDocument(t)
	seen := 0
	for _, op := range operationsOf(doc) {
		if !strings.HasPrefix(op.path, "/api/v1/admin/test/") {
			continue
		}
		seen++
		if got, want := securityOf(doc, op.operation), "[APIKeyAuth[*] | BearerAuth[*] | CookieAuth[*]]"; got != want {
			t.Errorf("%s security = %s, want %s", op.id, got, want)
		}
		responses := op.operation["responses"].(map[string]interface{})
		for _, status := range []string{"401", "403", "413", "500", "503"} {
			if responses[status] == nil {
				t.Errorf("%s omits %s", op.id, status)
			}
		}
	}
	if seen != 7 {
		t.Fatalf("described %d test operations, want 7", seen)
	}
}

func TestProductionOmitsTestRoutesAndDescription(t *testing.T) {
	for _, env := range []string{"production", "prod", "PROD", "Production", "prod-eu", "production-us", "  Prod  "} {
		t.Run(env, func(t *testing.T) {
			jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
			routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: env}, nil)
			defer routers.Close()
			for _, route := range routers.Registry.Routes() {
				if strings.Contains(route, "/api/v1/admin/test/") {
					t.Errorf("production registers %s", route)
				}
			}
			err := chi.Walk(routers.Public.(chi.Routes), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
				if strings.HasPrefix(path, "/api/v1/admin/test/") {
					t.Errorf("production serves %s %s", method, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			doc, warnings := routers.Description("test")
			if len(warnings) > 0 {
				t.Fatal(warnings)
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Paths map[string]json.RawMessage `json:"paths"`
			}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatal(err)
			}
			for path := range parsed.Paths {
				if strings.HasPrefix(path, "/api/v1/admin/test/") {
					t.Errorf("production describes %s", path)
				}
			}
		})
	}
}
