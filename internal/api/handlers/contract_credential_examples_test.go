package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

const appPasswordContractPath = "/api/v1/accounts/{id}/credentials"

func TestAppPasswordCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, appPasswordContractPath, "createAppPassword")
	want := map[string][]string{
		"request": {"created", "name_control", "name_empty", "name_missing", "name_too_long"},
		"201":     {"created"},
		"403":     {"recent_two_factor_required", "two_factor_required"},
		"422":     {"name_control", "name_empty", "name_missing", "name_too_long"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("createAppPassword pairs = %v, want %v", got, want)
	}
	wantRequests := map[string]interface{}{
		"created":       map[string]interface{}{"name": "iPhone Mail"},
		"name_missing":  map[string]interface{}{},
		"name_empty":    map[string]interface{}{"name": ""},
		"name_too_long": map[string]interface{}{"name": strings.Repeat("x", 101)},
		"name_control":  map[string]interface{}{"name": "Mail\nClient"},
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("createAppPassword requests = %s, want %s", mustJSON(requests), mustJSON(wantRequests))
	}
}

// Run every documented pair on an account the session manages. In the
// contract run the hook supplies its current password; everything else comes
// from the named request. Only the success may create a credential.
func TestAppPasswordCreate_DocumentedPairs(t *testing.T) {
	requests, responses := postExamples(t, appPasswordContractPath, "createAppPassword")
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewCredentialHandler(db)
	id := uintToStr(fx.user.ID)
	path := "/api/v1/accounts/" + id + "/credentials"
	for _, name := range names(requests) {
		t.Run(name, func(t *testing.T) {
			body, ok := requests[name].(map[string]interface{})
			if !ok {
				t.Fatalf("%s is not an object", name)
			}
			body["password"] = accountsPassword
			var before, after int64
			count := func(n *int64) {
				t.Helper()
				if err := db.Model(&models.Credential{}).Where("account_id = ?", fx.user.ID).Count(n).Error; err != nil {
					t.Fatal(err)
				}
			}
			count(&before)
			rr := httptest.NewRecorder()
			credentialRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, path, fx.user.ID, map[string]string{"id": id}, string(mustJSON(body))))
			count(&after)
			if name == "created" {
				if rr.Code != http.StatusCreated || after != before+1 {
					t.Fatalf("created: status %d, credentials %d -> %d: %s", rr.Code, before, after, rr.Body)
				}
				var created struct {
					Data createdAppPassword `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				if created.Data.Name != body["name"] || !strings.HasPrefix(created.Data.Password, "rm_") {
					t.Fatalf("created response: %s", rr.Body)
				}
				return
			}
			if rr.Code != http.StatusUnprocessableEntity || after != before {
				t.Fatalf("%s: status %d, credentials %d -> %d: %s", name, rr.Code, before, after, rr.Body)
			}
			var got interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, responses["422"][name]) {
				t.Errorf("%s answered %s, want %s", name, rr.Body, mustJSON(responses["422"][name]))
			}
		})
	}
}
