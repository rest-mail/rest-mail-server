package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
)

func TestAuth_ExamplePairs(t *testing.T) {
	for _, tc := range []struct {
		path, operation string
		pairs           map[string][]string
	}{
		{"/api/v1/auth/login", "login", map[string][]string{
			"200": {"signed_in"}, "401": {"wrong_password"},
			"422": {"email_missing", "password_missing", "client_invalid"},
		}},
		{"/api/v1/auth/refresh", "refreshToken", map[string][]string{
			"200": {"refreshed"}, "401": {"invalid_token", "token_missing"},
		}},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			requests, responses := postExamples(t, tc.path, tc.operation)
			seen := map[string]bool{}
			for status, names := range tc.pairs {
				if len(responses[status]) != len(names) {
					t.Fatalf("%s examples = %v, want %v", status, responses[status], names)
				}
				for _, name := range names {
					if _, ok := responses[status][name]; !ok {
						t.Errorf("%s missing response %s", status, name)
					}
					if _, ok := requests[name]; !ok || seen[name] {
						t.Errorf("%s must pair with exactly one request", name)
					}
					seen[name] = true
				}
			}
			if len(requests) != len(seen) {
				t.Errorf("unpaired requests: %v", requests)
			}
		})
	}
}

// Execute the requests the spec pairs with each refusal, checking the complete
// body and that no cookie or successful session is returned.
func authExampleResponse(t *testing.T, handler http.HandlerFunc, path string, body interface{}, status int, want interface{}) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(mustJSON(body)))
	req.Header.Set("Content-Type", "application/json")
	handler(rr, req)
	if rr.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", mustJSON(body), rr.Code, status, rr.Body)
	}
	if want != nil {
		var got interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %s, want %s", rr.Body, mustJSON(want))
		}
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Errorf("native request returned cookies: %v", rr.Result().Cookies())
	}
	return rr
}

func TestAuthLogin_DocumentedValidationExamples(t *testing.T) {
	path := "/api/v1/auth/login"
	requests, responses := postExamples(t, path, "login")
	// Nil DB proves validation is refused before account lookup.
	h := &AuthHandler{}
	for name, want := range responses["422"] {
		t.Run(name, func(t *testing.T) {
			authExampleResponse(t, h.Login, path, requests[name], http.StatusUnprocessableEntity, want)
		})
	}
}

func TestAuthLogin_DocumentedCredentialExamples(t *testing.T) {
	path := "/api/v1/auth/login"
	requests, responses := postExamples(t, path, "login")
	db := openLoginTestDB(t)
	valid := requests["signed_in"].(map[string]interface{})
	domain := models.Domain{Name: "restmail.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	account := models.Mailbox{DomainID: domain.ID, LocalPart: "admin", Address: valid["email"].(string), Active: true}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Where("account_id = ?", account.ID).Delete(&models.RefreshToken{})
		db.Where("account_id = ?", account.ID).Delete(&models.Credential{})
		db.Delete(&account)
		db.Delete(&domain)
	})
	hash, err := hashPassword(valid["password"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.NewCredentialRepository(db).SetPassword(account.ID, hash, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	h := NewAuthHandler(db, auth.NewJWTService("contract-auth-secret", 15*time.Minute, 7*24*time.Hour), "", nil)
	for name, want := range responses["401"] {
		t.Run(name, func(t *testing.T) {
			authExampleResponse(t, h.Login, path, requests[name], http.StatusUnauthorized, want)
		})
	}
	var count int64
	if err := db.Model(&models.RefreshToken{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("refused login minted sessions: count=%d, err=%v", count, err)
	}
	var unchanged models.Mailbox
	if err := db.First(&unchanged, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.LastLoginAt != nil {
		t.Fatal("refused login updated last_login_at")
	}
	// The same stored credential accepts the success request; the refusal was
	// a wrong password, not a missing account or broken fixture.
	rr := authExampleResponse(t, h.Login, path, valid, http.StatusOK, nil)
	native := decodeNative(t, rr)
	if native.Data.AccessToken == "" || native.Data.RefreshToken == "" {
		t.Fatalf("successful login returned no tokens: %s", rr.Body)
	}
	if err := db.Model(&models.RefreshToken{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("successful login ledger: count=%d, err=%v", count, err)
	}
}

func TestAuthRefresh_DocumentedExamples(t *testing.T) {
	path := "/api/v1/auth/refresh"
	requests, responses := postExamples(t, path, "refreshToken")
	jwt := auth.NewJWTService("contract-refresh-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)
	pair, err := jwt.GenerateSession(42, "admin@restmail.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, 42)
	before := *store.rows[pair.RefreshJTI]
	for name, want := range responses["401"] {
		t.Run(name, func(t *testing.T) {
			authExampleResponse(t, h.Refresh, path, requests[name], http.StatusUnauthorized, want)
			if len(store.rows) != 1 || !reflect.DeepEqual(*store.rows[pair.RefreshJTI], before) {
				t.Fatal("refused refresh changed the existing session")
			}
		})
	}
	// Materialize the runtime token placeholder, as the contract hook does.
	valid := requests["refreshed"].(map[string]interface{})
	if valid["refresh_token"] != "fixture-refresh-token" {
		t.Fatal("success example must use the hook's runtime token placeholder")
	}
	valid["refresh_token"] = pair.RefreshToken
	rr := authExampleResponse(t, h.Refresh, path, valid, http.StatusOK, nil)
	if store.statusOf(pair.RefreshJTI) != models.RefreshTokenRotated || decodeNative(t, rr).Data.RefreshToken == "" {
		t.Fatalf("success did not rotate the session: %s", rr.Body)
	}
}

// The optional refresh body also permits browser requests. The contract's
// empty-body probes need a cookie, while the same bodies without it stay 401.
func TestAuthRefresh_BrowserCoverageExamples(t *testing.T) {
	for _, body := range []string{`{}`, `{"refresh_token":""}`} {
		t.Run(body, func(t *testing.T) {
			jwt := auth.NewJWTService("contract-browser-refresh-secret", 15*time.Minute, 7*24*time.Hour)
			store := newFakeRefreshStore()
			h := newRefreshHandler(jwt, store)
			pair, err := jwt.GenerateSession(42, "admin@restmail.test", 0, []string{auth.AMRPassword}, false)
			if err != nil {
				t.Fatal(err)
			}
			seedActive(store, pair, 42)
			before := *store.rows[pair.RefreshJTI]
			authExampleResponse(t, h.Refresh, "/api/v1/auth/refresh", parseShapeJSON(t, body),
				http.StatusUnauthorized, parseShapeJSON(t, `{"error":{"code":"unauthorized","message":"No refresh token"}}`))
			if !reflect.DeepEqual(*store.rows[pair.RefreshJTI], before) {
				t.Fatal("missing-cookie refusal consumed the browser session")
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewBufferString(body))
			req.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: pair.RefreshToken})
			rr := httptest.NewRecorder()
			h.Refresh(rr, req)
			if rr.Code != http.StatusOK || refreshCookieValue(rr) == "" || store.statusOf(pair.RefreshJTI) != models.RefreshTokenRotated {
				t.Fatalf("browser refresh failed: %d %s", rr.Code, rr.Body)
			}
		})
	}
}
