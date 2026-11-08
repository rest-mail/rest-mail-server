package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestListAdminAPIKeyOwnerValidation(t *testing.T) {
	for _, owner := range []string{
		"", "a", "ascii@", "@example.com", "ascii@xn--a.example", "42",
		strings.Repeat("a", 65) + "@example.com",
		"ascii@bad..example",
		"ascii@" + strings.Repeat("a", 64) + ".example",
		"Display <ascii@example.com>",
	} {
		t.Run(owner, func(t *testing.T) {
			reached := false
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys?owner="+url.QueryEscape(owner), nil)
			func() {
				defer func() { reached = recover() != nil }()
				chiRouterFor(NewCredentialHandler(nil)).ServeHTTP(rr, req)
			}()
			if reached || rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("database reached=%v, status=%d, body=%s", reached, rr.Code, rr.Body)
			}
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != "validation_failed" || response.Error.Details.Fields["owner"] == "" {
				t.Fatalf("not an owner validation error: %s", rr.Body)
			}
		})
	}
}

func TestListAdminAPIKeyOwnerMatchesCanonicalIDN(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	canonical := "用户@xn--fsqu00a.xn--4rr70v"
	if err := tx.Model(&fixture.ownerMailbox).Update("address", canonical).Error; err != nil {
		t.Fatal(err)
	}
	key := models.Credential{
		AccountID: fixture.ownerMailbox.ID, Kind: models.CredentialKindAPIKey,
		Name: "IDN owner", PublicID: "pub-idn-owner",
	}
	if err := tx.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"用户@例子.广告", "用户@XN--FSQU00A.XN--4RR70V", canonical} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys?owner="+url.QueryEscape(owner), nil)
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, freshTwoFactorClaims(fixture.ownerMailbox.ID, "")))
		chiRouterFor(NewCredentialHandler(tx)).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "IDN owner") || !strings.Contains(rr.Body.String(), canonical) {
			t.Fatalf("%q: %d %s", owner, rr.Code, rr.Body)
		}
	}
}

func TestListAdminAPIKeyOwnerAcceptsIDN(t *testing.T) {
	for _, owner := range []string{
		"ascii@example.com", "用户@例子.广告", "用户@xn--fsqu00a.xn--3lr804guic",
		strings.Repeat("a", 64) + "@example.com",
	} {
		t.Run(owner, func(t *testing.T) {
			reached := false
			rr := httptest.NewRecorder()
			func() {
				defer func() { reached = recover() != nil }()
				req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys?owner="+url.QueryEscape(owner), nil)
				chiRouterFor(NewCredentialHandler(nil)).ServeHTTP(rr, req)
			}()
			if !reached {
				t.Fatalf("valid owner refused before database: %d %s", rr.Code, rr.Body)
			}
		})
	}
}
