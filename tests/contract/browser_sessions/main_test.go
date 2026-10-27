package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
)

func cookies(expired bool) []*http.Cookie {
	value, age := "contract-secret", 60
	if expired {
		value, age = "", -1
	}
	return []*http.Cookie{
		{Name: auth.AccessCookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: age},
		{Name: auth.RefreshCookieName, Value: value, Path: "/api/v1/auth", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: age},
		{Name: auth.CSRFCookieName, Value: value, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: age},
	}
}

func TestBrowserCookieAttributes(t *testing.T) {
	for _, expired := range []bool{false, true} {
		if err := sessionCookies(cookies(expired), expired); err != nil {
			t.Fatal(err)
		}
		for name, mutate := range map[string]func([]*http.Cookie){
			"missing":          func(c []*http.Cookie) { c[0].Name = "other" },
			"secure":           func(c []*http.Cookie) { c[0].Secure = false },
			"access readable":  func(c []*http.Cookie) { c[0].HttpOnly = false },
			"refresh readable": func(c []*http.Cookie) { c[1].HttpOnly = false },
			"csrf unreadable":  func(c []*http.Cookie) { c[2].HttpOnly = true },
			"same site":        func(c []*http.Cookie) { c[1].SameSite = http.SameSiteLaxMode },
			"refresh path":     func(c []*http.Cookie) { c[1].Path = "/" },
			"domain":           func(c []*http.Cookie) { c[0].Domain = "restmail.test" },
			"lifetime":         func(c []*http.Cookie) { c[0].MaxAge = 0 },
			"duplicate":        func(c []*http.Cookie) { c[1] = c[0] },
		} {
			t.Run(fmt.Sprintf("%t/%s", expired, name), func(t *testing.T) {
				candidate := cookies(expired)
				mutate(candidate)
				err := sessionCookies(candidate, expired)
				if err == nil {
					t.Fatal("broken cookie accepted")
				}
				if strings.Contains(err.Error(), "contract-secret") {
					t.Fatal("error leaked cookie value")
				}
			})
		}
	}
}

func browserFixture(t *testing.T, csrfGuard, secure bool, csrfFault ...string) *httptest.Server {
	t.Helper()
	revoked := false
	mux := http.NewServeMux()
	set := func(w http.ResponseWriter, expired bool) {
		for _, c := range cookies(expired) {
			c.Secure = secure
			http.SetCookie(w, c)
		}
	}
	answer := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var request map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["client"] != nil {
			t.Error("browser login requested native credentials")
		}
		set(w, false)
		answer(w, 200, `{"data":{"user":{"id":7}}}`)
	})
	mux.HandleFunc("POST /api/v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.RefreshCookieName); err != nil || revoked || c.Value == "" || r.Header.Get(auth.CSRFHeaderName) == "" {
			answer(w, 401, `{}`)
			return
		}
		for _, cookie := range cookies(false) {
			cookie.Value = "rotated-" + cookie.Value
			cookie.Secure = secure
			http.SetCookie(w, cookie)
		}
		answer(w, 200, `{"data":{"user":{"id":7}}}`)
	})
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		revoked = true
		set(w, true)
		w.WriteHeader(204)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.AccessCookieName); err != nil || c.Value == "" || revoked {
			answer(w, 401, `{"error":{"code":"unauthorized"}}`)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("browser proof sent a bearer credential")
		}
		if csrfGuard && r.Method != http.MethodGet {
			cookie, err := r.Cookie(auth.CSRFCookieName)
			header := r.Header.Get(auth.CSRFHeaderName)
			invalid := header == "" || err != nil || cookie.Value != header
			if len(csrfFault) > 0 {
				switch csrfFault[0] {
				case "presence only":
					invalid = header == ""
				case "missing cookie allowed":
					invalid = header == "" || (err == nil && cookie.Value != header)
				}
			}
			if invalid {
				answer(w, 403, `{"error":{"code":"csrf_failed"}}`)
				return
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/accounts/7/folders" {
			answer(w, 422, `{"error":{"code":"validation_failed"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/events") && !strings.Contains(r.URL.Path, "invalid") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			return
		}
		answer(w, 200, `{"data":[]}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func fixtureDescription() description {
	security := []map[string][]string{{"CookieAuth": nil}}
	return description{Security: security, Paths: map[string]map[string]operation{
		"/api/v1/accounts/{id}/folders": {
			"get": {OperationID: "listFolders"}, "post": {OperationID: "createFolder"},
		},
		"/api/v1/messages/{id}": {
			"get": {OperationID: "getMessage"}, "patch": {OperationID: "updateMessage"}, "delete": {OperationID: "deleteMessage"},
		},
		"/api/v1/accounts/{id}/events": {"get": {OperationID: "accountEvents"}},
		"/public":                      {"post": {OperationID: "public", Security: []map[string][]string{}}},
	}}
}

func TestBrowserSessionProof(t *testing.T) {
	server := browserFixture(t, true, true)
	result, err := prove(server.URL, fixtureDescription(), principal{Address: "admin@restmail.test", Password: "fixture"})
	if err != nil || !result.Passed {
		t.Fatalf("proof failed: %v", err)
	}
	seen := map[string]bool{}
	variants := map[string]map[string]bool{}
	for _, c := range result.Checks {
		seen[c.Operation] = true
		if variants[c.Operation] == nil {
			variants[c.Operation] = map[string]bool{}
		}
		variants[c.Operation][c.Principal] = true
		if c.Operation == "public" {
			t.Fatal("public operation classified as cookie-protected")
		}
	}
	for _, operation := range []string{"createFolder", "updateMessage", "deleteMessage"} {
		for _, variant := range []csrfVariant{csrfMissing, csrfMismatched, csrfCookieMissing} {
			if !variants[operation][string(variant)] {
				t.Errorf("missing %s receipt for %s", variant, operation)
			}
		}
	}
	for _, operation := range []string{"createFolder", "updateMessage", "deleteMessage", "account events", "refresh", "logout", "revoked refresh"} {
		if !seen[operation] {
			t.Errorf("missing receipt for %s", operation)
		}
	}
	bytes, err := json.Marshal(result)
	if err != nil || strings.Contains(string(bytes), "contract-secret") || strings.Contains(string(bytes), "fixture") {
		t.Fatal("receipt contains a credential")
	}
}

func TestBrowserSessionFaultsFailTheGate(t *testing.T) {
	for _, fault := range []struct {
		name         string
		csrf, secure bool
	}{
		{"missing CSRF guard", false, true}, {"insecure cookie", true, false},
	} {
		t.Run(fault.name, func(t *testing.T) {
			server := browserFixture(t, fault.csrf, fault.secure)
			result, err := prove(server.URL, fixtureDescription(), principal{Address: "admin@restmail.test", Password: "fixture"})
			if err == nil || result.Passed {
				t.Fatal("fault was accepted")
			}
			t.Logf("fault refused: %v", err)
		})
	}
}

func TestBrowserSessionPartialCSRFFaultsFailTheGate(t *testing.T) {
	for _, fault := range []string{"presence only", "missing cookie allowed"} {
		t.Run(fault, func(t *testing.T) {
			server := browserFixture(t, true, true, fault)
			result, err := prove(server.URL, fixtureDescription(), principal{Address: "admin@restmail.test", Password: "fixture"})
			if err == nil || result.Passed {
				t.Fatal("partial CSRF bypass was accepted")
			}
			if !strings.Contains(err.Error(), "createFolder") {
				t.Fatalf("fault failed outside the operation probe: %v", err)
			}
		})
	}
}

func TestBrowserSessionRequiresDisposableFixture(t *testing.T) {
	if _, err := prove("http://unused", fixtureDescription(), principal{Address: "admin@example.com", Password: "secret"}); err == nil {
		t.Fatal("non-fixture account accepted")
	}
}
