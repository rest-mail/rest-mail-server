package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

type failingLogoutStore struct {
	*fakeRefreshStore
	failure error
	calls   int
}

func (s *failingLogoutStore) Revoke(jti string) error {
	s.calls++
	if s.failure != nil {
		return s.failure
	}
	return s.fakeRefreshStore.Revoke(jti)
}

type logoutDeviceRecorder struct{ sessions []string }

func (d *logoutDeviceRecorder) SessionEnded(sid string, _ time.Time) (int, error) {
	d.sessions = append(d.sessions, sid)
	return 1, nil
}

func requestLogout(h *AuthHandler, token string, native bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	if native {
		r = bodyRequest("/api/v1/auth/logout", token)
	} else if token != "" {
		r.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: token})
	}
	rr := httptest.NewRecorder()
	serveAuth(h, rr, r)
	return rr
}

func requireLogoutCookiesExpired(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	for _, name := range []string{auth.AccessCookieName, auth.RefreshCookieName, auth.CSRFCookieName} {
		cookie := cookieByName(rr, name)
		if cookie == nil || cookie.MaxAge >= 0 || cookie.Value != "" {
			t.Fatalf("logout did not expire %s", name)
		}
	}
}

func TestLogoutRevocationFailureCanBeRetried(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "browser"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			jwt := auth.NewJWTService("logout-failure-fixture", time.Minute, time.Hour)
			pair, err := jwt.GenerateSession(7, "logout@example.test", 0, []string{auth.AMRPassword}, false)
			if err != nil {
				t.Fatal(err)
			}
			store := &failingLogoutStore{fakeRefreshStore: newFakeRefreshStore(), failure: errors.New("private ledger failure detail")}
			seedActive(store.fakeRefreshStore, pair, 7)
			devices := &logoutDeviceRecorder{}
			h := newRefreshHandler(jwt, store)
			h.SetPushDevices(devices)

			rr := requestLogout(h, pair.RefreshToken, native)
			if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), `"internal_error"`) ||
				strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Body.String(), pair.RefreshToken) {
				t.Fatalf("failed revocation must return a sanitized 500: %d %s", rr.Code, rr.Body)
			}
			if len(rr.Header().Values("Set-Cookie")) != 0 || store.statusOf(pair.RefreshJTI) != models.RefreshTokenActive || len(devices.sessions) != 0 || store.calls != 1 {
				t.Fatal("failed revocation cleared retry cookies, changed the ledger or removed session devices")
			}

			store.failure = nil
			rr = requestLogout(h, pair.RefreshToken, native)
			if rr.Code != http.StatusNoContent || store.statusOf(pair.RefreshJTI) != models.RefreshTokenRevoked ||
				len(devices.sessions) != 1 || devices.sessions[0] != pair.SessionID {
				t.Fatalf("retry did not end the session: %d %s", rr.Code, rr.Body)
			}
			requireLogoutCookiesExpired(t, rr)
			if got := doRefresh(h, pair.RefreshToken); got.Code != http.StatusUnauthorized {
				t.Fatalf("logged-out token can still refresh: %d", got.Code)
			}
			rr = requestLogout(h, pair.RefreshToken, native)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("repeated logout changed its idempotent response: %d", rr.Code)
			}
			requireLogoutCookiesExpired(t, rr)
		})
	}
}

func TestLogoutMissingOrInvalidTokenStillClearsCookies(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, token := range []string{"", "not-a-valid-refresh-token"} {
			jwt := auth.NewJWTService("logout-invalid-fixture", time.Minute, time.Hour)
			store := &failingLogoutStore{fakeRefreshStore: newFakeRefreshStore(), failure: errors.New("ledger unavailable")}
			h := newRefreshHandler(jwt, store)
			devices := &logoutDeviceRecorder{}
			h.SetPushDevices(devices)
			rr := requestLogout(h, token, native)
			if rr.Code != http.StatusNoContent || store.calls != 0 || len(devices.sessions) != 0 {
				t.Fatalf("missing/invalid token reached storage or changed response: %d %s", rr.Code, rr.Body)
			}
			requireLogoutCookiesExpired(t, rr)
		}
	}
}
