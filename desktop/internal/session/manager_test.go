package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	keyring "github.com/zalando/go-keyring"
)

type authFixture struct {
	server    string
	totp      bool
	refreshes int
	mu        sync.Mutex
}

func newAuthFixture(t *testing.T, totp bool) *authFixture {
	t.Helper()
	keyring.MockInit()
	fixture := &authFixture{totp: totp}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		var body map[string]string
		if request.Method == http.MethodPost {
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		switch request.URL.Path {
		case "/api/v1/auth/login":
			if body["client"] != "native" || body["password"] != "password" {
				t.Error("login must use native mode and the supplied password")
			}
			if fixture.totp && body["totp_code"] != "123456" {
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(writer, `{"error":{"code":"totp_required"}}`)
				return
			}
		case "/api/v1/auth/refresh":
			if body["refresh_token"] != fmt.Sprintf("refresh-secret-%d", fixture.refreshes) {
				t.Errorf("wrong refresh credential: %q", body["refresh_token"])
			}
			fixture.refreshes++
		case "/api/v1/auth/logout":
			if body["refresh_token"] == "" {
				t.Error("logout must revoke the refresh credential")
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		default:
			if request.Header.Get("Authorization") != fmt.Sprintf("Bearer access-secret-%d", fixture.refreshes) {
				t.Error("missing current bearer token")
			}
			_, _ = io.WriteString(writer, `{"data":[]}`)
			return
		}
		_, _ = fmt.Fprintf(writer, `{"data":{"access_token":"access-secret-%d","refresh_token":"refresh-secret-%d","expires_in":3600,"user":{"id":42,"email":"admin@example.test"}}}`, fixture.refreshes, fixture.refreshes)
	}))
	t.Cleanup(server.Close)
	fixture.server = strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	return fixture
}

func signIn(t *testing.T, manager *Manager, fixture *authFixture) SignInResult {
	t.Helper()
	result, err := manager.SignIn(fixture.server, "admin@example.test", "password")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSignInWithoutTOTPStoresOnlyRefreshCredential(t *testing.T) {
	fixture := newAuthFixture(t, false)
	manager := New()
	var statuses []SessionStatus
	manager.SetOnChange(func(status SessionStatus) { statuses = append(statuses, status) })
	if result := signIn(t, manager, fixture); result.NeedsTOTP {
		t.Fatal("unexpected second factor")
	}
	status := manager.Status()
	if !status.SignedIn || status.AccountID != 42 || status.Server != fixture.server || len(statuses) != 1 {
		t.Fatalf("unexpected status/event: %+v / %+v", status, statuses)
	}
	stored, err := keyring.Get(KeychainService, fixture.server+"\nadmin@example.test")
	if err != nil || !strings.Contains(stored, "refresh-secret-0") || strings.Contains(stored, "access-secret") || strings.Contains(stored, "password") {
		t.Fatalf("wrong persisted credential: %s, %v", stored, err)
	}
}

func TestSignInWithTOTP(t *testing.T) {
	fixture := newAuthFixture(t, true)
	manager := New()
	if result := signIn(t, manager, fixture); !result.NeedsTOTP || manager.Status().SignedIn {
		t.Fatal("challenge must not create a session")
	}
	if _, err := keyring.Get(KeychainService, activeAccount); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("challenge must not persist credentials")
	}
	if err := manager.SubmitTOTP("wrong"); err == nil {
		t.Fatal("wrong second factor accepted")
	}
	if err := manager.SubmitTOTP("123456"); err != nil || !manager.Status().SignedIn {
		t.Fatalf("second factor failed: %v", err)
	}
	if manager.pending.password != "" {
		t.Fatal("password retained after login")
	}
}

func TestRefreshBeforeExpiryRotatesKeychain(t *testing.T) {
	fixture := newAuthFixture(t, false)
	manager := New()
	signIn(t, manager, fixture)
	manager.expires = time.Now().Add(-time.Second)
	response, err := manager.Do(context.Background(), http.MethodGet, "/api/v1/accounts/42/folders", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	stored, err := keyring.Get(KeychainService, fixture.server+"\nadmin@example.test")
	if err != nil || !strings.Contains(stored, "refresh-secret-1") || manager.access != "access-secret-1" {
		t.Fatalf("refresh did not rotate credential: %s / %v", stored, err)
	}
	for _, secret := range []string{"access-secret-0", "access-secret-1", "refresh-secret-0", "refresh-secret-1"} {
		if string(manager.Redact([]byte(secret))) != "[redacted]" {
			t.Fatalf("previous/current secret not redacted: %s", secret)
		}
	}
	if len(manager.SensitiveValues()) != 4 {
		t.Fatal("Go redaction must include old and current credential pairs")
	}
}

func TestRestoreSessionAfterRestart(t *testing.T) {
	fixture := newAuthFixture(t, false)
	signIn(t, New(), fixture)
	restarted := New()
	if err := restarted.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := restarted.Status()
	if !status.SignedIn || status.AccountID != 42 || status.Email != "admin@example.test" || status.Server != fixture.server {
		t.Fatalf("wrong restored session: %+v", status)
	}
}

func TestSignOutWipesKeychainAndPendingPassword(t *testing.T) {
	fixture := newAuthFixture(t, false)
	manager := New()
	signIn(t, manager, fixture)
	if err := manager.SignOut(); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{fixture.server + "\nadmin@example.test", activeAccount} {
		if _, err := keyring.Get(KeychainService, account); !errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("credential still present for %s", account)
		}
	}
	if err := New().Restore(context.Background()); err != nil || manager.Status().SignedIn {
		t.Fatalf("signed-out session restored: %v", err)
	}
	if _, err := manager.Do(context.Background(), http.MethodGet, "/api/v1/accounts", nil); !errors.Is(err, ErrSignedOut) {
		t.Fatal("signed-out request was allowed")
	}
}

func TestValidateServerRequiresHTTPS(t *testing.T) {
	for _, server := range []string{"http://example.test", "http://127.0.0.1", "http://localhost.evil.test", "https://user:pass@example.test", "https://example.test/path", "https://example.test?q=x", "file:///etc/passwd", "//example.test"} {
		if _, err := ValidateServer(server); err == nil {
			t.Errorf("accepted unsafe server %s", server)
		}
	}
	for _, server := range []string{"https://example.test", "http://localhost:1234", "https://example.test/"} {
		if _, err := ValidateServer(server); err != nil {
			t.Errorf("rejected safe server %s: %v", server, err)
		}
	}
}

func TestUntrustedTLSCertificateIsRejected(t *testing.T) {
	keyring.MockInit()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS request reached server") }))
	defer server.Close()
	if _, err := New().SignIn(server.URL, "admin@example.test", "password"); err == nil {
		t.Fatal("accepted self-signed certificate")
	}
}

func TestKeychainFailureDoesNotSignIn(t *testing.T) {
	fixture := newAuthFixture(t, false)
	keyring.MockInitWithError(errors.New("keychain locked"))
	t.Cleanup(keyring.MockInit)
	manager := New()
	if _, err := manager.SignIn(fixture.server, "admin@example.test", "password"); err == nil || manager.Status().SignedIn {
		t.Fatal("keychain failure must fail closed")
	}
}

func TestBackgroundRefreshStopsOnCancellation(t *testing.T) {
	fixture := newAuthFixture(t, false)
	manager := New()
	signIn(t, manager, fixture)
	manager.expires = time.Now().Add(-time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { manager.Run(ctx); close(done) }()
	deadline := time.After(4 * time.Second)
	for {
		fixture.mu.Lock()
		refreshed := fixture.refreshes > 0
		fixture.mu.Unlock()
		if refreshed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background refresh did not happen")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh worker did not stop")
	}
}
