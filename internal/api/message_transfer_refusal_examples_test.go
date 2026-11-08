package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/api/limittest"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestMessageTransferRefusalExamples(t *testing.T) {
	configureAuthReadPush(t)
	useRealGrants(t)
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, time.Hour)
	makeRouter := func(cap int64) http.Handler {
		routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: cap}, nil)
		t.Cleanup(routers.Close)
		return routers.Public
	}
	unavailable := makeRouter(4096)
	sessionCheck = func(*handlers.SessionCache) middleware.SessionCurrent {
		return func(uint, int) (bool, error) { return true, nil }
	}
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) {
			return grants.HeldOf(grants.Entry{Capability: grants.Wildcard, Scope: grants.All}), nil
		}
	}
	failed := makeRouter(4096)
	session, err := jwt.GenerateSession(10, "operator@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := jwt.GenerateSession(10, "operator@example.test", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := map[string]accountReadScenario{
		"unauthenticated":     {failed, ""},
		"setup_required":      {failed, pending.AccessToken},
		"malformed_json":      {failed, session.AccessToken},
		"request_too_large":   {makeRouter(8), session.AccessToken},
		"rate_limited":        {apiKeyRefusalLimited(t), ""},
		"internal_error":      {failed, session.AccessToken},
		"session_unavailable": {unavailable, session.AccessToken},
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	route := requirementRoutes(t)["transferMessages"]
	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			pair, ok := pairs["transferMessages"][name]
			if !ok {
				t.Fatal("missing transfer refusal pair")
			}
			examplepairs.Proven(t, "transferMessages", name)
			request := requirementRequest(route, pair)
			body := string(pair.Request.Body)
			if pair.Requires["body"] == "malformed" {
				body = "{"
				request.Body = io.NopCloser(strings.NewReader(body))
				request.ContentLength = int64(len(body))
			}
			if scenario.token != "" {
				request.Header.Set("Authorization", "Bearer "+scenario.token)
			}
			response := httptest.NewRecorder()
			scenario.handler.ServeHTTP(response, request)
			assertRequirementResponse(t, pair, response)
			if name == "rate_limited" {
				limittest.RetryAfter(t, response.Header())
			}
			if binary := os.Getenv("RESTMAIL_VERTRAG_BINARY"); binary != "" {
				checkNamedPairWithVertrag(t, binary, scenario.handler, namedPairExchange{
					method: route.method, path: route.path, uri: request.URL.RequestURI(), name: name,
					status: pair.Response.Status, body: body, token: scenario.token,
				})
			}
		})
	}
}
