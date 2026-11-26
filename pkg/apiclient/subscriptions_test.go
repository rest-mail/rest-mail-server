package apiclient

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A gateway-session token is valid only at the internal listener; a normal
// bearer token still belongs at the public listener. Exercise the real
// bodiless PUT transport and its escaped folder path in both directions.
func TestSubscribeUsesTokenSelectedListener(t *testing.T) {
	for _, test := range []struct {
		name, token string
		internal    bool
	}{
		{"gateway session", "rmgs_subscription", true},
		{"public bearer", "public_access_token", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var publicCalls, internalCalls atomic.Int32
			handler := func(internal bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if internal {
						internalCalls.Add(1)
					} else {
						publicCalls.Add(1)
					}
					if internal != test.internal {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/v1/accounts/7/subscriptions/Fruit%2FApple" || r.Header.Get("Authorization") != "Bearer "+test.token {
						t.Errorf("subscription request: %s %s, authorization %q", r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"))
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.WriteHeader(http.StatusOK)
				}
			}
			public := httptest.NewServer(handler(false))
			defer public.Close()
			internal := httptest.NewServer(handler(true))
			defer internal.Close()
			client := New(public.URL, WithInternalMTLS(internal.URL, nil))
			if err := client.Subscribe(test.token, 7, "Fruit/Apple"); err != nil {
				t.Fatal(err)
			}
			wantPublic, wantInternal := int32(1), int32(0)
			if test.internal {
				wantPublic, wantInternal = 0, 1
			}
			if publicCalls.Load() != wantPublic || internalCalls.Load() != wantInternal {
				t.Fatalf("public/internal calls = %d/%d, want %d/%d", publicCalls.Load(), internalCalls.Load(), wantPublic, wantInternal)
			}
		})
	}
}
