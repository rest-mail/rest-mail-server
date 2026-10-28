package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

func TestClientCredentialsExchange(t *testing.T) {
	tokens := auth.NewJWTService("secret", time.Hour, 24*time.Hour)
	for _, tc := range []struct {
		name, form, header, reason, fault string
		status                            int
	}{
		{name: "issued", form: "grant_type=client_credentials&client_id=public&client_secret=secret", status: 200},
		{name: "basic issued", form: "grant_type=client_credentials", header: "public:secret", status: 200},
		{name: "basic wrong secret", form: "grant_type=client_credentials", header: "public:wrong", status: 401},
		{name: "mixed authentication", form: "grant_type=client_credentials&client_id=public&client_secret=secret", header: "public:secret", status: 400},
		{name: "wrong secret", form: "grant_type=client_credentials&client_id=public&client_secret=wrong", status: 401},
		{name: "revoked", form: "grant_type=client_credentials&client_id=public&client_secret=secret", reason: auth.CredentialRevoked, status: 401},
		{name: "expired", form: "grant_type=client_credentials&client_id=public&client_secret=secret", reason: auth.CredentialExpired, status: 401},
		{name: "unsupported grant", form: "grant_type=password&client_id=public&client_secret=secret", status: 400},
		{name: "scope refused", form: "grant_type=client_credentials&client_id=public&client_secret=secret&scope=mail:read", status: 400},
		{name: "duplicate field", form: "grant_type=client_credentials&client_id=public&client_id=other&client_secret=secret", status: 400},
		{name: "audit unavailable", form: "grant_type=client_credentials&client_id=public&client_secret=secret", fault: "audit", status: 500},
		{name: "credential store unavailable", form: "grant_type=client_credentials&client_id=public&client_secret=secret", fault: "database", status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entries []*models.AuditEntry
			h := &OAuthHandler{tokens: tokens, now: time.Now,
				verify: func(_ context.Context, id, secret string, _ time.Time) (*models.Credential, *models.Mailbox, error) {
					if tc.fault == "database" {
						return nil, nil, errors.New("private database error")
					}
					if id != "public" || secret != "secret" {
						return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialWrongSecret}
					}
					if tc.reason != "" {
						return nil, nil, &auth.CredentialRefusal{Reason: tc.reason}
					}
					return &models.Credential{ID: 34, AccountID: 12, Kind: "client_secret", PublicID: id}, &models.Mailbox{ID: 12, Address: "robot@example.test", Active: true}, nil
				},
				record: func(_ context.Context, e *models.AuditEntry) error {
					if tc.fault == "audit" {
						return errors.New("private audit error")
					}
					entries = append(entries, e)
					return nil
				},
			}
			r := chi.NewRouter()
			h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, passingSessionChecks)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(tc.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.header != "" {
				req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(tc.header)))
			}
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("%d %s", out.Code, out.Body)
			}
			if tc.status == 401 && tc.header != "" && out.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("Basic refusal needs WWW-Authenticate")
			}
			if out.Header().Get("Cache-Control") != "no-store" || out.Header().Get("Pragma") != "no-cache" {
				t.Fatal("token response cacheable")
			}
			if tc.status != 200 {
				if strings.Contains(out.Body.String(), "private") {
					t.Fatal("failure exposed internal error")
				}
				if strings.Contains(out.Body.String(), "access_token") {
					t.Fatal("refusal issued token")
				}
				return
			}
			var got struct {
				AccessToken string `json:"access_token"`
				TokenType   string `json:"token_type"`
				ExpiresIn   int    `json:"expires_in"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.TokenType != "Bearer" || got.ExpiresIn != 300 {
				t.Fatalf("response %+v", got)
			}
			claims, err := tokens.ValidateAccessToken(got.AccessToken)
			if err != nil || claims.TwoFactor || len(claims.AMR) != 0 || claims.MachineCredentialID != 34 {
				t.Fatalf("claims %+v, error %v", claims, err)
			}
			if len(entries) != 1 || entries[0].ActorID == nil || *entries[0].ActorID != 12 || entries[0].CredentialID == nil || *entries[0].CredentialID != 34 {
				t.Fatalf("audit %+v", entries)
			}
		})
	}
}

func TestClientSecretCreationRequiresTwoFactorAndTier(t *testing.T) {
	h := NewCredentialHandler(nil)
	for _, machine := range []bool{false, true} {
		r := authzRequest("POST", "/", 1, nil, "")
		claims := middleware.GetClaims(r)
		if machine {
			claims.CredentialID = 3
		} else {
			claims.TwoFactor, claims.AuthTime = false, nil
		}
		_, err := h.createClientSecret(r, createAPIKeyRequest{})
		if refusal, ok := err.(*endpoint.Error); !ok || refusal.Status != http.StatusForbidden || refusal.Code != "two_factor_required" {
			t.Fatalf("machine %v: %v", machine, err)
		}
	}
}
