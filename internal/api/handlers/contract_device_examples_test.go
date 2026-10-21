package handlers

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
)

// registerPushDevice's platform_unsupported, token_invalid,
// environment_invalid and app_id_mismatch requests (#386) are each refused by
// the real handler, for a session signed in with a password, with exactly
// their documented 422 and before anything is looked up or stored: the
// handler has no database.
func TestDeviceRegister_DocumentedRefusals(t *testing.T) {
	h := NewDeviceHandler(nil, pushTestTopic)
	signedIn := func(w http.ResponseWriter, r *http.Request) {
		claims := &auth.Claims{AccountID: 1, SessionID: "contract-session"}
		h.Register(w, r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, claims)))
	}
	documentedRefusals(t, "/api/v1/devices", "registerPushDevice", signedIn)
}

// The pairs vertrag compiles for registerPushDevice: registered with the 201,
// each refusal with the 422, and the 200 with no request (it needs the token
// registered first). Each refused request is registered with one field
// changed, so it is refused for that field alone.
func TestDeviceRegister_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/devices", "registerPushDevice")

	want := map[string][]string{
		"request": {"app_id_mismatch", "environment_invalid", "platform_unsupported", "registered", "token_invalid"},
		"200":     {"already_registered"},
		"201":     {"registered"},
		"422":     {"app_id_mismatch", "environment_invalid", "platform_unsupported", "token_invalid"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registerPushDevice's named examples are %v, want %v", got, want)
	}

	registered := map[string]interface{}{
		"platform":    "apns",
		"token":       "5c1d7e0f3a9b2c4d6e8f0a1b3c5d7e9f1a2b4c6d8e0f2a3b5c7d9e1f3a4b6c8d",
		"environment": "production",
		"app_id":      pushTestTopic,
	}
	if !reflect.DeepEqual(requests["registered"], registered) {
		t.Errorf("registered is %s, want the valid registration %s",
			mustJSON(requests["registered"]), mustJSON(registered))
	}
	for name, change := range map[string][2]string{
		"platform_unsupported": {"platform", "fcm"},
		"token_invalid":        {"token", "not-an-apns-token"},
		"environment_invalid":  {"environment", "staging"},
		"app_id_mismatch":      {"app_id", "com.example.other"},
	} {
		changed := map[string]interface{}{}
		for k, v := range registered {
			changed[k] = v
		}
		changed[change[0]] = change[1]
		if !reflect.DeepEqual(requests[name], changed) {
			t.Errorf("%s is %s, want registered with %s %q: %s",
				name, mustJSON(requests[name]), change[0], change[1], mustJSON(changed))
		}
	}
}
