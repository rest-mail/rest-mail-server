package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
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

// Every described operation on the public listener answers with the declared security
// headers, whatever refuses it first. Requests come from a public peer without credentials,
// so the network gates and authentication refusals are the responses checked. Where the
// answered status is described, the description declares each header the middleware sent,
// required and with the value served.
func TestEveryPublicResponseCarriesSecurityHeaders(t *testing.T) {
	cfg := middleware.SecurityHeadersConfig{HSTSMaxAgeSeconds: 63072000}
	routers := NewRouters(newFailingGormDB(t), auth.NewJWTService(routerTestSecret, time.Minute, time.Hour),
		&config.Config{Environment: "test", SecurityHeadersEnabled: true, HSTSMaxAgeSeconds: cfg.HSTSMaxAgeSeconds}, nil)
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
		t.Run(op.id, func(t *testing.T) {
			path := concretePath(op.path)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			req := httptest.NewRequest(strings.ToUpper(op.method), path, nil).WithContext(ctx)
			req.RemoteAddr = "192.0.2.10:40000"
			rr := httptest.NewRecorder()
			routers.Public.ServeHTTP(rr, req)
			responses, _ := op.operation["responses"].(map[string]interface{})
			declared, described := responses[strconv.Itoa(rr.Code)].(map[string]interface{})
			headers, _ := declared["headers"].(map[string]interface{})
			for name, values := range middleware.SecurityHeaderValues(cfg, path) {
				if !slices.Contains(rr.Header().Values(name), values[0]) {
					t.Errorf("%s %s answered %d with %s = %q, want %q", op.method, path, rr.Code, name, rr.Header().Values(name), values[0])
				}
				if !described {
					continue
				}
				header, _ := headers[name].(map[string]interface{})
				if header["required"] != true || header["description"] != rr.Header().Get(name) {
					t.Errorf("%s %s answered %d with %s = %q, but its description declares %v", op.method, path, rr.Code, name, rr.Header().Get(name), header)
				}
			}
		})
	}
}
