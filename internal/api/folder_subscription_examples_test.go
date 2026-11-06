package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// Every subscription pair (#1675), with PostgreSQL for storage and production
// refusal fixtures for budgets and dependency faults. A pair for account 1 goes
// to the fixture's account, newly subscribed to its standard folders.
func TestFolderSubscriptionExamples(t *testing.T) {
	f := newCredentialFixture(t)
	unavailable, failed, _, jwt := authReadBatchRouters(t)
	faultToken := "Bearer " + mailboxToken(t, jwt)
	limited := apiKeyRefusalLimited(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct{ id, method, path, without string }{
		{"listSubscriptions", http.MethodGet, "/api/v1/accounts/{id}/subscriptions", models.CapMailWrite},
		{"subscribeFolder", http.MethodPut, "/api/v1/accounts/{id}/subscriptions/{folder}", models.CapMailRead},
		{"unsubscribeFolder", http.MethodDelete, "/api/v1/accounts/{id}/subscriptions/{folder}", models.CapMailRead},
	} {
		if len(pairs[op.id]) < 5 {
			t.Fatalf("incomplete %s pairs", op.id)
		}
		// A key holding only the other mail capability is the pairs' without_capability principal.
		key, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		credential := models.Credential{AccountID: f.accountID, Kind: models.CredentialKindAPIKey, Name: op.id, PublicID: public, SecretHash: hash,
			Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: op.without, Scope: grants.Mailbox(f.address)}}}}
		if err := f.db.Create(&credential).Error; err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(pairs[op.id]))
		for name := range pairs[op.id] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			pair := pairs[op.id][name]
			t.Run(op.id+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op.id, name)
				id := fmt.Sprint(f.mailboxID)
				if pair.Request.Path["id"] != float64(1) {
					id = fmt.Sprintf("%.0f", pair.Request.Path["id"])
				}
				uri := strings.ReplaceAll(op.path, "{id}", id)
				if folder, ok := pair.Request.Path["folder"].(string); ok {
					uri = strings.ReplaceAll(uri, "{folder}", folder)
				}
				token := f.session
				switch pair.Requires["principal"] {
				case "anonymous":
					token = ""
				case "without_capability":
					token = "Bearer " + key
				}
				req := httptest.NewRequest(op.method, uri, nil)
				req.RemoteAddr = "192.0.2.10:50000"
				router := f.router
				switch requirementOf(pair) {
				case "limit=rate":
					router = limited
				case "body=over_cap":
					router, token = failed, faultToken
					req.ContentLength = 9
				case "fault=session_store":
					router, token = unavailable, faultToken
				case "fault=database":
					router, token = failed, faultToken
				}
				if token != "" {
					req.Header.Set("Authorization", token)
				}
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				assertRequirementResponse(t, pair, rr)
			})
		}
	}
}
