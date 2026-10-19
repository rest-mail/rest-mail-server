package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// createAccount asks for a session before it reads the body, so its documented
// refusals are sent signed in as an ordinary account. The handler still has
// no database: the refusal comes before the session's grants are loaded.
func TestAccountCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/accounts", "createAccount", signedIn(NewAccountHandler(nil, nil, "").CreateAccount))
}

// The refused example is refused for the address it leaves out: given the
// address of the example that creates an account, the same body passes
// validation and goes on to load the session's grants, which needs the
// database the handler does not have.
func TestAccountCreate_AddressMissingIsTheRefusal(t *testing.T) {
	requests, _ := postExamples(t, "/api/v1/accounts", "createAccount")
	created, ok := requests["created"].(map[string]interface{})
	if !ok {
		t.Fatalf("createAccount has no created request example: %v", requests)
	}
	refused, ok := requests["address_missing"].(map[string]interface{})
	if !ok {
		t.Fatalf("createAccount has no address_missing request example: %v", requests)
	}
	if _, ok := refused["address"]; ok {
		t.Fatalf("address_missing has an address: %v", refused)
	}

	refused["address"] = created["address"]
	body, err := json.Marshal(refused)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	defer func() {
		if recover() == nil {
			t.Errorf("%s was answered %d without reaching the database: %s", body, rr.Code, rr.Body)
		}
	}()
	signedIn(NewAccountHandler(nil, nil, "").CreateAccount)(rr, req)
}

func signedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, withClaims(r, mailboxClaims(1, "admin@restmail.test")))
	}
}
