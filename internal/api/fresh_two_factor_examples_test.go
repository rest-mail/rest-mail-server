package api

import (
	"github.com/restmail/restmail/internal/auth"
	"testing"
)

// Sensitive-operation fixtures use human proof; password-only and credential
// refusal cases keep their own explicit principals.
func freshMailboxToken(t *testing.T, service *auth.JWTService) string {
	t.Helper()
	pair, err := service.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	return pair.AccessToken
}
