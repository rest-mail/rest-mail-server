package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

type scopedAccountState struct{ state accountState }

func (s *scopedAccountState) AccountState(uint) (accountState, error) { return s.state, nil }

func TestScopedRefreshSnapshot(t *testing.T) {
	for _, setup := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "setup"}[setup], func(t *testing.T) {
			jwt := auth.NewJWTService("scoped-refresh-secret", 15*time.Minute, 7*24*time.Hour)
			store := newFakeRefreshStore()
			h := newRefreshHandler(jwt, store)
			state := &scopedAccountState{accountState{Active: true, Address: "x@a.test", DisplayName: "X", SetupPending: setup, SetupRequired: []string{"password"}, Capabilities: []string{"queue:read"}, ScopedCapabilities: []grants.Entry{{Scope: "domain:a.test", Capability: "domains:read"}}}}
			h.accountState = state
			pair, err := jwt.GenerateSession(42, "x@a.test", 0, []string{auth.AMRPassword}, setup)
			if err != nil {
				t.Fatal(err)
			}
			seedActive(store, pair, 42)
			read := func(token string) loginResponse {
				rr := httptest.NewRecorder()
				serveAuth(h, rr, bodyRequest("/api/v1/auth/refresh", token))
				if rr.Code != http.StatusOK {
					t.Fatalf("refresh status%d body%s", rr.Code, rr.Body.String())
				}
				var out struct{ Data loginResponse }
				if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				return out.Data
			}
			first := read(pair.RefreshToken)
			if setup {
				if len(first.Capabilities) != 0 || len(first.ScopedCapabilities) != 0 {
					t.Fatal("setup gained permissions")
				}
			} else {
				if len(first.ScopedCapabilities) != 1 || first.ScopedCapabilities[0].Scope != "domain:a.test" {
					t.Fatalf("snapshot=%+v", first)
				}
				state.state.ScopedCapabilities = nil
				state.state.Capabilities = nil
				second := read(first.RefreshToken)
				if len(second.Capabilities) != 0 || len(second.ScopedCapabilities) != 0 {
					t.Fatalf("revoked scopes retained: %+v", second)
				}
			}
		})
	}
}
