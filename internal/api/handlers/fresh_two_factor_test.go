package handlers

import (
	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/auth"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequireFreshTwoFactor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		machine bool
		missing bool
		code    string
	}{
		{name: "fresh", age: time.Minute},
		{name: "stale", age: 16 * time.Minute, code: "recent_two_factor_required"},
		{name: "future", age: -time.Minute, code: "recent_two_factor_required"},
		{name: "legacy", missing: true, code: "recent_two_factor_required"},
		{name: "machine", age: time.Minute, machine: true, code: "two_factor_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &auth.Claims{AccountID: 1, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true, AuthTime: jwt.NewNumericDate(time.Now().Add(-tc.age))}
			if tc.machine {
				c.CredentialKind = "api_key"
				c.CredentialID = 1
			}
			if tc.missing {
				c.AuthTime = nil
			}
			r := withClaims(httptest.NewRequest(http.MethodPost, "/", nil), c)
			err := RequireFreshTwoFactor(r)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e, ok := err.(*endpoint.Error)
			if !ok || e.Status != 403 || e.Code != tc.code {
				t.Fatalf("got %v, want 403 %s", err, tc.code)
			}
			if !tc.machine && RequireTwoFactor(r) != nil {
				t.Fatal("old human proof still satisfies ordinary 2FA")
			}
		})
	}
}

func TestFreshTwoFactorShorterWindow(t *testing.T) {
	c := &auth.Claims{AccountID: 1, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true, AuthTime: jwt.NewNumericDate(time.Now().Add(-6 * time.Minute))}
	r := withClaims(httptest.NewRequest("POST", "/", nil), c)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := RequireFreshTwoFactor(r); err == nil {
			t.Error("shortened window ignored")
		}
	})
	TwoFactorWindow(5*time.Minute)(next).ServeHTTP(httptest.NewRecorder(), r)
}

// freshTwoFactorClaims is an ordinary human session for tests whose subject is
// authorization, persistence or another handler behavior beyond the 2FA gate.
func freshTwoFactorClaims(id uint, address string) *auth.Claims {
	return &auth.Claims{AccountID: id, Address: address, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true, AuthTime: jwt.NewNumericDate(time.Now())}
}

func TestRefreshPreservesStaleSecondFactorTime(t *testing.T) {
	jwtService := auth.NewJWTService("refresh-auth-time", time.Minute, time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwtService, store)
	old := jwt.NewNumericDate(time.Now().Add(-time.Hour))
	pair, err := jwtService.GenerateSession(42, "user@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false, auth.ContinueSession("existing-session"), auth.ContinueAuthentication(old))
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, 42)
	rr := doRefresh(h, pair.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body)
	}
	for _, token := range []string{refreshCookieValue(rr), accessCookieValue(rr)} {
		c, err := jwtService.ValidateToken(token)
		if err != nil {
			t.Fatal(err)
		}
		if c.AuthTime == nil || !c.AuthTime.Equal(old.Time) {
			t.Fatalf("refresh changed auth_time: %v", c.AuthTime)
		}
		r := withClaims(httptest.NewRequest("POST", "/", nil), c)
		if err := RequireFreshTwoFactor(r); err == nil {
			t.Fatal("refresh reopened sudo window")
		}
	}
}
