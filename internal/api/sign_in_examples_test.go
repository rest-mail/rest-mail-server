package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// The sign-in pairs that need no requirement, but that the live contract run
// does not send exactly (#632): the refusals binding or the handler makes before
// any credential is checked, and signing out with a token that names no session.
// Each runs through the production router, on the auth requirement ledger, and
// must change nothing it holds.
func TestSignInRefusalExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"login", "body_array"}, {"login", "client_invalid"},
		{"logout", "body_array"}, {"logout", "token_not_string"}, {"logout", "signed_out"},
		{"refreshToken", "body_array"}, {"refreshToken", "token_not_string"},
		{"recoverAccount", "body_array"}, {"recoverAccount", "client_invalid"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			pair, ok := pairs[tc.op][tc.name]
			if !ok {
				t.Fatalf("no pair %s/%s", tc.op, tc.name)
			}
			c, r, _, _ := newAuthRequirement(t, tc.op, tc.name)
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, requirementRequest(routes[tc.op], pair))
			assertRequirementResponse(t, pair, rr)
			if c.epoch != 0 || c.revoked || c.recoveryUsed || c.refreshRevoked || c.refresh != nil {
				t.Fatal("the request changed credentials or sessions")
			}
			if tc.name != "signed_out" {
				return
			}
			// An unknown token still clears all three session cookies.
			expired := map[string]bool{}
			for _, cookie := range rr.Result().Cookies() {
				if cookie.Value == "" && cookie.MaxAge < 0 {
					expired[cookie.Name] = true
				}
			}
			for _, name := range []string{auth.AccessCookieName, auth.RefreshCookieName, auth.CSRFCookieName} {
				if !expired[name] {
					t.Fatalf("cookie %s was not expired: %s", name, strings.Join(rr.Header().Values("Set-Cookie"), "; "))
				}
			}
		})
	}
}

// The protocolSignIn body refusals (#632): a gateway signs a mail client in
// with a real app password over HTTP Basic, which passes, so the body alone is
// refused. Each case seeds its own scratch database and app password.
func TestProtocolSignInBodyExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid_body", "protocol_missing"} {
		t.Run(name, func(t *testing.T) {
			pair, ok := pairs["protocolSignIn"][name]
			if !ok {
				t.Fatalf("no pair protocolSignIn/%s", name)
			}
			examplepairs.Proven(t, "protocolSignIn", name)
			f := newCredentialFixture(t)
			app := f.createAppPassword("imap client")
			// The credential is real: with a valid body the same Basic
			// credential signs the client in.
			if rr := f.do(http.MethodPost, "/api/v1/auth/protocol-sign-in", basic(f.address, app.Password), `{"protocol":"imap"}`); rr.Code != http.StatusOK {
				t.Fatalf("sign in with the app password: %d %s", rr.Code, rr.Body.String())
			}
			before := f.credentialCount()
			rr := f.do(http.MethodPost, "/api/v1/auth/protocol-sign-in", basic(f.address, app.Password), string(pair.Request.Body))
			assertRequirementResponse(t, pair, rr)
			if f.credentialCount() != before {
				t.Fatal("the refusal changed the account's credentials")
			}
		})
	}
}
