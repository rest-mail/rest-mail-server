package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/examplepairs"
)

// TestRecoveryEmailUnconfiguredExamples proves the recovery_unconfigured pairs
// through the production router: with no HTTPS RESTMAIL_RECOVERY_EMAIL_BASE_URL
// the server cannot send a recovery link, so registering an address and asking
// for a link both answer 503 before anything is read or sent.
func TestRecoveryEmailUnconfiguredExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	r := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test"}, nil)
	t.Cleanup(r.Close)
	session, err := jwt.GenerateSession(testAdminHolding([]string{"*"}), "admin@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"registerRecoveryEmail", "requestRecoveryEmail"} {
		t.Run(op, func(t *testing.T) {
			pair, ok := pairs[op]["recovery_unconfigured"]
			if !ok {
				t.Fatalf("%s has no recovery_unconfigured pair", op)
			}
			examplepairs.Proven(t, op, "recovery_unconfigured")
			req := requirementRequest(routes[op], pair)
			if op == "registerRecoveryEmail" {
				req.Header.Set("Authorization", "Bearer "+session.AccessToken)
			}
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("got %d %s", rr.Code, rr.Body.String())
			}
			assertRequirementResponse(t, pair, rr)
		})
	}
}
