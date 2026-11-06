package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/api/limittest"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func installationSenderRuleMutationScenarios(t *testing.T) map[string]installationSenderRulesScenario {
	t.Helper()
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
	return map[string]installationSenderRulesScenario{
		"unauthenticated":     {"", unavailable},
		"setup_required":      {pending.AccessToken, failed},
		"request_too_large":   {session.AccessToken, makeRouter(8)},
		"rate_limited":        {"", apiKeyRefusalLimited(t)},
		"internal_error":      {session.AccessToken, failed},
		"session_unavailable": {session.AccessToken, unavailable},
	}
}

func TestInstallationSenderRuleMutationExamplesWithVertrag(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	fixture := newGrantsFixture(t)
	owner := fixture.token(t, "owner")
	scenarios := installationSenderRuleMutationScenarios(t)
	for _, operation := range []string{"addInstallationSenderRule", "removeInstallationSenderRule"} {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				scenario, ok := scenarios[name]
				if !ok {
					scenario = installationSenderRulesScenario{owner, fixture.router}
				}
				seed := func() {
					t.Helper()
					if name != "duplicate" && name != "removed" {
						return
					}
					rule := models.InstallationSenderRule{Pattern: "@blocked.example", ListType: "block", Reason: "Sends spam"}
					if name == "removed" {
						rule.ID = 1
					}
					if err := fixture.db.Create(&rule).Error; err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() {
					if err := fixture.db.Where("pattern = ?", "@blocked.example").Delete(&models.InstallationSenderRule{}).Error; err != nil {
						t.Error(err)
					}
				})
				seed()
				route := routes[operation]
				request := requirementRequest(route, pair)
				body := string(pair.Request.Body)
				if pair.Requires["body"] == "malformed" {
					body = "{"
				}
				if pair.Requires["body"] == "over_cap" && route.method == http.MethodDelete {
					body = "123456789"
				}
				if body != string(pair.Request.Body) {
					request.Body = io.NopCloser(strings.NewReader(body))
					request.ContentLength = int64(len(body))
				}
				if scenario.token != "" {
					request.Header.Set("Authorization", "Bearer "+scenario.token)
				}
				response := httptest.NewRecorder()
				scenario.router.ServeHTTP(response, request)
				if response.Code == http.StatusCreated {
					var got, want struct {
						Data handlers.InstallationSenderRule `json:"data"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
						t.Fatal(err)
					}
					var stored models.InstallationSenderRule
					if err := fixture.db.First(&stored, got.Data.ID).Error; err != nil {
						t.Fatal(err)
					}
					if got.Data.ID == 0 || got.Data.Pattern != stored.Pattern || got.Data.ListType != stored.ListType ||
						got.Data.Reason != stored.Reason || got.Data.CreatedBy == nil || stored.CreatedBy == nil ||
						*got.Data.CreatedBy != *stored.CreatedBy || *stored.CreatedBy != fixture.admins["owner"].user.ID ||
						!got.Data.CreatedAt.Equal(stored.CreatedAt) || time.Since(got.Data.CreatedAt).Abs() > time.Minute {
						t.Fatalf("created response does not describe its stored rule: %+v, %+v", got.Data, stored)
					}
					got.Data.ID, got.Data.CreatedBy, got.Data.CreatedAt = want.Data.ID, want.Data.CreatedBy, want.Data.CreatedAt
					rewriteBody(t, response, got)
					if err := fixture.db.Delete(&stored).Error; err != nil {
						t.Fatal(err)
					}
				}
				assertRequirementResponse(t, pair, response)
				if name == "removed" {
					if err := fixture.db.First(&models.InstallationSenderRule{}, 1).Error; err != gorm.ErrRecordNotFound {
						t.Fatalf("removed rule still readable: %v", err)
					}
					if response.Header().Get("Content-Type") != "" {
						t.Fatal("204 response carried a content type")
					}
				}
				if name == "rate_limited" {
					limittest.RetryAfter(t, response.Header())
				}
				if binary := os.Getenv("RESTMAIL_VERTRAG_BINARY"); binary != "" {
					if name == "removed" {
						seed()
						checkRegisteredExamplesWithVertrag(t, binary, scenario.router, scenario.token, route.method, "204", []string{route.path}, 1, false)
					} else {
						checkNamedPairWithVertrag(t, binary, scenario.router, namedPairExchange{method: route.method, path: route.path,
							uri: request.URL.RequestURI(), name: name, status: pair.Response.Status, body: body, token: scenario.token})
					}
				}
			})
		}
	}
}
