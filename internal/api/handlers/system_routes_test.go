package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/version"
)

func systemRoutes() http.Handler {
	r := chi.NewRouter()
	pass := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	RegisterSystem(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, pass)
	return r
}

// The typed answers write the bytes the maps they replaced wrote.
func TestSystemResponseJSONParity(t *testing.T) {
	notAfter := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"api root", map[string]any{
			"name": "restmail", "version": "1.2.3", "commit": "abc", "build_date": "today",
			"links": map[string]string{"health": "/api/health", "docs": "/api/docs", "login": "/api/v1/auth/login"},
		}, APIRoot{BuildDate: "today", Commit: "abc", Name: "restmail", Version: "1.2.3",
			Links: APIRootLinks{Docs: "/api/docs", Health: "/api/health", Login: "/api/v1/auth/login"}}},
		{"health", map[string]any{"status": "healthy", "db": "connected"},
			Health{Status: "healthy", DB: "connected"}},
		{"health with warnings", map[string]any{"status": "healthy", "db": "connected", "cert_warnings": []map[string]any{{
			"cert_id": uint(4), "domain": "example.test", "issuer": "CA", "not_after": notAfter,
			"status": "expiring_soon", "days_until": 9,
		}}}, Health{Status: "healthy", DB: "connected", CertWarnings: []CertWarning{{
			CertID: 4, Domain: "example.test", Issuer: "CA", NotAfter: notAfter, Status: "expiring_soon", DaysUntil: 9,
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, err := json.Marshal(tc.old)
			if err != nil {
				t.Fatal(err)
			}
			current, err := json.Marshal(tc.current)
			if err != nil {
				t.Fatal(err)
			}
			if string(old) != string(current) {
				t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
			}
		})
	}
}

func TestAPIRoot_NamesTheAPIAndWhereToStart(t *testing.T) {
	rr := httptest.NewRecorder()
	systemRoutes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api = %d %s", rr.Code, rr.Body)
	}
	var body struct {
		Data APIRoot `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rr.Body, err)
	}
	if body.Data.Name != "restmail" || body.Data.Version != version.Version || body.Data.Links.Login != "/api/v1/auth/login" {
		t.Fatalf("GET /api = %+v", body.Data)
	}
}
