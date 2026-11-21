package apiproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	keyring "github.com/zalando/go-keyring"
)

const accessSecret = "access-credential-only-go-may-see"
const refreshSecret = "refresh-credential-only-keychain-may-store"

func signedInProxy(t *testing.T) (*session.Manager, http.Handler) {
	t.Helper()
	keyring.MockInit()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/auth/login" {
			_, _ = fmt.Fprintf(writer, `{"data":{"access_token":%q,"refresh_token":%q,"expires_in":3600,"user":{"id":42}}}`, accessSecret, refreshSecret)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+accessSecret {
			t.Error("proxy did not supply the Go-side bearer token")
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("X-CSRF-Token") != "" {
			t.Error("browser credentials were forwarded")
		}
		writer.Header().Set("Set-Cookie", "restmail_access="+accessSecret)
		writer.Header().Set("Authorization", "Bearer "+accessSecret)
		writer.Header().Set("X-Reflected-Token", refreshSecret)
		writer.Header().Set("Content-Disposition", "attachment; filename="+accessSecret)
		switch request.URL.Path {
		case "/api/v1/redirect":
			writer.Header().Set("Location", "http://localhost:1/"+accessSecret)
			writer.WriteHeader(http.StatusFound)
		case "/api/v1/error":
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(writer, accessSecret)
		case "/api/v1/text", "/restmail/download":
			_, _ = io.WriteString(writer, "mail body "+accessSecret+" "+refreshSecret)
		case "/api/v1/compressed":
			writer.Header().Set("Content-Encoding", "gzip")
			_, _ = io.WriteString(writer, accessSecret)
		case "/api/v1/malformed":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"access_token":"`+accessSecret)
		case "/api/v1/upload":
			if request.Header.Get("Content-Type") != "multipart/form-data; boundary=desktop" || request.Header.Get("Idempotency-Key") != "send-once" {
				t.Error("proxy lost content type or idempotency key")
			}
			_, _ = io.WriteString(writer, `{"data":{}}`)
		default:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"data":{"subject":"hello","AccessToken":"new-token-not-yet-known-to-Go","nested":[{"refresh_token":%q,"token":%q,"credential":"another-secret","secret":"hidden","description":%q}]}}`, refreshSecret, accessSecret, "reflected "+accessSecret)
		}
	}))
	t.Cleanup(upstream.Close)
	manager := session.New()
	if _, err := manager.SignIn(strings.Replace(upstream.URL, "127.0.0.1", "localhost", 1), "admin@example.test", "password"); err != nil {
		t.Fatal(err)
	}
	return manager, New(manager)(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(writer, `<html><script>const paths=["/api/v1/messages","/api/v1/error","/api/v1/text","/restmail/download","/api/v1/redirect","/api/v1/compressed","/api/v1/malformed","/api/v1/auth/login","/api/v1/auth/refresh","/api/v1/auth/logout"]; Promise.all(paths.map(path=>fetch(path).then(async response=>({headers:[...response.headers],body:await response.text()}))));</script></html>`)
	}))
}

func TestProxyAddsBearerAndStripsCredentialFields(t *testing.T) {
	_, handler := signedInProxy(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	request.Header.Set("Authorization", "Bearer attacker-controlled")
	request.Header.Set("Cookie", "restmail_access=browser-controlled")
	request.Header.Set("X-CSRF-Token", "browser-csrf")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("proxy returned %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "AccessToken", "new-token-not-yet-known-to-Go", "another-secret", "hidden", accessSecret, refreshSecret} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Errorf("response exposed %s", forbidden)
		}
	}
	if recorder.Header().Get("Set-Cookie") != "" || recorder.Header().Get("Authorization") != "" || !strings.Contains(recorder.Body.String(), "hello") {
		t.Fatal("proxy did not preserve mailbox data while stripping credentials")
	}
}

func TestJavaScriptCannotReadToken(t *testing.T) {
	_, handler := signedInProxy(t)
	assets := httptest.NewServer(handler)
	defer assets.Close()
	pageResponse, err := http.Get(assets.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(pageResponse.Body)
	pageResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	pathsStart := strings.Index(string(page), "paths=") + len("paths=")
	pathsEnd := strings.Index(string(page)[pathsStart:], ";") + pathsStart
	var paths []string
	if err := json.Unmarshal(page[pathsStart:pathsEnd], &paths); err != nil || len(paths) != 10 {
		t.Fatalf("could not read page's Fetch request list: %v", err)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response, err := http.Get(assets.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			observable := string(page) + string(body) + fmt.Sprint(response.Header) + fmt.Sprint(response.Trailer)
			for _, secret := range []string{accessSecret, refreshSecret, "new-token-not-yet-known-to-Go"} {
				if strings.Contains(observable, secret) {
					t.Fatalf("Fetch-visible response leaked a credential at %s", path)
				}
			}
		})
	}
}

func TestProxyReturns401WhenSignedOut(t *testing.T) {
	handler := New(session.New())(http.NotFoundHandler())
	for _, path := range []string{"/api/v1/messages", "/restmail/messages"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("signed-out response %d", recorder.Code)
		}
	}
}

func TestProxyForwardsMultipartAndIdempotency(t *testing.T) {
	_, handler := signedInProxy(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/upload", strings.NewReader("attachment"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=desktop")
	request.Header.Set("Idempotency-Key", "send-once")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatal(recorder.Code)
	}
}

func TestProxyMapsNetworkFailuresTo502(t *testing.T) {
	manager, handler := signedInProxy(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request.WithContext(ctx))
	if recorder.Code != http.StatusBadGateway || !manager.Status().SignedIn {
		t.Fatalf("bad error mapping: %d", recorder.Code)
	}
}
