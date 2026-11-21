package mailbackend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiproxy"
	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	keyring "github.com/zalando/go-keyring"
)

func testBackend(t *testing.T) *Manager {
	t.Helper()
	keyring.MockInit()
	encoded := `{"accounts":[{"type":"imap","id":4503599627370497,"host":"imap.test","port":993,"username":"reader","smtp_host":"smtp.test","smtp_port":587,"address":"reader@example.test","imap_security":"tls","smtp_security":"starttls"}],"next_account":1}`
	if keyring.Set("REST Mail", "imap-accounts-v1", encoded) != nil || keyring.Set("REST Mail", "imap:4503599627370497", "backend-password<&>secret") != nil {
		t.Fatal("seed keychain")
	}
	store, err := imapmail.New()
	if err != nil {
		t.Fatal(err)
	}
	manager := New(session.New(), store)
	t.Cleanup(manager.Close)
	return manager
}

func TestMixedRESTAndIMAPAccountsMergeWithoutIDCollisions(t *testing.T) {
	manager := testBackend(t)
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/auth/login":
			_, _ = io.WriteString(writer, `{"data":{"access_token":"rest-access-secret","refresh_token":"rest-refresh-secret","expires_in":3600,"user":{"id":42,"email":"rest@example.test"}}}`)
		case "/api/v1/accounts":
			switch mode.Load() {
			case 1:
				_, _ = io.WriteString(writer, `{"data":[{"id":4503599627370497,"address":"collision@example.test"}]}`)
			case 2:
				_, _ = io.WriteString(writer, "invalid JSON")
			case 3:
				writer.WriteHeader(503)
			default:
				_, _ = io.WriteString(writer, `{"data":[{"id":42,"address":"rest@example.test","is_primary":true}]}`)
			}
		default:
			_, _ = io.WriteString(writer, `{"data":[]}`)
		}
	}))
	defer server.Close()
	manager.SetOnChange(func(_ session.SessionStatus) {})
	if _, err := manager.SignIn(strings.Replace(server.URL, "127.0.0.1", "localhost", 1), "rest@example.test", "password"); err != nil {
		t.Fatal(err)
	}
	if manager.Status().AccountID != 42 || len(manager.ListAccounts()) != 2 {
		t.Fatal("REST identity not retained alongside IMAP")
	}
	if len(manager.SensitiveValues()) < 3 {
		t.Fatal("combined redaction lost an account credential")
	}
	response, err := manager.Do(t.Context(), "GET", "/api/v1/accounts", nil)
	if err != nil {
		t.Fatal(err)
	}
	var accounts struct {
		Data []struct {
			ID      uint `json:"id"`
			Primary bool `json:"is_primary"`
		} `json:"data"`
	}
	if json.NewDecoder(response.Body).Decode(&accounts) != nil {
		t.Fatal("invalid merged account response")
	}
	response.Body.Close()
	if len(accounts.Data) != 2 || !accounts.Data[0].Primary || accounts.Data[1].Primary {
		t.Fatal("merged account selection wrong")
	}
	for _, state := range []int32{1, 2, 3} {
		mode.Store(state)
		if response, err := manager.Do(t.Context(), "GET", "/api/v1/accounts", nil); err == nil {
			response.Body.Close()
			t.Fatalf("unsafe upstream state %d accepted", state)
		}
	}
	mode.Store(0)
	response, err = manager.Do(t.Context(), "GET", "/api/v1/messages/scheduled", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = manager.Do(t.Context(), "DELETE", "/api/v1/accounts/4503599627370497", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(manager.ListAccounts()) != 1 {
		t.Fatal("local DELETE route did not remove account")
	}
	if _, err := manager.Do(t.Context(), "GET", "://", nil); err == nil {
		t.Fatal("invalid request URL accepted")
	}
}

func TestBackendMissingKeychainAndInvalidAddFailSafely(t *testing.T) {
	manager := New(session.New(), nil)
	if _, err := manager.AddIMAPAccount(t.Context(), imapmail.AccountInput{}); err == nil {
		t.Fatal("missing keychain accepted account")
	}
	if manager.RemoveIMAPAccount(imapmail.IDBase+1) == nil {
		t.Fatal("missing keychain accepted removal")
	}
	if manager.Status().SignedIn || len(manager.ListAccounts()) != 0 || len(manager.SensitiveValues()) != 0 {
		t.Fatal("empty backend exposed account")
	}
	manager.Close()
	manager = testBackend(t)
	if _, err := manager.AddIMAPAccount(t.Context(), imapmail.AccountInput{Password: "failed-add-secret"}); err == nil {
		t.Fatal("invalid account accepted")
	}
	if strings.Contains(string(manager.Redact([]byte("failed-add-secret"))), "failed-add-secret") {
		t.Fatal("failed-add password was not registered for redaction")
	}
}

func TestNativeBackendAccountListAndSignedOutBoundary(t *testing.T) {
	manager := testBackend(t)
	status := manager.Status()
	if !status.SignedIn || status.AccountID != imapmail.IDBase+1 || status.Email != "reader@example.test" {
		t.Fatalf("status=%+v", status)
	}
	response, err := manager.Do(t.Context(), "GET", "/api/v1/accounts", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || !strings.Contains(string(data), "4503599627370497") || strings.Contains(string(data), "password") {
		t.Fatalf("account response=%s err=%v", data, err)
	}
	unsupported, err := manager.Do(t.Context(), "GET", "/api/v1/messages/scheduled", nil)
	if err != nil {
		t.Fatal(err)
	}
	unsupported.Body.Close()
	if unsupported.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unsupported feature signed out local account: %d", unsupported.StatusCode)
	}
	if len(manager.ListAccounts()) != 1 {
		t.Fatal("binding list lost local account")
	}
	if err := manager.SignOut(); err != nil {
		t.Fatal(err)
	}
	if manager.Status().SignedIn {
		t.Fatal("sign out did not lock local accounts")
	}
	response, err = manager.Do(t.Context(), "GET", "/api/v1/accounts", nil)
	if err != session.ErrSignedOut {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("signed-out transport error=%v", err)
	}
}

func TestNativeBackendProxyAndRemovalNeverExposePassword(t *testing.T) {
	manager := testBackend(t)
	handler := apiproxy.New(manager)(http.NotFoundHandler())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/accounts", nil))
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "password") || strings.Contains(recorder.Body.String(), "backend-password") {
		t.Fatalf("unsafe Fetch-visible response: %d %s", recorder.Code, recorder.Body.String())
	}
	var notifications []session.SessionStatus
	manager.SetOnChange(func(status session.SessionStatus) { notifications = append(notifications, status) })
	if err := manager.RemoveIMAPAccount(imapmail.IDBase + 1); err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 || notifications[0].SignedIn {
		t.Fatal("removal did not publish signed-out identity")
	}
	if _, err := keyring.Get("REST Mail", "imap:4503599627370497"); err != keyring.ErrNotFound {
		t.Fatal("removal retained keychain password")
	}
	if strings.Contains(string(manager.Redact([]byte("backend-password<&>secret"))), "backend-password") {
		t.Fatal("removed password no longer redacted")
	}
	encoded, _ := json.Marshal(manager.ListAccounts())
	if string(encoded) != "[]" {
		t.Fatalf("removed account returned to JS: %s", encoded)
	}
}
