package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

var mozillaExamplePaths = map[string]string{
	"mozillaAutoconfig":          "/mail/config-v1.1.xml",
	"mozillaAutoconfigWellKnown": "/.well-known/autoconfig/mail/config-v1.1.xml",
}

func TestAutoconfigDatabaseFailureIsNotUnknownDomain(t *testing.T) {
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", GatewayHostname: "mail.example.com"}, nil)
	t.Cleanup(routers.Close)
	for _, tc := range []struct{ name, method, path, body string }{
		{"mozilla", http.MethodGet, "/mail/config-v1.1.xml", ""},
		{"well-known", http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml", ""},
		{"outlook XML", http.MethodPost, "/autodiscover/autodiscover.xml", `<Autodiscover><Request><EMailAddress>user@example.com</EMailAddress></Request></Autodiscover>`},
		{"outlook query fallback", http.MethodPost, "/autodiscover/autodiscover.xml", "not XML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path+"?emailaddress=user%40example.com", strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/xml")
			}
			rr := httptest.NewRecorder()
			routers.Public.ServeHTTP(rr, req)
			if rr.Code != http.StatusInternalServerError || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("database failure = %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}
