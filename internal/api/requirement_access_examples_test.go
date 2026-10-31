package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/examplepairs"
)

type requirementRoute struct{ method, path string }

func requirementRoutes(t *testing.T) map[string]requirementRoute {
	t.Helper()
	paths, _ := describedAPI(t)
	out := map[string]requirementRoute{}
	for path, methods := range paths {
		for method, op := range methods {
			out[op.OperationID] = requirementRoute{strings.ToUpper(method), path}
		}
	}
	return out
}

func requirementRequest(route requirementRoute, pair oapifly.ExamplePair) *http.Request {
	req := httptest.NewRequest(route.method, messageCollectionReadURI(route.path, pair), bytes.NewReader(pair.Request.Body))
	req.RemoteAddr = "127.0.0.1:12345"
	if len(pair.Request.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range pair.Request.Header {
		if v, ok := value.(string); ok {
			req.Header.Set(key, v)
		}
	}
	return req
}

func assertRequirementResponse(t *testing.T, pair oapifly.ExamplePair, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != pair.Response.Status || (pair.Response.MediaType != "" && !strings.HasPrefix(rr.Header().Get("Content-Type"), pair.Response.MediaType)) {
		t.Fatalf("got %d %q %s, want %d %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
	}
	if len(pair.Response.Body) > 0 {
		assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
	} else if rr.Body.Len() != 0 {
		t.Fatalf("body on empty response: %s", rr.Body.String())
	}
}

// Exhaust actual production budgets. Public sign-in and owner routes use their
// own buckets; authenticated routes also throttle malformed API-key refusals.
func TestRateRequirementExamples(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for op, named := range pairs {
		for name, pair := range named {
			if requirementOf(pair) != "limit=rate" {
				continue
			}
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				var handler http.Handler
				switch op {
				case "login", "refreshToken", "recoverAccount", "resetAdmin", "acceptInvitation", "ownerChallenge", "ownerToken", "ownerStatus", "changeOwnPassword", "enrollTwoFactor", "confirmTwoFactor", "disableTwoFactor", "createAPIKey", "createAppPassword", "createClientSecret", "linkAccount", "receiveTlsReport",
					"registerRecoveryEmail", "requestRecoveryEmail", "verifyRecoveryEmail", "redeemRecoveryEmail", "issueClientCredentialsToken":
					jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
					r := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", AuthRateLimitEnabled: true, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1, TLSRPTRateLimitEnabled: true, TLSRPTRateLimitRPS: 1.0 / 3600, TLSRPTRateLimitBurst: 1}, nil)
					t.Cleanup(r.Close)
					handler = r.Public
					prime := "/api/v1/auth/login"
					if strings.HasPrefix(op, "owner") {
						prime = "/api/v1/auth/owner/challenge"
					}
					if op == "receiveTlsReport" {
						prime = routes[op].path
					}
					req := httptest.NewRequest(http.MethodPost, prime, nil)
					req.RemoteAddr = "127.0.0.1:12345"
					handler.ServeHTTP(httptest.NewRecorder(), req)
					session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, []string{auth.AMRPassword}, false)
					if err != nil {
						t.Fatal(err)
					}
					handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						req.Header.Set("Authorization", "Bearer "+session.AccessToken)
						r.Public.ServeHTTP(w, req)
					})
				default:
					handler = apiKeyRefusalLimited(t)
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, requirementRequest(routes[op], pair))
				assertRequirementResponse(t, pair, rr)
				retry, err := strconv.Atoi(rr.Header().Get("Retry-After"))
				if err != nil || retry < 1 {
					t.Fatalf("invalid Retry-After: %q", rr.Header().Get("Retry-After"))
				}
			})
		}
	}
}

// Refusals with no SQL prerequisites still go through authentication and the
// production middleware. SQL-dependent refusals are covered by the state fixture.
func TestAccessRequirementExamples(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"deactivateGrant", "csrf_mismatch"}, {"unlinkAccount", "csrf_mismatch"},
		{"updateMailbox", "csrf_mismatch"}, {"protocolSignIn", "csrf_mismatch"},
		{"unlinkAccount", "own_account"}, {"enrollTwoFactor", "disabled"},
		{"createAppPassword", "credential"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			pair := pairs[tc.op][tc.name]
			jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
			r := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey}, nil)
			t.Cleanup(r.Close)
			session, err := jwt.GenerateSession(1, "admin@restmail.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
			if err != nil {
				t.Fatal(err)
			}
			req := requirementRequest(routes[tc.op], pair)
			if tc.name == "csrf_mismatch" {
				req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: session.AccessToken})
				req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "expected"})
				req.Header.Set(auth.CSRFHeaderName, "different")
			} else {
				req.Header.Set("Authorization", "Bearer "+session.AccessToken)
			}
			if tc.name == "credential" {
				req.SetBasicAuth("admin@restmail.test", "the current password")
			}
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			assertRequirementResponse(t, pair, rr)
		})
	}
}
