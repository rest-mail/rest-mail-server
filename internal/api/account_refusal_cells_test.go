package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// The authorization matrix sends these requests as a principal with no link to
// the account it names, and they are the answers it reported as disagreeing
// with a required 403 (#1162). The owner decided on #672 that such a caller gets
// the same 404 as for an account that does not exist, so the matrix now accepts
// it. This proves the server's half: that it is a 404, and that it is the same
// one, so the answer says nothing about whether the account exists.
func TestAccountRefusals_ANoLinkCallerCannotTellAnAccountExists(t *testing.T) {
	f := newCredentialFixture(t)
	var domain models.Domain
	if err := f.db.First(&domain).Error; err != nil {
		t.Fatal(err)
	}
	stranger := f.seedMailbox(domain.ID, "stranger")
	const missing = uint(4_000_000)

	// The matrix's ordinary principal signed in with only a password.
	pair, err := f.jwt.GenerateSession(f.accountID, f.address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	session := "Bearer " + pair.AccessToken
	appPassword := basic(f.address, f.createAppPassword("laptop").Password)

	createKey := fmt.Sprintf(`{"name":"k","password":%q,"permissions":[{"capability":"mail:read","scope":"mailbox:stranger@example.test"}]}`, credentialTestPassword)
	updateKey := fmt.Sprintf(`{"password":%q,"permissions":[{"capability":"mail:read","scope":"mailbox:stranger@example.test"}]}`, credentialTestPassword)

	for _, tc := range []struct {
		operation, method, path, as, body string
	}{
		{"getAccount as ordinary", http.MethodGet, "/api/v1/accounts/%d", session, ""},
		{"getAccount as app-password", http.MethodGet, "/api/v1/accounts/%d", appPassword, ""},
		{"listAPIKeys as ordinary", http.MethodGet, "/api/v1/accounts/%d/api-keys", session, ""},
		{"createAPIKey as ordinary", http.MethodPost, "/api/v1/accounts/%d/api-keys", session, createKey},
		{"updateAPIKey as ordinary", http.MethodPatch, "/api/v1/accounts/%d/api-keys/1", session, updateKey},
		{"listCredentials as ordinary", http.MethodGet, "/api/v1/accounts/%d/credentials", session, ""},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			someoneElses := f.do(tc.method, fmt.Sprintf(tc.path, stranger), tc.as, tc.body)
			if someoneElses.Code != http.StatusNotFound {
				t.Fatalf("someone else's account: %d %s", someoneElses.Code, someoneElses.Body.String())
			}
			absent := f.do(tc.method, fmt.Sprintf(tc.path, missing), tc.as, tc.body)
			if absent.Code != http.StatusNotFound || absent.Body.String() != someoneElses.Body.String() {
				t.Errorf("an account that does not exist answers differently:\n%d %s\nsomeone else's:\n%d %s",
					absent.Code, absent.Body.String(), someoneElses.Code, someoneElses.Body.String())
			}
		})
	}
}
