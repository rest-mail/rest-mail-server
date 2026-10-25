package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

func TestSecurityHeadersDescribeEveryResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		hsts    int
	}{
		{"enabled", true, 63072000},
		{"without_hsts", true, 0},
		{"disabled", false, 63072000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routers := NewRouters(newFailingGormDB(t), auth.NewJWTService(routerTestSecret, time.Minute, time.Hour),
				&config.Config{Environment: "test", SecurityHeadersEnabled: tc.enabled, HSTSMaxAgeSeconds: tc.hsts}, nil)
			t.Cleanup(routers.Close)
			description, warnings := routers.Description("test")
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			data, err := json.Marshal(description)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]interface{}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			for _, op := range operationsOf(doc) {
				want := middleware.SecurityHeaderValues(middleware.SecurityHeadersConfig{HSTSMaxAgeSeconds: tc.hsts}, op.path)
				for status, value := range op.operation["responses"].(map[string]interface{}) {
					response := value.(map[string]interface{})
					headers, _ := response["headers"].(map[string]interface{})
					for name, values := range want {
						header, present := headers[name].(map[string]interface{})
						if !tc.enabled {
							if present {
								t.Errorf("%s %s unexpectedly declares %s", op.id, status, name)
							}
							continue
						}
						if !present || header["required"] != true || header["description"] != values[0] {
							t.Errorf("%s %s: %s = %v, want required %q", op.id, status, name, header, values[0])
						}
					}
					if tc.hsts == 0 && headers["Strict-Transport-Security"] != nil {
						t.Errorf("%s %s declares disabled HSTS", op.id, status)
					}
				}
			}
			// Check the real listener on a success, an authentication refusal and docs.
			for _, path := range []string{"/api/v1/server", "/api/v1/admin/domains", "/api/docs"} {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				routers.Public.ServeHTTP(rr, req)
				for name, values := range middleware.SecurityHeaderValues(middleware.SecurityHeadersConfig{HSTSMaxAgeSeconds: tc.hsts}, path) {
					want := ""
					if tc.enabled {
						want = values[0]
					}
					if rr.Header().Get(name) != want {
						t.Errorf("%s %s = %q, want %q", path, name, rr.Header().Get(name), want)
					}
				}
			}
		})
	}
}
