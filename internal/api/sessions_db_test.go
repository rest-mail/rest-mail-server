package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// Every token an account was issued stops working the moment its sessions end
// (#354): a new password, a reset, 2FA turned off, an admin ending them, a
// disable. Refused on the API, on the event stream and at refresh, while a
// session signed in after works. These run the real router, with the real
// session check and grants, on a database of their own.

type epochSession struct {
	access, refresh string
}

// epochFixture is credentialFixture with the real session check, 2FA, and an
// admin, admin@, holding the admin role at "*".
type epochFixture struct {
	*credentialFixture
	srv   *httptest.Server
	admin string // admin@'s access token
}

func newEpochFixture(t *testing.T) *epochFixture {
	t.Helper()
	useRealGrants(t)
	f := &epochFixture{credentialFixture: newCredentialFixtureWith(t, &config.Config{
		Environment: "test", TOTP2FAEnabled: true, MasterKey: accessMasterKey,
	})}
	f.srv = httptest.NewServer(f.router)
	t.Cleanup(f.srv.Close)

	var domain models.Domain
	f.db.First(&domain)
	adminID := f.seedMailbox(domain.ID, "admin")
	var role models.Role
	if err := f.db.Where("name = ?", "admin").First(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: adminID, RoleID: role.ID,
		Scope: grants.All, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	f.admin = "Bearer " + f.signIn("admin@example.test", credentialTestPassword, "").access
	return f
}

func (f *epochFixture) signIn(address, password, code string) epochSession {
	f.t.Helper()
	rr := f.do(http.MethodPost, "/api/v1/auth/login", "",
		fmt.Sprintf(`{"email":%q,"password":%q,"totp_code":%q,"client":"native"}`, address, password, code))
	if rr.Code != http.StatusOK {
		f.t.Fatalf("sign in as %s: %d %s", address, rr.Code, rr.Body.String())
	}
	return sessionOf(f.t, rr)
}

func sessionOf(t *testing.T, rr *httptest.ResponseRecorder) epochSession {
	t.Helper()
	var resp struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	decodeInto(t, rr, &resp)
	return epochSession{access: resp.Data.AccessToken, refresh: resp.Data.RefreshToken}
}

// folders is what chris@'s folders answer the session.
func (f *epochFixture) folders(s epochSession) int {
	return f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", f.accountID), "Bearer "+s.access, "").Code
}

// events is what chris@'s event stream answers the session. One that opens is
// closed again at once.
func (f *epochFixture) events(s epochSession) int {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/accounts/%d/events", f.srv.URL, f.accountID), nil)
	req.Header.Set("Authorization", "Bearer "+s.access)
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatalf("open the event stream: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// refresh exchanges the session's refresh token, and returns the new session
// when it is accepted.
func (f *epochFixture) refresh(s epochSession) (int, epochSession) {
	f.t.Helper()
	rr := f.do(http.MethodPost, "/api/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, s.refresh))
	if rr.Code != http.StatusOK {
		return rr.Code, epochSession{}
	}
	return rr.Code, sessionOf(f.t, rr)
}

func (f *epochFixture) protocolSignIn(password string) int {
	return f.do(http.MethodPost, "/api/v1/auth/protocol-sign-in", basic(f.address, password), `{"protocol":"imap"}`).Code
}

func (f *epochFixture) appPassword(s epochSession) string {
	f.t.Helper()
	rr := f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID), "Bearer "+s.access,
		fmt.Sprintf(`{"name":"Thunderbird","password":%q}`, f.password))
	if rr.Code != http.StatusCreated {
		f.t.Fatalf("create an app password: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data createdCredential `json:"data"`
	}
	decodeInto(f.t, rr, &resp)
	return resp.Data.Password
}

// enrollTwoFactor turns 2FA on for chris@ through the API, and returns its
// TOTP secret and a recovery code.
func (f *epochFixture) enrollTwoFactor(s epochSession) (secret, recovery string) {
	f.t.Helper()
	rr := f.do(http.MethodPost, "/api/v1/auth/2fa/enroll", "Bearer "+s.access, "")
	if rr.Code != http.StatusOK {
		f.t.Fatalf("enroll 2FA: %d %s", rr.Code, rr.Body.String())
	}
	var enrolled struct {
		Data struct {
			Secret        string   `json:"secret"`
			RecoveryCodes []string `json:"recovery_codes"`
		} `json:"data"`
	}
	decodeInto(f.t, rr, &enrolled)
	code, err := auth.GenerateTOTPCode(enrolled.Data.Secret, time.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	if rr := f.do(http.MethodPost, "/api/v1/auth/2fa/confirm", "Bearer "+s.access, fmt.Sprintf(`{"totp_code":%q}`, code)); rr.Code != http.StatusNoContent {
		f.t.Fatalf("confirm 2FA: %d %s", rr.Code, rr.Body.String())
	}
	return enrolled.Data.Secret, enrolled.Data.RecoveryCodes[0]
}

func TestSessionsEnd_EveryTokenIssuedBeforeIsRefused(t *testing.T) {
	const newPassword = "a-new-password-456"
	mailbox := func(f *epochFixture) string { return fmt.Sprintf("/api/v1/admin/mailboxes/%d", f.mailboxID) }
	expect := func(t *testing.T, rr *httptest.ResponseRecorder, want int, what string) {
		t.Helper()
		if rr.Code != want {
			t.Fatalf("%s: %d %s, want %d", what, rr.Code, rr.Body.String(), want)
		}
	}

	cases := []struct {
		name string
		// twoFactor signs chris@ in with 2FA before the change.
		twoFactor bool
		// end ends chris@'s sessions, given one from before, and leaves f.password
		// its password now.
		end func(t *testing.T, f *epochFixture, before epochSession, recovery string)
		// appPasswordStays: an app password made before still works after.
		// Only a new password revokes them (#353).
		appPasswordStays bool
		// inSetup: the session signed in after is in setup, and reaches only
		// the setup routes.
		inSetup bool
	}{
		{
			name: "a new password",
			end: func(t *testing.T, f *epochFixture, before epochSession, _ string) {
				expect(t, f.do(http.MethodPost, "/api/v1/auth/password", "Bearer "+before.access,
					fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, f.password, newPassword)), http.StatusOK, "change the password")
				f.password = newPassword
			},
		},
		{
			name: "a new password set by an admin",
			end: func(t *testing.T, f *epochFixture, _ epochSession, _ string) {
				expect(t, f.do(http.MethodPatch, mailbox(f), f.admin, fmt.Sprintf(`{"password":%q}`, newPassword)), http.StatusOK, "set a new password")
				f.password = newPassword
			},
		},
		{
			name: "a reset with a one-time token",
			end: func(t *testing.T, f *epochFixture, _ epochSession, _ string) {
				token, _, err := bootstrap.MintResetToken(f.db, f.address, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				expect(t, f.do(http.MethodPost, "/api/v1/auth/reset", "",
					fmt.Sprintf(`{"token":%q,"password":%q,"client":"native"}`, token, newPassword)), http.StatusOK, "redeem the reset token")
				f.password = newPassword
			},
			inSetup: true,
		},
		{
			name:      "2FA turned off",
			twoFactor: true,
			end: func(t *testing.T, f *epochFixture, before epochSession, recovery string) {
				expect(t, f.do(http.MethodPost, "/api/v1/auth/2fa/disable", "Bearer "+before.access,
					fmt.Sprintf(`{"recovery_code":%q}`, recovery)), http.StatusNoContent, "turn 2FA off")
			},
			appPasswordStays: true,
		},
		{
			name: "an admin ending them",
			end: func(t *testing.T, f *epochFixture, _ epochSession, _ string) {
				expect(t, f.do(http.MethodDelete, mailbox(f)+"/sessions", f.admin, ""), http.StatusNoContent, "end the sessions")
			},
			appPasswordStays: true,
		},
		{
			name: "a disable, even once enabled again",
			end: func(t *testing.T, f *epochFixture, _ epochSession, _ string) {
				expect(t, f.do(http.MethodPatch, mailbox(f), f.admin, `{"active":false}`), http.StatusOK, "disable")
				expect(t, f.do(http.MethodPatch, mailbox(f), f.admin, `{"active":true}`), http.StatusOK, "enable")
			},
			appPasswordStays: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEpochFixture(t)
			oldPassword := f.password

			before := f.signIn(f.address, f.password, "")
			var recovery string
			if tc.twoFactor {
				var secret string
				secret, recovery = f.enrollTwoFactor(before)
				code, err := auth.GenerateTOTPCode(secret, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				before = f.signIn(f.address, f.password, code)
			}
			app := f.appPassword(before)
			// The session works, on the API and the event stream, until the change.
			if got := f.folders(before); got != http.StatusOK {
				t.Fatalf("folders before the change: %d", got)
			}
			if got := f.events(before); got != http.StatusOK {
				t.Fatalf("event stream before the change: %d", got)
			}

			tc.end(t, f, before, recovery)

			if got := f.folders(before); got != http.StatusUnauthorized {
				t.Errorf("folders with a token from before: %d, want 401", got)
			}
			if got := f.events(before); got != http.StatusUnauthorized {
				t.Errorf("event stream with a token from before: %d, want 401", got)
			}
			if got, _ := f.refresh(before); got != http.StatusUnauthorized {
				t.Errorf("refresh with a token from before: %d, want 401", got)
			}

			// A mail client: the app password made before, and the password.
			wantApp := http.StatusUnauthorized
			if tc.appPasswordStays {
				wantApp = http.StatusOK
			}
			if got := f.protocolSignIn(app); got != wantApp {
				t.Errorf("gateway sign-in with an app password from before: %d, want %d", got, wantApp)
			}
			if f.password != oldPassword {
				if got := f.protocolSignIn(oldPassword); got != http.StatusUnauthorized {
					t.Errorf("gateway sign-in with the old password: %d, want 401", got)
				}
			}
			if !tc.inSetup {
				if got := f.protocolSignIn(f.password); got != http.StatusOK {
					t.Errorf("gateway sign-in with the password now: %d, want 200", got)
				}
			}

			// A session signed in after the change works everywhere.
			after := f.signIn(f.address, f.password, "")
			if tc.inSetup {
				if rr := f.do(http.MethodGet, "/api/v1/auth/2fa", "Bearer "+after.access, ""); rr.Code != http.StatusOK {
					t.Errorf("setup with a token from after: %d %s", rr.Code, rr.Body.String())
				}
				if got, _ := f.refresh(after); got != http.StatusOK {
					t.Errorf("refresh with a token from after: %d", got)
				}
				return
			}
			if got := f.folders(after); got != http.StatusOK {
				t.Errorf("folders with a token from after: %d", got)
			}
			if got := f.events(after); got != http.StatusOK {
				t.Errorf("event stream with a token from after: %d", got)
			}
			got, refreshed := f.refresh(after)
			if got != http.StatusOK {
				t.Fatalf("refresh with a token from after: %d", got)
			}
			if got := f.folders(refreshed); got != http.StatusOK {
				t.Errorf("folders with the refreshed token: %d", got)
			}
		})
	}
}
