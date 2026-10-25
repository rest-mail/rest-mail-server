package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/auth"
)

func TestCSRFMethodDescriptionMatchesGuard(t *testing.T) {
	for _, tc := range []struct {
		method    string
		protected bool
	}{
		{http.MethodGet, false}, {http.MethodHead, false}, {http.MethodOptions, false},
		{http.MethodPost, true}, {http.MethodPut, true}, {http.MethodPatch, true},
		{http.MethodDelete, true}, {http.MethodTrace, true},
	} {
		t.Run(tc.method, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/", nil)
			req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: "session"})
			rr := httptest.NewRecorder()
			CSRF()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(rr, req)
			refused := rr.Code == http.StatusForbidden
			if refused != tc.protected || CSRFProtectsMethod(tc.method) != tc.protected {
				t.Fatalf("%s guard answered%d and description protects=%v; want protected=%v", tc.method, rr.Code, CSRFProtectsMethod(tc.method), tc.protected)
			}
		})
	}
}
