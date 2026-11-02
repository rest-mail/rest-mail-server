package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// The own password and two-factor pairs the shared requirement tests do not send run
// the real router on a scratch database, as a signed-in account with no authenticator,
// and check that a refusal left the password, the authenticator and the session as
// they were (#634). They skip, and prove nothing, when no database is reachable.

var ownTwoFactorPaths = map[string]string{
	"changeOwnPassword":  "/api/v1/auth/password",
	"confirmTwoFactor":   "/api/v1/auth/2fa/confirm",
	"disableTwoFactor":   "/api/v1/auth/2fa/disable",
	"enrollTwoFactor":    "/api/v1/auth/2fa/enroll",
	"getTwoFactorStatus": "/api/v1/auth/2fa",
}

func newOwnTwoFactorFixture(t *testing.T) *credentialFixture {
	t.Helper()
	return newCredentialFixtureWith(t, &config.Config{Environment: "test", MasterKey: accessMasterKey, TOTP2FAEnabled: true})
}

// sendOwnTwoFactorExample sends the pair's body on the fixture's session; a pair that
// requires a malformed body sends the truncated JSON byte its description names.
func sendOwnTwoFactorExample(f *credentialFixture, op string, pair oapifly.ExamplePair) {
	f.t.Helper()
	method, body := http.MethodPost, string(pair.Request.Body)
	if op == "getTwoFactorStatus" {
		method = http.MethodGet
	}
	if pair.Requires["body"] == "malformed" {
		body = "{"
	}
	assertRequirementResponse(f.t, pair, f.do(method, ownTwoFactorPaths[op], f.session, body))
}

// assertOwnCredentialsUnchanged checks a refusal kept the account's password, left it
// without an authenticator, and did not end the session that sent it.
func assertOwnCredentialsUnchanged(f *credentialFixture) {
	f.t.Helper()
	if cred, err := repositories.NewCredentialRepository(f.db).MatchPassword(f.mailboxID, credentialTestPassword, time.Now()); err != nil || cred == nil {
		f.t.Fatalf("refusal changed the password: %v", err)
	}
	if _, err := repositories.NewTwoFactorRepository(f.db).Get(f.mailboxID); !errors.Is(err, repositories.ErrTwoFactorNotFound) {
		f.t.Fatalf("refusal left an authenticator: %v", err)
	}
	if rr := f.do(http.MethodGet, ownTwoFactorPaths["getTwoFactorStatus"], f.session, ""); rr.Code != http.StatusOK {
		f.t.Fatalf("refusal ended the session: %d %s", rr.Code, rr.Body.String())
	}
}

func TestOwnTwoFactorExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ op, name string }{
		{"changeOwnPassword", "malformed_json"}, {"changeOwnPassword", "body_array"}, {"changeOwnPassword", "wrong_current"},
		{"confirmTwoFactor", "no_pending"}, {"confirmTwoFactor", "body_array"}, {"confirmTwoFactor", "code_missing"},
		{"disableTwoFactor", "not_enabled"}, {"disableTwoFactor", "body_array"}, {"disableTwoFactor", "code_missing"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			pair, ok := pairs[tc.op][tc.name]
			if !ok {
				t.Fatal("the named pair is missing")
			}
			f := newOwnTwoFactorFixture(t)
			if tc.name == "wrong_current" && strings.Contains(string(pair.Request.Body), credentialTestPassword) {
				t.Fatal("pair sends the account's password; it must send a different one")
			}
			sendOwnTwoFactorExample(f, tc.op, pair)
			assertOwnCredentialsUnchanged(f)
		})
	}

	t.Run("getTwoFactorStatus/example", func(t *testing.T) {
		examplepairs.Proven(t, "getTwoFactorStatus", "example")
		f := newOwnTwoFactorFixture(t)
		sendOwnTwoFactorExample(f, "getTwoFactorStatus", pairs["getTwoFactorStatus"]["example"])
	})

	t.Run("enrollTwoFactor/example", func(t *testing.T) {
		examplepairs.Proven(t, "enrollTwoFactor", "example")
		pair := pairs["enrollTwoFactor"]["example"]
		f := newOwnTwoFactorFixture(t)
		rr := f.do(http.MethodPost, ownTwoFactorPaths["enrollTwoFactor"], f.session, "")
		var got struct {
			Data struct {
				Secret        string   `json:"secret"`
				OTPAuthURL    string   `json:"otpauth_url"`
				RecoveryCodes []string `json:"recovery_codes"`
			} `json:"data"`
		}
		if rr.Code == http.StatusOK {
			decodeInto(t, rr, &got)
			provisioning, err := url.Parse(got.Data.OTPAuthURL)
			if err != nil || provisioning.Scheme != "otpauth" || provisioning.Host != "totp" || provisioning.Query().Get("secret") != got.Data.Secret || got.Data.Secret == "" {
				t.Fatalf("invalid provisioning URI %q for secret %q: %v", got.Data.OTPAuthURL, got.Data.Secret, err)
			}
			if !strings.Contains(provisioning.Path, f.address) {
				t.Fatalf("provisioning URI %q does not label %s", got.Data.OTPAuthURL, f.address)
			}
			if len(got.Data.RecoveryCodes) == 0 {
				t.Fatal("no recovery codes returned")
			}
			for _, code := range got.Data.RecoveryCodes {
				if code == "" {
					t.Fatal("empty recovery code returned")
				}
			}
			enrolled, err := repositories.NewTwoFactorRepository(f.db).Get(f.mailboxID)
			if err != nil || enrolled.ConfirmedAt != nil || enrolled.EncryptedSecret == "" || enrolled.EncryptedSecret == got.Data.Secret {
				t.Fatalf("enrollment not stored pending and encrypted: %+v (%v)", enrolled, err)
			}
			// The secret and codes are random; the pair shows their shape.
			rr.Body.Reset()
			rr.Body.Write(pair.Response.Body)
		}
		assertRequirementResponse(t, pair, rr)
		var status struct {
			Data struct{ Enabled, Pending bool } `json:"data"`
		}
		decodeInto(t, f.do(http.MethodGet, ownTwoFactorPaths["getTwoFactorStatus"], f.session, ""), &status)
		if status.Data.Enabled || !status.Data.Pending {
			t.Fatalf("status after enrollment = %+v, want pending", status.Data)
		}
	})

	for _, warning := range describedRouters(t).generator("own two-factor").Report() {
		if _, selected := ownTwoFactorPaths[warning.OperationID]; selected {
			t.Errorf("own password or two-factor operation still has a generator warning: %v", warning)
		}
	}
}
