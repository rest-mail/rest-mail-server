package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The typed owner answers write the bytes the maps and the local type they replaced wrote:
// the restmail CLI reads them.
func TestOwnerResponseJSONParity(t *testing.T) {
	at := time.Date(2026, 10, 24, 1, 2, 3, 456789, time.FixedZone("BST", 3600))
	stamp := at.UTC().Format(time.RFC3339)
	type oldStatus struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuiltAt   string `json:"built_at"`
		StartedAt string `json:"started_at"`
	}
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"challenge", map[string]string{"challenge": "c", "expires_at": stamp}, OwnerChallenge{Challenge: "c", ExpiresAt: wholeSecondUTC(at)}},
		{"token", map[string]string{"purpose": "reset-admin", "token": "t", "expires_at": stamp},
			OwnerToken{ExpiresAt: wholeSecondUTC(at), Purpose: "reset-admin", Token: "t"}},
		{"status", oldStatus{"0.22.0", "abc", "unknown", stamp},
			ServerStatus{Version: "0.22.0", Commit: "abc", BuiltAt: "unknown", StartedAt: wholeSecondUTC(at)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, err := json.Marshal(tc.old)
			if err != nil {
				t.Fatal(err)
			}
			current, err := json.Marshal(tc.current)
			if err != nil {
				t.Fatal(err)
			}
			if string(old) != string(current) {
				t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
			}
		})
	}
}

// What binding refuses, it refuses before the owner registry or a token is read: handlers
// with no database prove it. A wrong value of the right type is still the handler's.
func TestOwnerAndResetBindingBeforeDatabase(t *testing.T) {
	owner, auth := &OwnerHandler{}, &AuthHandler{}
	for _, tc := range []struct {
		route        http.HandlerFunc
		body, field  string
		wantContains string
	}{
		{ownerRoute(owner, "token"), `{"purpose":5}`, "purpose", "must be a string"},
		{ownerRoute(owner, "token"), `{"purpose":"reset-admin","address":"a@b.test","lifetime_seconds":"long"}`, "lifetime_seconds", "must be an integer"},
		{ownerRoute(owner, "token"), `{"purpose":"reset-admin","scope":"all"}`, "scope", "not allowed"},
		{ownerRoute(owner, "token"), `{"purpose":"setup"}`, "purpose", `must be \"reset-admin\"`},
		{ownerRoute(owner, "token"), `{"purpose":"reset-admin"}`, "address", "required for reset-admin"},
		{ownerRoute(owner, "token"), `{"purpose":"reset-admin","address":"a@b.test","lifetime_seconds":86401}`, "lifetime_seconds", "must be between 0"},
		{ownerRoute(owner, "token"), `{"purpose":"reset-admin","address":"a@b.test","signature":"not base64!"}`, "signature", "must be base64"},
		{ownerRoute(owner, "status"), `{"signature":["x"]}`, "signature", "must be a string"},
		{ownerRoute(owner, "status"), `{"signature":"not base64!"}`, "signature", "must be base64"},
		{resetHandler(auth), `{"token":5,"password":"long enough password"}`, "token", "must be a string"},
		{resetHandler(auth), `{"token":"t","password":"long enough password","client":"web"}`, "client", `must be \"native\" or omitted`},
	} {
		t.Run(tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			tc.route(rr, req)
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":"`) ||
				!strings.Contains(rr.Body.String(), tc.wantContains) {
				t.Fatalf("want 422 on %s (%s), got %d %s", tc.field, tc.wantContains, rr.Code, rr.Body)
			}
		})
	}
	for name, route := range map[string]http.HandlerFunc{"token": ownerRoute(owner, "token"), "status": ownerRoute(owner, "status"), "reset": resetHandler(auth)} {
		rr := httptest.NewRecorder()
		route(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"challenge":`)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: a body that is not JSON = %d %s, want 400", name, rr.Code, rr.Body)
		}
	}
}
