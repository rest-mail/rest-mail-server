package session

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

// setupServer plays a fresh server's first-run rules: a bootstrap password
// and no authenticator, so every session is held in setup until both change.
type setupServer struct {
	mu        sync.Mutex
	password  string
	confirmed bool
	issued    int
	live      map[string]bool
}

func (server *setupServer) steps() []string {
	var steps []string
	if server.password == "bootstrap-password" {
		steps = append(steps, SetupPassword)
	}
	if !server.confirmed {
		steps = append(steps, SetupTwoFactor)
	}
	return steps
}

func (server *setupServer) session(writer http.ResponseWriter) {
	server.issued++
	access := fmt.Sprintf("setup-access-%d", server.issued)
	server.live[access] = true
	steps, _ := json.Marshal(server.steps())
	_, _ = fmt.Fprintf(writer, `{"data":{"access_token":%q,"refresh_token":"setup-refresh-%d","expires_in":3600,"user":{"id":7},"setup_required":%s}}`, access, server.issued, steps)
}

func newSetupServer(t *testing.T) (*setupServer, string) {
	t.Helper()
	keyring.MockInit()
	state := &setupServer{password: "bootstrap-password", live: map[string]bool{}}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		switch request.URL.Path {
		case "/api/v1/auth/login":
			if body["password"] != state.password {
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(writer, `{"error":{"code":"unauthorized","message":"Invalid email or password"}}`)
				return
			}
			state.session(writer)
			return
		case "/api/v1/auth/refresh":
			state.session(writer)
			return
		}
		if !state.live[strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")] {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(writer, `{"error":{"code":"unauthorized","message":"Invalid or expired session"}}`)
			return
		}
		switch request.URL.Path {
		case "/api/v1/auth/password":
			if body["current_password"] != state.password {
				writer.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(writer, `{"error":{"code":"invalid_password","message":"Current password is incorrect"}}`)
				return
			}
			if len(body["new_password"]) < 12 {
				writer.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(writer, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"new_password":"must be at least 12 characters"}}}}`)
				return
			}
			state.password = body["new_password"]
			state.live = map[string]bool{}
			_, _ = io.WriteString(writer, `{"data":{"message":"Password changed. Sign in again."}}`)
		case "/api/v1/auth/2fa/enroll":
			_, _ = io.WriteString(writer, `{"data":{"secret":"JBSWY3DPEHPK3PXP","otpauth_url":"otpauth://totp/REST%20Mail:admin?secret=JBSWY3DPEHPK3PXP","recovery_codes":["one-time-1","one-time-2"]}}`)
		case "/api/v1/auth/2fa/confirm":
			if body["totp_code"] != "123456" {
				writer.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(writer, `{"error":{"code":"invalid_totp","message":"Invalid two-factor code"}}`)
				return
			}
			state.confirmed = true
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(writer, `{"error":{"code":"setup_required","message":"Finish account setup first"}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	return state, strings.Replace(upstream.URL, "127.0.0.1", "localhost", 1)
}

func TestFirstRunSetupWalksPasswordThenAuthenticator(t *testing.T) {
	_, server := newSetupServer(t)
	manager := New()
	var events []SessionStatus
	manager.SetOnChange(func(status SessionStatus) { events = append(events, status) })

	result, err := manager.SignIn(server, "admin@example.test", "bootstrap-password")
	if err != nil {
		t.Fatal(err)
	}
	both := []string{SetupPassword, SetupTwoFactor}
	if !slices.Equal(result.SetupRequired, both) || !slices.Equal(manager.Status().SetupRequired, both) {
		t.Fatalf("sign-in lost the setup steps: %+v / %+v", result, manager.Status())
	}

	if _, err := manager.ChangePassword("wrong", "a-long-new-password"); err == nil || !strings.Contains(err.Error(), "Current password is incorrect") {
		t.Fatalf("wrong current password not reported: %v", err)
	}
	if _, err := manager.ChangePassword("bootstrap-password", "short"); err == nil || err.Error() != "new password must be at least 12 characters" {
		t.Fatalf("field problem not reported: %v", err)
	}
	result, err = manager.ChangePassword("bootstrap-password", "a-long-new-password")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.SetupRequired, []string{SetupTwoFactor}) || !manager.Status().SignedIn {
		t.Fatalf("password change did not sign in again: %+v / %+v", result, manager.Status())
	}

	enrollment, err := manager.EnrollTwoFactor()
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Secret != "JBSWY3DPEHPK3PXP" || !strings.HasPrefix(enrollment.OTPAuthURL, "otpauth://") || len(enrollment.RecoveryCodes) != 2 {
		t.Fatalf("enrollment incomplete: %+v", enrollment)
	}
	if err := manager.ConfirmTwoFactor("000000"); err == nil || !strings.Contains(err.Error(), "Invalid two-factor code") {
		t.Fatalf("wrong code not reported: %v", err)
	}
	if err := manager.ConfirmTwoFactor("123456"); err != nil {
		t.Fatal(err)
	}
	status := manager.Status()
	if !status.SignedIn || len(status.SetupRequired) != 0 {
		t.Fatalf("setup still pending after both steps: %+v", status)
	}
	if last := events[len(events)-1]; len(last.SetupRequired) != 0 || !last.SignedIn {
		t.Fatalf("no change event for finished setup: %+v", events)
	}
}
