package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	keyring "github.com/zalando/go-keyring"
)

func TestWailsAccountsNeverExposeStoredPasswords(t *testing.T) {
	keyring.MockInit()
	if keyring.Set("REST Mail", "imap-accounts-v1", `{"accounts":[{"type":"imap","id":4503599627370497,"host":"imap.test","address":"reader@example.test","username":"reader"}],"next_account":1}`) != nil || keyring.Set("REST Mail", "imap:4503599627370497", "wails-private-password") != nil {
		t.Fatal("seed keychain")
	}
	manager := newDesktopSession()
	defer manager.Close()
	service := &Accounts{manager: manager}
	allowed := map[string]bool{"AddIMAPAccount": true, "RemoveIMAPAccount": true, "ListAccounts": true}
	bound := reflect.TypeOf(service)
	for index := 0; index < bound.NumMethod(); index++ {
		if !allowed[bound.Method(index).Name] {
			t.Fatalf("unexpected Wails account binding: %s", bound.Method(index).Name)
		}
	}
	result := service.ListAccounts()
	encoded, _ := json.Marshal(result)
	if len(result) != 1 || strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "wails-private") {
		t.Fatal("stored password reached Wails result")
	}
	if _, err := service.AddIMAPAccount(imapmail.AccountInput{Password: "failed-input-password"}); err == nil || strings.Contains(err.Error(), "failed-input-password") {
		t.Fatal("unsafe account creation error")
	}
	if err := service.RemoveIMAPAccount(imapmail.IDBase + 1); err != nil {
		t.Fatal(err)
	}
	if len(service.ListAccounts()) != 0 {
		t.Fatal("removed account exposed to Wails")
	}
	if _, err := keyring.Get("REST Mail", "imap:4503599627370497"); err != keyring.ErrNotFound {
		t.Fatal("Wails removal retained password")
	}
}

func TestNamedTestCopyDoesNotRestoreOwnerAccounts(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set("REST Mail", "active-account", `{"server":"https://owner.test","email":"owner@owner.test"}`); err != nil {
		t.Fatal(err)
	}
	t.Setenv(isolatedInstanceEnv, "themes")
	manager := newDesktopSession()
	defer manager.Close()
	if err := manager.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if manager.Status().SignedIn || len(manager.ListAccounts()) != 0 {
		t.Fatal("test copy restored owner accounts")
	}
	if _, err := keyring.Get("REST Mail", "active-account"); err != keyring.ErrNotFound {
		t.Fatal("test copy did not start with an ephemeral credential store")
	}
}

func TestNamedTestCopySignInAndRestoreAvoidSystemKeyring(t *testing.T) {
	keyring.MockInitWithError(errors.New("system keyring must not be called"))
	t.Cleanup(keyring.MockInit)
	t.Setenv(isolatedInstanceEnv, "themes")
	manager := newDesktopSession()
	defer manager.Close()
	if err := manager.Restore(t.Context()); err != nil {
		t.Fatalf("empty test-copy restore reached system keyring: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/auth/login", "/api/v1/auth/refresh":
			_, _ = io.WriteString(writer, `{"data":{"access_token":"test-access","refresh_token":"test-refresh","expires_in":3600,"user":{"id":42,"email":"test@example.test"}}}`)
		default:
			_, _ = io.WriteString(writer, `{"data":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	if _, err := manager.SignIn(strings.Replace(server.URL, "127.0.0.1", "localhost", 1), "test@example.test", "test-password"); err != nil {
		t.Fatalf("test-copy sign-in reached system keyring: %v", err)
	}
	if !manager.Status().SignedIn {
		t.Fatal("test-copy sign-in did not create an ephemeral session")
	}
	restored := session.New()
	if err := restored.Restore(t.Context()); err != nil || !restored.Status().SignedIn {
		t.Fatalf("test-copy credential restore failed: %v", err)
	}
}
