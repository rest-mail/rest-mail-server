package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestIdentityAccountAddressDescriptionAndOwnershipPairs(t *testing.T) {
	const operation = "listAccountAddresses"
	const path = "/api/v1/accounts/{id}/addresses"
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}

	db := openAccessDB(t)
	router := identityRouter(t, db)
	principals := seedAdmins(t, db, []fixtureAdmin{{name: "writer"}, {name: "outsider"}})
	domain := models.Domain{Name: "example.com", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	owner := models.Mailbox{ID: 10, DomainID: domain.ID, LocalPart: "user", Address: "user@example.com", Active: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(accessPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.NewCredentialRepository(db).SetPassword(owner.ID, hash, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []models.Alias{
		{DomainID: domain.ID, SourceAddress: "help@example.com", DestinationAddress: owner.Address, Active: true},
		{DomainID: domain.ID, SourceAddress: "private@example.com", DestinationAddress: principals["outsider"].user.Address, Active: true},
	} {
		if err := db.Create(&alias).Error; err != nil {
			t.Fatal(err)
		}
	}

	role := models.Role{Name: "write-only"}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	// A scoped write grant needs the session capability at the same scope.
	for _, name := range []string{models.CapMailWrite, models.CapMailboxSession} {
		var capability models.Capability
		if err := db.Where("name = ?", name).First(&capability).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: capability.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.Grant{
		HolderKind: models.GrantHolderAccount, HolderID: principals["writer"].user.ID,
		RoleID: role.ID, Scope: grants.Mailbox(owner.Address), Mode: models.GrantActive,
		GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	login := identityCall(router, "/login", "", map[string]string{"email": owner.Address, "password": accessPassword})
	identityStatus(t, login, http.StatusOK, "")
	var loginBody struct {
		Data struct {
			AccessToken string `json:"access_token"`
		}
	}
	decodeInto(t, login, &loginBody)
	if loginBody.Data.AccessToken != "" {
		t.Fatal("browser login delivered its access token in JSON")
	}
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == auth.AccessCookieName && c.HttpOnly && c.Value != "" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("browser login did not deliver an httpOnly access cookie")
	}

	writer := principals["writer"]
	writerToken := identitySession(t, identityLogin(router, writer.user.Address, accessPassword, ""), writer.user.Address).AccessToken
	outsider := principals["outsider"]
	outsiderToken := identitySession(t, identityLogin(router, outsider.user.Address, accessPassword, ""), outsider.user.Address).AccessToken

	for _, tc := range []struct {
		name, pair, bearer string
		id                 uint
		cookie             *http.Cookie
	}{
		{name: "primary", pair: "primary", cookie: cookie},
		{name: "write_only", pair: "write_only", id: owner.ID, bearer: writerToken},
		{name: "missing", pair: "missing", bearer: outsiderToken},
		{name: "other_tenants_mailbox", pair: "missing", id: owner.ID, bearer: outsiderToken},
		{name: "unauthenticated", pair: "unauthenticated", id: owner.ID},
		{name: "invalid_id", pair: "invalid_id", bearer: outsiderToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, ok := pairs[operation][tc.pair]
			if !ok {
				t.Fatalf("missing named pair %s/%s", operation, tc.pair)
			}
			examplepairs.Proven(t, operation, tc.pair)
			id := strconv.FormatUint(uint64(tc.id), 10)
			if tc.id == 0 {
				value, ok := pair.Request.Path["id"].(float64)
				if !ok {
					t.Fatalf("named pair %s/%s has no numeric account ID", operation, tc.pair)
				}
				id = strconv.FormatFloat(value, 'f', -1, 64)
			}
			req := httptest.NewRequest(http.MethodGet, strings.ReplaceAll(path, "{id}", id), nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			assertRequirementResponse(t, pair, rr)
		})
	}

	t.Run("served_description_distinguishes_hidden_and_visible_accounts", func(t *testing.T) {
		rr := doRequest(router, http.MethodGet, "/api/docs/openapi.json", "", "")
		identityStatus(t, rr, http.StatusOK, "")
		var doc struct {
			Paths map[string]map[string]struct {
				Description string
				Responses   map[string]any
			}
		}
		decodeInto(t, rr, &doc)
		get := doc.Paths[path]["get"]
		if get.Responses["403"] == nil || get.Responses["404"] == nil ||
			!strings.Contains(get.Description, "Hidden or missing accounts return 404") ||
			!strings.Contains(get.Description, "A visible account without mail:read for this credential returns 403") {
			t.Fatalf("account addresses description lacks the two observed refusals: %q", get.Description)
		}
	})
}
