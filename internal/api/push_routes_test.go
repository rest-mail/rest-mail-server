package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

// devicesRequests are the three device routes, as the app calls them.
var devicesRequests = []struct{ method, path, body string }{
	{http.MethodGet, "/api/v1/devices", ""},
	{http.MethodPost, "/api/v1/devices", `{"platform":"apns","token":"` + strings.Repeat("ab", 32) + `"}`},
	{http.MethodDelete, "/api/v1/devices/1", ""},
}

func devicesStatus(t *testing.T, h http.Handler, method, path, body, token string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

// TestDevicesRoutesAre404WhenPushIsOff: with APNs not fully configured, all
// three device routes answer 404, signed in or not, which the app reads as
// "this server has no push".
func TestDevicesRoutesAre404WhenPushIsOff(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"nothing set": {},
		"no team id":  {"RESTMAIL_APNS_KEY_FILE": "/nonexistent/AuthKey.p8", "RESTMAIL_APNS_KEY_ID": "KEY1234567"},
		"no key file": {"RESTMAIL_APNS_KEY_ID": "KEY1234567", "RESTMAIL_APNS_TEAM_ID": "TEAM123456"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"RESTMAIL_APNS_KEY_FILE", "RESTMAIL_APNS_KEY_ID", "RESTMAIL_APNS_TEAM_ID", "RESTMAIL_APNS_TOPIC"} {
				t.Setenv(k, env[k])
			}
			if err := CheckPush(&config.Config{}); err != nil {
				t.Fatalf("CheckPush with push off = %v, want nil", err)
			}
			router, jwtSvc := newTestRouter(t)
			token := mailboxToken(t, jwtSvc)
			for _, r := range devicesRequests {
				for _, bearer := range []string{token, ""} {
					if got := devicesStatus(t, router, r.method, r.path, r.body, bearer); got != http.StatusNotFound {
						t.Errorf("%s %s (signed in: %v) = %d, want 404", r.method, r.path, bearer != "", got)
					}
				}
			}
		})
	}
}

// TestDevicesRoutesExistWhenPushIsOn: with APNs configured, the routes are
// there, behind the session: 401 without one, and the handler with one.
func TestDevicesRoutesExistWhenPushIsOn(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESTMAIL_APNS_KEY_FILE", keyFile)
	t.Setenv("RESTMAIL_APNS_KEY_ID", "KEY1234567")
	t.Setenv("RESTMAIL_APNS_TEAM_ID", "TEAM123456")
	t.Setenv("RESTMAIL_APNS_TOPIC", "")
	if err := CheckPush(&config.Config{}); err != nil {
		t.Fatalf("CheckPush with a good key = %v", err)
	}

	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	routers := NewRouters(newFailingGormDB(t), jwtSvc, &config.Config{Environment: "test"}, nil)
	t.Cleanup(routers.Close)
	if err := chi.Walk(routers.Public.(chi.Routes), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !routers.Registry.Has(method, path) {
			t.Errorf("push-enabled server serves %s %s without registering it", method, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	description, warnings := routers.Description("test")
	if len(warnings) != 0 {
		t.Fatalf("push-enabled description has warnings: %v", warnings)
	}
	data, err := json.Marshal(description)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]map[string]interface{} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct{ path, method, id string }{
		{"/api/v1/devices", "get", "listPushDevices"},
		{"/api/v1/devices", "post", "registerPushDevice"},
		{"/api/v1/devices/{device_id}", "delete", "removePushDevice"},
	} {
		if document.Paths[operation.path][operation.method].OperationID != operation.id {
			t.Errorf("push-enabled description omits %s", operation.id)
		}
	}
	device := document.Components.Schemas["deviceResponse"]
	if device["additionalProperties"] != false {
		t.Fatal("the push device response is not closed")
	}
	if _, leaked := device["properties"].(map[string]interface{})["token"]; leaked {
		t.Fatal("the push device response declares a secret token")
	}

	token := mailboxToken(t, jwtSvc)
	for _, r := range devicesRequests {
		if got := devicesStatus(t, routers.Public, r.method, r.path, r.body, ""); got != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", r.method, r.path, got)
		}
		// The database of these tests fails every query: reaching the
		// handler is anything but the middleware's 401 or the router's 404.
		if got := devicesStatus(t, routers.Public, r.method, r.path, r.body, token); got == http.StatusNotFound || got == http.StatusUnauthorized {
			t.Errorf("%s %s with a session = %d, want the handler's answer", r.method, r.path, got)
		}
	}

	// A key that is set and does not load refuses to start the server.
	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckPush(&config.Config{}); err == nil {
		t.Fatal("CheckPush accepted an APNs key that does not parse")
	}
}
