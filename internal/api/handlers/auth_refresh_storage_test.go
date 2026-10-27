package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// A refresh that cannot reach its stores fails closed, minting nothing, and
// says the failure is the server's (#1162, area #632). It used to answer 401
// "Invalid or expired refresh token", which tells a client its session is
// over, while the token still works once the database is back.

var errSessionStore = errors.New("session store unavailable: dial tcp 10.0.0.9:5432")

type failingAccountState struct{}

func (failingAccountState) AccountState(uint) (accountState, error) {
	return accountState{}, errSessionStore
}

type failingRotateStore struct{ *fakeRefreshStore }

func (failingRotateStore) Rotate(string) error { return errSessionStore }

func TestRefreshReportsAStorageFailureNotAnEndedSession(t *testing.T) {
	jwt := auth.NewJWTService("refresh-storage-secret", 15*time.Minute, 7*24*time.Hour)
	for _, tc := range []struct {
		name  string
		setup func(*AuthHandler, *fakeRefreshStore)
	}{
		{"account state unreadable", func(h *AuthHandler, _ *fakeRefreshStore) { h.accountState = failingAccountState{} }},
		{"rotation ledger unreachable", func(h *AuthHandler, store *fakeRefreshStore) {
			h.refreshStore = failingRotateStore{store}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeRefreshStore()
			h := newRefreshHandler(jwt, store)
			pair, err := jwt.GenerateSession(42, "user@example.test", 0, []string{auth.AMRPassword}, false)
			if err != nil {
				t.Fatal(err)
			}
			seedActive(store, pair, 42)
			tc.setup(h, store)

			rr := doRefresh(h, pair.RefreshToken)
			if rr.Code != http.StatusInternalServerError || errorCode(rr) != "internal_error" {
				t.Fatalf("status %d %q (%s), want 500 internal_error", rr.Code, errorCode(rr), rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "10.0.0.9") {
				t.Errorf("the dependency failure reached the client: %s", rr.Body.String())
			}
			if refreshCookieValue(rr) != "" {
				t.Error("a refresh that failed closed set a new refresh token")
			}
			if got := store.statusOf(pair.RefreshJTI); got != models.RefreshTokenActive {
				t.Errorf("the presented token is %q, want it still active for the retry", got)
			}

			// Once the stores answer, the same token refreshes.
			h.accountState, h.refreshStore = nil, store
			if retry := doRefresh(h, pair.RefreshToken); retry.Code != http.StatusOK {
				t.Errorf("retry: status %d (%s), want 200", retry.Code, retry.Body.String())
			}
		})
	}

	// A token the ledger does not hold as active is still refused as ended.
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)
	pair, err := jwt.GenerateSession(42, "user@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rr := doRefresh(h, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Errorf("unknown token: status %d (%s), want 401", rr.Code, rr.Body.String())
	}
}
