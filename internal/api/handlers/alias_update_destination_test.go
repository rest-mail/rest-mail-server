package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// aliasTables are the tables every alias scratch database starts with. The composite
// index matches the production migration, which creates it separately from the
// model's field tags.
var aliasTables = scratchStep{"alias-tables", func(db *gorm.DB) error {
	if err := db.AutoMigrate(&models.Domain{}, &models.Alias{}, &models.Mailbox{}); err != nil {
		return err
	}
	return db.Exec("CREATE UNIQUE INDEX idx_aliases_source_dest ON aliases(source_address, destination_address)").Error
}}

// openAliasUpdateScratchDB is a scratch database with the domain and alias tables, then setup.
func openAliasUpdateScratchDB(t *testing.T, setup ...any) *gorm.DB {
	t.Helper()
	return openScratchDB(t, "aliasupdate", append([]any{aliasTables}, setup...))
}

// patchAlias sends body to the alias update handler as PATCH /admin/aliases/{id}.
func patchAlias(h *AliasHandler, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/aliases/"+id, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	aliasRoutes(h).ServeHTTP(rr, req)
	return rr
}

// seedAlias stores a fresh alias to update, and returns it as stored.
func seedAlias(t *testing.T, db *gorm.DB, domain models.Domain, source string) models.Alias {
	t.Helper()
	alias := models.Alias{DomainID: domain.ID, SourceAddress: source, DestinationAddress: "admin@aliasupdate.test", Active: true}
	if err := db.Create(&alias).Error; err != nil {
		t.Fatal(err)
	}
	var stored models.Alias
	if err := db.First(&stored, alias.ID).Error; err != nil {
		t.Fatal(err)
	}
	return stored
}

func storedAlias(t *testing.T, db *gorm.DB, id uint) models.Alias {
	t.Helper()
	var stored models.Alias
	if err := db.First(&stored, id).Error; err != nil {
		t.Fatal(err)
	}
	return stored
}

func aliasCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.Alias{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// sameAlias compares the stored columns, ignoring the unloaded association.
func sameAlias(a, b models.Alias) bool {
	return a.ID == b.ID && a.DomainID == b.DomainID && a.SourceAddress == b.SourceAddress &&
		a.DestinationAddress == b.DestinationAddress && a.Active == b.Active && a.CreatedAt.Equal(b.CreatedAt)
}

// An explicitly supplied destination that is not a mail address is refused
// with the canonical 422 and nothing is written, not even the active flag sent
// beside it (#388): UpdateAliasRequest declares destination_address format:
// email, so an empty string is refused too.
func TestAliasUpdate_MalformedDestinationRefused(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	domain := models.Domain{Name: "aliasupdate.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewAliasHandler(db)
	want := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "validation_failed",
			"message": "Request body failed validation",
			"details": map[string]interface{}{"fields": map[string]interface{}{
				"destination_address": "must be a valid email address",
			}},
		},
	}
	for name, destination := range map[string]string{
		"bareword":        "not-an-email",
		"no local part":   "@aliasupdate.test",
		"no domain":       "admin@",
		"two addresses":   "admin@aliasupdate.test, eve@aliasupdate.test",
		"header injected": "admin@aliasupdate.test\r\nBcc: eve@aliasupdate.test",
		"only spaces":     "   ",
		"empty":           "",
	} {
		t.Run(name, func(t *testing.T) {
			before := seedAlias(t, db, domain, "sales-"+strings.ReplaceAll(name, " ", "-")+"@aliasupdate.test")
			count := aliasCount(t, db)
			body := fmt.Sprintf(`{"destination_address":%s,"active":false}`, mustJSON(destination))
			rr := patchAlias(h, fmt.Sprint(before.ID), body)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422; body %s", rr.Code, rr.Body)
			}
			var got interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", rr.Body, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("answered\n  %s\nwant\n  %s", rr.Body, mustJSON(want))
			}
			if after := storedAlias(t, db, before.ID); !sameAlias(after, before) {
				t.Errorf("stored alias changed by a refused update:\n  before %+v\n  after  %+v", before, after)
			}
			if n := aliasCount(t, db); n != count {
				t.Errorf("alias count = %d, want %d", n, count)
			}
		})
	}
}

// Well-formed destinations are stored exactly as sent; an omitted or null
// destination leaves the stored one alone, and active:false is still stored.
// Only declared fields are accepted by the registered request body.
func TestAliasUpdate_DestinationPersistence(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	domain := models.Domain{Name: "aliasupdate.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewAliasHandler(db)

	for _, tc := range []struct {
		name        string
		fields      string
		destination string // "" keeps the seeded destination
		active      bool
	}{
		{"ordinary", `"destination_address":"other@aliasupdate.test"`, "other@aliasupdate.test", true},
		{"plus tag", `"destination_address":"admin+alias@aliasupdate.test"`, "admin+alias@aliasupdate.test", true},
		{"quoted local part", `"destination_address":"\"admin team\"@aliasupdate.test"`, `"admin team"@aliasupdate.test`, true},
		{"utf-8 local part", `"destination_address":"jürgen@aliasupdate.test"`, "jürgen@aliasupdate.test", true},
		{"internationalized", `"destination_address":"admin@münchen.test"`, "admin@münchen.test", true},
		{"valid with active false", `"destination_address":"first.last@mail.aliasupdate.test","active":false`, "first.last@mail.aliasupdate.test", false},
		{"omitted with active false", `"active":false`, "", false},
		{"null with active false", `"destination_address":null,"active":false`, "", false},
		{"omitted", ``, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := seedAlias(t, db, domain, "sales-"+strings.ReplaceAll(tc.name, " ", "-")+"@aliasupdate.test")
			count := aliasCount(t, db)
			fields := tc.fields
			rr := patchAlias(h, fmt.Sprint(before.ID), "{"+fields+"}")
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d, want 200; body %s", rr.Code, rr.Body)
			}
			want := before
			want.Active = tc.active
			if tc.destination != "" {
				want.DestinationAddress = tc.destination
			}
			var response struct {
				Data models.Alias `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !sameAlias(response.Data, want) || response.Data.Domain.Name != domain.Name {
				t.Errorf("returned %s, want %+v in domain %s", rr.Body, want, domain.Name)
			}
			if after := storedAlias(t, db, before.ID); !sameAlias(after, want) {
				t.Errorf("stored %+v, want %+v", after, want)
			}
			if n := aliasCount(t, db); n != count {
				t.Errorf("alias count = %d, want %d", n, count)
			}
		})
	}
}

// The destination check runs after the path and the alias are resolved and
// the body decoded: a bad ID is 422, a missing alias still 404 and a
// body that is not JSON still 400, whatever destination comes with them.
func TestAliasUpdate_DestinationCheckPrecedence(t *testing.T) {
	malformed := `{"destination_address":"not-an-email"}`
	t.Run("invalid id before the database", func(t *testing.T) {
		rr := patchAlias(NewAliasHandler(nil), "abc", malformed)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"id":`) {
			t.Errorf("status %d body %s, want 422 with id validation", rr.Code, rr.Body)
		}
	})
	db := openAliasUpdateScratchDB(t)
	domain := models.Domain{Name: "aliasupdate.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewAliasHandler(db)
	t.Run("missing alias", func(t *testing.T) {
		rr := patchAlias(h, "987654", malformed)
		if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "Alias not found") {
			t.Errorf("status %d body %s, want 404 Alias not found", rr.Code, rr.Body)
		}
	})
	t.Run("body not JSON", func(t *testing.T) {
		before := seedAlias(t, db, domain, "sales@aliasupdate.test")
		rr := patchAlias(h, fmt.Sprint(before.ID), `{"destination_address":"not-an-email"`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Invalid request body") {
			t.Errorf("status %d body %s, want 400 Invalid request body", rr.Code, rr.Body)
		}
		if after := storedAlias(t, db, before.ID); !sameAlias(after, before) {
			t.Errorf("stored alias changed: before %+v after %+v", before, after)
		}
	})
}
