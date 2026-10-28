package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSecondFactorAuthTimeSurvivesRefresh(t *testing.T) {
	svc := NewJWTService("auth-time-test", time.Hour, 24*time.Hour)
	started := time.Now().Truncate(time.Second)
	pair, err := svc.GenerateSession(1, "a@example.test", 0, []string{AMRPassword, AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := svc.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if access.AuthTime == nil || access.AuthTime.Before(started) || access.AuthTime.After(time.Now()) {
		t.Fatalf("missing sign-in auth_time: %+v", access)
	}
	old := jwt.NewNumericDate(time.Now().Add(-time.Hour))
	refreshed, err := svc.GenerateSession(1, "a@example.test", 0, access.AMR, false, ContinueSession(access.SessionID), ContinueAuthentication(old))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{refreshed.AccessToken, refreshed.RefreshToken} {
		c, err := svc.ValidateToken(token)
		if err != nil {
			t.Fatal(err)
		}
		if c.AuthTime == nil || !c.AuthTime.Equal(old.Time) {
			t.Fatalf("refresh reset auth_time: %+v", c.AuthTime)
		}
		if !c.IssuedAt.After(old.Time) {
			t.Fatal("iat must describe the refreshed token")
		}
	}
	// A pre-auth_time refresh must not acquire fresh proof merely by refreshing.
	legacy, err := svc.GenerateSession(1, "a@example.test", 0, access.AMR, false, ContinueSession(access.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := svc.ValidateToken(legacy.AccessToken)
	if c.AuthTime != nil {
		t.Fatal("refresh invented second-factor proof")
	}
	plain, _ := svc.GenerateSession(1, "a@example.test", 0, []string{AMRPassword}, false, ContinueAuthentication(old))
	c, _ = svc.ValidateToken(plain.AccessToken)
	if c.AuthTime != nil {
		t.Fatal("password-only session has second-factor auth_time")
	}
}
