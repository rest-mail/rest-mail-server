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

// Every example pair of the subscription routes (#1675), sent to the production
// routes over PostgreSQL. A pair for account 1 goes to the fixture's account,
// which is new and so subscribed to its standard folders.
func TestFolderSubscriptionExamples(t *testing.T) {
	f := newCredentialFixture(t)
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
				if name == "invalid_id" || name == "missing" {
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
				if token != "" {
					req.Header.Set("Authorization", token)
				}
				rr := httptest.NewRecorder()
				f.router.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("want %d: %d %s", pair.Response.Status, rr.Code, rr.Body)
				}
				if len(pair.Response.Body) > 0 {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
			})
		}
	}
}
