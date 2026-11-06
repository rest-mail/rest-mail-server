package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// The reputation view's refusal of a session without queue:read at the server's
// own scope answers as its example pair says, through the production router
// (#1590). The refusal comes before any database read.
func TestReputationExampleRequiredCapability(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	pair, ok := pairs["getReputation"]["required_capability"]
	if !ok {
		t.Fatal("getReputation/required_capability pair missing")
	}
	if pair.Requires["principal"] != "without_capability" {
		t.Fatalf("the pair requires %v; want a principal without the capability", pair.Requires)
	}
	examplepairs.Proven(t, "getReputation", "required_capability")

	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey}, nil)
	t.Cleanup(routers.Close)
	session, err := jwt.GenerateSession(testAdminHolding([]string{middleware.CapDomainsRead}), "reader@example.test", 0,
		[]string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/admin/reputation", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	rr := httptest.NewRecorder()
	routers.Public.ServeHTTP(rr, req)
	if rr.Code != pair.Response.Status {
		t.Fatalf("got %d, want %d: %s", rr.Code, pair.Response.Status, rr.Body)
	}
	assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
}
