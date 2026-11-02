package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

func TestInvitationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to replay invitation examples")
	}
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, examples := range pairs {
		if operation != "inviteAccount" && operation != "acceptInvitation" {
			continue
		}
		for name, pair := range examples {
			t.Run(operation+"/"+name, func(t *testing.T) {
				mode := map[string]string{"insufficient_permissions": "denied", "missing": "missing_account", "existing_credentials": "established_account", "invalid_token": "expired", "wrong_purpose": "wrong_purpose", "unavailable": "unavailable"}[name]
				if name == "internal_error" {
					mode = "credential_failure"
					if operation == "inviteAccount" {
						mode = "mint_failure"
					}
				}
				if name == "rate_limited" && operation == "acceptInvitation" {
					mode = "rate_limited"
				}
				f := newInvitationFixture(t, mode)
				path := "/api/v1/auth/invitation"
				uri := path
				if operation == "inviteAccount" {
					path = "/api/v1/admin/mailboxes/{id}/invitation"
					uri = strings.ReplaceAll(path, "{id}", "12")
					if name == "invalid_id" {
						uri = strings.ReplaceAll(path, "{id}", "0")
					}
				}
				body := ""
				if len(pair.Request.Body) > 0 {
					body = string(pair.Request.Body)
				}
				if pair.Requires["body"] == "malformed" {
					body = "{"
				}
				if operation == "inviteAccount" && name == "request_too_large" {
					body = strings.Repeat("x", 513)
				}
				subject, token := http.Handler(f.handler), f.token
				switch {
				case operation == "acceptInvitation" && name == "rate_limited":
					subject = invitationSpentThrottle(f.handler, uri)
				case operation == "inviteAccount" && name == "rate_limited":
					subject = apiKeyRefusalLimited(t)
				case name == "two_factor_required" || name == "recent_two_factor_required":
					subject, token = invitationTwoFactorRefusal(t, name)
				}
				if operation == "acceptInvitation" || name == "unauthenticated" {
					token = ""
				}
				checkNamedPairWithVertrag(t, binary, invitationNormalizedMint(t, subject, operation), namedPairExchange{
					method: http.MethodPost, path: path, uri: uri, name: name, status: pair.Response.Status, body: body, token: token,
				})
			})
		}
	}
}

// invitationNormalizedMint replaces the volatile values of a minted invitation.
// The dedicated route test verifies entropy, persisted hash, lifetime and
// secrecy headers before this illustration is normalized.
func invitationNormalizedMint(t *testing.T, handler http.Handler, operation string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, r)
		if operation == "inviteAccount" && rr.Code == http.StatusCreated {
			var payload map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Error(err)
				return
			}
			data := payload["data"].(map[string]any)
			data["token"] = invitationExampleToken
			data["expires_at"] = "2030-01-01T00:15:00Z"
			normalized, _ := json.Marshal(payload)
			rr.Body.Reset()
			_, _ = rr.Body.Write(normalized)
		}
		for k, values := range rr.Header() {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		w.WriteHeader(rr.Code)
		_, _ = w.Write(rr.Body.Bytes())
	})
}

// invitationSpentThrottle spends the sign-in throttle from the caller's address
// before the real request arrives, so that request is the one refused.
func invitationSpentThrottle(handler http.Handler, uri string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spend := httptest.NewRequest(http.MethodPost, uri, strings.NewReader("{"))
		spend.Header.Set("Content-Type", "application/json")
		spend.RemoteAddr = r.RemoteAddr
		handler.ServeHTTP(httptest.NewRecorder(), spend)
		handler.ServeHTTP(w, r)
	})
}

// invitationTwoFactorRefusal is the production router with a session the
// two-factor check refuses: password-only, or signed in longer ago than the
// fresh window. It uses the same sessions as TestTwoFactorRefusalExamples.
func invitationTwoFactorRefusal(t *testing.T, name string) (http.Handler, string) {
	t.Helper()
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	r := NewRouters(requirementDB(t, &twoFactorRefusalConnection{}), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey}, nil)
	t.Cleanup(r.Close)
	amr, opts := []string{auth.AMRPassword}, []auth.TokenOption(nil)
	if name == "recent_two_factor_required" {
		amr = []string{auth.AMRPassword, auth.AMROTP}
		opts = append(opts, auth.ContinueSession("stale-session"), auth.ContinueAuthentication(jwtlib.NewNumericDate(time.Now().Add(-time.Hour))))
	}
	session, err := jwt.GenerateSession(1, "admin@example.test", 0, amr, false, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r.Public, session.AccessToken
}
