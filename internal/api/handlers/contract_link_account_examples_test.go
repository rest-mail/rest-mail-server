package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

func TestLinkAccount_DocumentedRefusals(t *testing.T) {
	// A valid session reaches validation, but none of the refused examples
	// needs a database, password check or grant write.
	documentedRefusals(t, "/api/v1/accounts/link", "linkAccount", signedIn(NewAccountHandler(nil, nil, "").LinkAccount))
}

func TestLinkAccount_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/accounts/link", "linkAccount")
	want := map[string][]string{
		"request": {"address_blank", "address_missing", "linked", "password_empty", "password_missing"},
		"201":     {"linked"},
		"422":     {"address_blank", "address_missing", "password_empty", "password_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linkAccount's named examples = %v, want %v", got, want)
	}
	linked := map[string]interface{}{"address": "eve@restmail.test", "password": "supplied-by-contract-hook"}
	if !reflect.DeepEqual(requests["linked"], linked) {
		t.Fatalf("linked = %s, want %s", mustJSON(requests["linked"]), mustJSON(linked))
	}
	for _, tc := range []struct{ name, field, value string }{
		{"address_missing", "address", ""},
		{"password_missing", "password", ""},
		{"address_blank", "address", "   "},
		{"password_empty", "password", ""},
	} {
		body := map[string]interface{}{"address": linked["address"], "password": linked["password"]}
		if tc.name == "address_missing" || tc.name == "password_missing" {
			delete(body, tc.field)
		} else {
			body[tc.field] = tc.value
		}
		if !reflect.DeepEqual(requests[tc.name], body) {
			t.Errorf("%s = %s, want %s", tc.name, mustJSON(requests[tc.name]), mustJSON(body))
		}
	}
}

func TestLinkAccount_RefusalsStillRequireSession(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/accounts/link", "linkAccount")
	for name := range responses["422"] {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/link", bytes.NewReader(mustJSON(requests[name])))
		NewAccountHandler(nil, nil, "").LinkAccount(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401: %s", name, rr.Code, rr.Body)
		}
	}
}

// Like the contract hook, this supplies the fixture password without filling
// in a missing field. The address belongs to this test's isolated fixture.
func TestLinkAccount_DocumentedLifecycle(t *testing.T) {
	db := openAccountsTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAccountsFixture(t, tx)
	h := NewAccountHandler(tx, grants.NewCache(tx, grants.CacheTTL), "")
	requests, responses := postExamples(t, "/api/v1/accounts/link", "linkAccount")
	link := func(name string) *httptest.ResponseRecorder {
		t.Helper()
		body := map[string]interface{}{}
		for field, value := range requests[name].(map[string]interface{}) {
			if field == "address" && value == "eve@restmail.test" {
				value = fx.admin.Address
			}
			if field == "password" && value == "supplied-by-contract-hook" {
				value = accountsPassword
			}
			body[field] = value
		}
		rr := httptest.NewRecorder()
		h.LinkAccount(rr, authzRequest(http.MethodPost, "/api/v1/accounts/link", fx.user.ID, nil, string(mustJSON(body))))
		return rr
	}
	if rr := link("linked"); rr.Code != http.StatusCreated {
		t.Fatalf("linked: status %d, body %s", rr.Code, rr.Body)
	} else if got := decodeAccount(t, rr); got.ID != fx.admin.ID || got.Address != fx.admin.Address || got.IsPrimary {
		t.Fatalf("linked account = %+v, want the fixture target", got)
	}
	countGrants := func() int64 {
		t.Helper()
		var count int64
		if err := tx.Model(&models.Grant{}).Where("holder_id = ? AND holder_kind = ?", fx.user.ID, models.GrantHolderAccount).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	if count := countGrants(); count != 1 {
		t.Fatalf("linked made %d grants, want one", count)
	}
	for name, want := range responses["422"] {
		t.Run(name, func(t *testing.T) {
			rr := link(name)
			if rr.Code != http.StatusUnprocessableEntity || !reflect.DeepEqual(parseShapeJSON(t, rr.Body.String()), want) {
				t.Fatalf("%s: status %d, body %s; want 422 %s", name, rr.Code, rr.Body, mustJSON(want))
			}
			if count := countGrants(); count != 1 {
				t.Errorf("%s left %d grants, want one", name, count)
			}
		})
	}
}
