package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/examplepairs"
)

// Ordinary examples must fit below the cap; oversized requests supply actual bytes
// as well as a declared length, since some routes cap reads rather than headers.
const requirementBodyCap = 1 << 20

// requirementSetups are the shared requirement families exercised through the
// production router. Operation-specific prerequisites and later database faults
// are supplied by requirementExampleSetup (#1237).
var requirementSetups = map[string]bool{
	"principal=anonymous": true,
	"fault=session_store": true,
	"body=over_cap":       true,
	"fault=database":      true,
}

// requirementOf is a pair's single requirement as kind=value, or "" when it has none
// or more than one.
func requirementOf(pair oapifly.ExamplePair) string {
	if len(pair.Requires) != 1 {
		return ""
	}
	for kind, value := range pair.Requires {
		return kind + "=" + value
	}
	return ""
}

// Every description pair whose requirement is one of requirementSetups is sent through
// the production router with that requirement set up, and must answer exactly as the
// pair says: status, media type and body.
func TestRequirementExamplesThroughTheProductionRouter(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := describedAPI(t)
	type at struct{ path, method string }
	byOperation := map[string]at{}
	for path, methods := range paths {
		for method, op := range methods {
			byOperation[op.OperationID] = at{path, strings.ToUpper(method)}
		}
	}
	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{Environment: "test", APIMaxBodyBytes: requirementBodyCap, MasterKey: accessMasterKey,
		TOTP2FAEnabled: true, GatewayHostname: "mail.example.com", RecoveryEmailBaseURL: "https://mail.example.com"}
	// The session and grant checks are chosen when a router is built: the package's
	// seams stand them aside for stubbed, and useRealGrants puts the real ones back
	// for the router whose session store cannot be read.
	stubbed := NewRouters(newFailingGormDB(t), jwt, cfg, nil)
	t.Cleanup(stubbed.Close)
	session, err := jwt.GenerateSession(testAdminHolding([]string{"*"}), "admin@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	token := session.AccessToken
	stubSession, stubGrants := sessionCheck, heldGrants
	useRealGrants(t)
	real := NewRouters(newFailingGormDB(t), jwt, cfg, nil)
	t.Cleanup(real.Close)
	sessionCheck, heldGrants = stubSession, stubGrants

	operations := make([]string, 0, len(pairs))
	for op := range pairs {
		operations = append(operations, op)
	}
	sort.Strings(operations)
	for _, op := range operations {
		route, described := byOperation[op]
		for name, pair := range pairs[op] {
			requirement := requirementOf(pair)
			if !requirementSetups[requirement] || !described {
				continue
			}
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				body := []byte(pair.Request.Body)
				if pair.Request.MediaType != "" && pair.Request.MediaType != "application/json" && len(body) > 0 {
					var raw string
					if err := json.Unmarshal(body, &raw); err != nil {
						t.Fatal(err)
					}
					body = []byte(raw)
				}
				if requirement == "body=over_cap" {
					body = bytes.Repeat([]byte(" "), requirementBodyCap+1)
				}
				router, credential := stubbed.Public, token
				if requirement == "fault=session_store" {
					router = real.Public
				}
				router, credential, body = requirementExampleSetup(t, op, name, requirement, jwt, cfg, router, credential, body)
				req := httptest.NewRequest(route.method, messageCollectionReadURI(route.path, pair), bytes.NewReader(body))
				req.RemoteAddr = "127.0.0.1:12345"
				if requirement == "body=over_cap" {
					// Routes carry caps of their own above the group's; a declared length
					// past every one is refused before the body is read.
					req.ContentLength = 1 << 40
				}
				if len(body) > 0 {
					req.Header.Set("Content-Type", "application/json")
				}
				if pair.Request.MediaType != "" {
					req.Header.Set("Content-Type", pair.Request.MediaType)
				}
				for key, value := range pair.Request.Header {
					if text, ok := value.(string); ok {
						req.Header.Set(key, text)
					}
				}
				if requirement != "principal=anonymous" && req.Header.Get("Authorization") == "" {
					req.Header.Set("Authorization", "Bearer "+credential)
				}
				if op == "protocolSignIn" && requirement == "fault=database" {
					req.SetBasicAuth("admin@restmail.test", "fixture-password")
				}
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("%s %s with %s: got %d, want %d: %s", route.method, route.path, requirement, rr.Code, pair.Response.Status, rr.Body.String())
				}
				if pair.Response.MediaType != "" && !strings.HasPrefix(rr.Header().Get("Content-Type"), pair.Response.MediaType) {
					t.Fatalf("content type %q, want %q", rr.Header().Get("Content-Type"), pair.Response.MediaType)
				}
				if len(pair.Response.Body) > 0 {
					if strings.HasPrefix(pair.Response.MediaType, "text/") {
						var expected string
						if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
							t.Fatal(err)
						}
						if rr.Body.String() != expected {
							t.Fatalf("got %q, want %q", rr.Body.String(), expected)
						}
					} else {
						assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
					}
				}
			})
		}
	}
	// An operational failure is 500 at every read, while an unknown or denied
	// account remains indistinguishable (404). Never silently return empty mail.
	for _, phase := range []string{"lookup", "grants", "count", "content", "missing", "denied"} {
		t.Run("search_database/"+phase, func(t *testing.T) {
			faults := 0
			db := requirementDB(t, requirementConnection{operation: "search_" + phase, faults: &faults})
			routers := NewRouters(db, jwt, cfg, nil)
			t.Cleanup(routers.Close)
			rr := doRequest(routers.Public, http.MethodGet, "/api/v1/accounts/1/search", token, "")
			pair := pairs["searchMessages"]["internal_error"]
			status, body, calls := pair.Response.Status, pair.Response.Body, 1
			if phase == "missing" || phase == "denied" {
				status, body, calls = http.StatusNotFound, []byte(`{"error":{"code":"not_found","message":"Account not found"}}`), 0
			}
			if rr.Code != status || faults != calls {
				t.Fatalf("got %d with %d injected faults, want %d with %d: %s", rr.Code, faults, status, calls, rr.Body.String())
			}
			assertPublicExampleBody(t, body, rr.Body.Bytes())
		})
	}
	// Both the declared-length fast path and actual streamed bytes must be capped
	// before these public handlers look up credentials or touch the database.
	for _, path := range []string{"login", "logout", "refresh", "reset", "recover", "owner/challenge", "owner/token", "owner/status"} {
		t.Run("public_body_cap/"+path, func(t *testing.T) {
			lengths := []int64{requirementBodyCap + 1}
			if path != "owner/challenge" { // This endpoint consumes no body.
				lengths = append(lengths, -1)
			}
			for _, length := range lengths {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+path, bytes.NewReader(bytes.Repeat([]byte(" "), requirementBodyCap+1)))
				req.ContentLength = length
				rr := httptest.NewRecorder()
				stubbed.Public.ServeHTTP(rr, req)
				if rr.Code != http.StatusRequestEntityTooLarge {
					t.Fatalf("body with Content-Length %d: got %d, want 413: %s", length, rr.Code, rr.Body.String())
				}
				assertPublicExampleBody(t, pairs["login"]["request_too_large"].Response.Body, rr.Body.Bytes())
			}
		})
	}
}
