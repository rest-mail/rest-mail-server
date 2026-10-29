package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/examplepairs"
	"github.com/restmail/restmail/internal/grants"
)

// The client secret pairs that need a second-factor session, a stored client
// secret or a revoked one run the real router on a scratch database holding
// exactly that state, and check what the request changed as well as the answer
// (#1237). They skip, and prove nothing, when no database is reachable.

// The pairs' client secret: its id, secret and row, as the examples show them.
const (
	clientSecretExampleID     = 34
	clientSecretExamplePublic = "9Hm3yU1cNp5Q"
	clientSecretExampleSecret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func clientSecretExamplePairs(t *testing.T) map[string]map[string]oapifly.ExamplePair {
	t.Helper()
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	return pairs
}

// sendClientSecretExample sends the pair's request, path, body and media type,
// to the fixture's account in place of the pair's {id}.
func sendClientSecretExample(f *credentialFixture, route requirementRoute, pair oapifly.ExamplePair, authorization string) *httptest.ResponseRecorder {
	f.t.Helper()
	path := maps.Clone(pair.Request.Path)
	if _, ok := path["id"]; ok {
		path["id"] = float64(f.accountID)
	}
	pair.Request.Path = path
	body := []byte(pair.Request.Body)
	mediaType := pair.Request.MediaType
	switch mediaType {
	case "":
		mediaType = "application/json"
	case "application/x-www-form-urlencoded":
		var form string
		if err := json.Unmarshal(pair.Request.Body, &form); err != nil {
			f.t.Fatalf("form body: %v", err)
		}
		body = []byte(form)
	}
	req := httptest.NewRequest(route.method, messageCollectionReadURI(route.path, pair), bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Type", mediaType)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.RemoteAddr = "192.0.2.10:50000"
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

// knownPassword makes password the fixture mailbox's password.
func knownPassword(f *credentialFixture, password string) {
	f.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := repositories.NewCredentialRepository(f.db).SetPassword(f.mailboxID, hash, nil, time.Now()); err != nil {
		f.t.Fatalf("set the known password: %v", err)
	}
}

// twoFactorSession is a session for the fixture mailbox that signed in with its
// password and a second factor, at the account's current epoch.
func twoFactorSession(f *credentialFixture) string {
	f.t.Helper()
	var mailbox models.Mailbox
	if err := f.db.First(&mailbox, f.mailboxID).Error; err != nil {
		f.t.Fatal(err)
	}
	session, err := f.jwt.GenerateSession(f.mailboxID, f.address, mailbox.CredentialEpoch, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return "Bearer " + session.AccessToken
}

// administerExampleDomain gives chris@ the admin role on example.test, so the
// pairs' pipelines:write at domain:example.test is within its grants.
func administerExampleDomain(f *credentialFixture) {
	f.t.Helper()
	var admin models.Role
	if err := f.db.Where("name = ?", "admin").First(&admin).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: f.mailboxID, RoleID: admin.ID,
		Scope: grants.Domain("example.test"), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		f.t.Fatal(err)
	}
}

// clientSecret stores the pairs' client secret for chris@, created and expiring
// as the examples show it unless expires is set, and revoked when revoked is.
func clientSecret(f *credentialFixture, expires *time.Time, revoked bool) models.Credential {
	f.t.Helper()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if expires == nil {
		at := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		expires = &at
	}
	by := f.mailboxID
	cred := models.Credential{ID: clientSecretExampleID, AccountID: f.mailboxID, Kind: models.CredentialKindClientSecret, Name: "Invoice robot",
		PublicID: clientSecretExamplePublic, SecretHash: auth.HashAppPasswordSecret(clientSecretExampleSecret),
		Scope:       models.CredentialScope{MailboxID: f.mailboxID, Capabilities: []string{}, Permissions: []auth.ScopedPermission{{Capability: "pipelines:write", Scope: "domain:example.test"}}},
		CreatedByID: &by, CreatedAt: created, ExpiresAt: expires}
	if revoked {
		cred.RevokedAt = &created
	}
	if err := f.db.Create(&cred).Error; err != nil {
		f.t.Fatalf("seed client secret: %v", err)
	}
	return cred
}

// usableClientSecret is the pairs' client secret, expiring an hour from now so
// it can still be exchanged.
func usableClientSecret(f *credentialFixture, revoked bool) models.Credential {
	f.t.Helper()
	expires := time.Now().Add(time.Hour)
	return clientSecret(f, &expires, revoked)
}

func reloadCredential(f *credentialFixture, id uint) models.Credential {
	f.t.Helper()
	var cred models.Credential
	if err := f.db.First(&cred, id).Error; err != nil {
		f.t.Fatal(err)
	}
	return cred
}

// sameInstant checks that got[key] is the instant want[key] names, written
// in whatever zone, and puts want's text in its place.
func sameInstant(t *testing.T, got, want map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		g, gerr := time.Parse(time.RFC3339Nano, fmt.Sprint(got[key]))
		w, werr := time.Parse(time.RFC3339Nano, fmt.Sprint(want[key]))
		if gerr != nil || werr != nil || !g.Equal(w) {
			t.Fatalf("%s is %v, want %v", key, got[key], want[key])
		}
		got[key] = want[key]
	}
}

// rewriteBody replaces the recorded answer's body with v.
func rewriteBody(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	rr.Body.Reset()
	rr.Body.Write(data)
}

func TestClientSecretExamples(t *testing.T) {
	pairs := clientSecretExamplePairs(t)
	routes := requirementRoutes(t)
	passwordOf := func(t *testing.T, pair oapifly.ExamplePair) string {
		t.Helper()
		var body struct {
			Password string `json:"password"`
		}
		if err := json.Unmarshal(pair.Request.Body, &body); err != nil || body.Password == "" {
			t.Fatalf("pair has no password: %s", pair.Request.Body)
		}
		return body.Password
	}
	clientSecrets := func(f *credentialFixture) int64 {
		f.t.Helper()
		var n int64
		if err := f.db.Model(&models.Credential{}).Where("kind = ?", models.CredentialKindClientSecret).Count(&n).Error; err != nil {
			f.t.Fatal(err)
		}
		return n
	}

	t.Run("createClientSecret/created", func(t *testing.T) {
		examplepairs.Proven(t, "createClientSecret", "created")
		pair := pairs["createClientSecret"]["created"]
		f := newCredentialFixture(t)
		administerExampleDomain(f)
		knownPassword(f, passwordOf(t, pair))
		session := twoFactorSession(f) // two_factor_session, with the pair's password
		started := time.Now()
		rr := sendClientSecretExample(f, routes["createClientSecret"], pair, session)
		finished := time.Now()
		if rr.Code != pair.Response.Status {
			t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
		}
		var got, want map[string]map[string]any
		decodeInto(t, rr, &got)
		if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
			t.Fatal(err)
		}
		data := got["data"]
		cred := reloadCredential(f, uint(data["id"].(float64)))
		if cred.Kind != models.CredentialKindClientSecret || cred.AccountID != f.mailboxID || data["public_id"] != cred.PublicID || data["client_id"] != cred.PublicID {
			t.Fatalf("answered %v for stored %+v", data, cred)
		}
		secret, _ := data["client_secret"].(string)
		if len(secret) != len(clientSecretExampleSecret) {
			t.Fatalf("client secret %q", secret)
		}
		if _, _, err := repositories.NewCredentialRepository(f.db).VerifyClientSecret(cred.PublicID, secret, time.Now()); err != nil {
			t.Fatalf("the answered secret does not verify: %v", err)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, data["created_at"].(string))
		if err != nil || createdAt.Before(started.Truncate(time.Second)) || createdAt.After(finished) {
			t.Fatalf("created_at %v", data["created_at"])
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, data["expires_at"].(string))
		if lifetime := expiresAt.Sub(createdAt); err != nil || lifetime < 90*24*time.Hour-time.Minute || lifetime > 90*24*time.Hour+time.Minute {
			t.Fatalf("expires_at %v, want 90 days after %v", data["expires_at"], data["created_at"])
		}
		for _, key := range []string{"id", "public_id", "client_id", "client_secret", "created_at", "expires_at"} {
			data[key] = want["data"][key]
		}
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
		if es := f.entries(handlers.ActionClientSecretCreate); len(es) != 1 {
			t.Fatalf("%d creation audit entries, want 1", len(es))
		}
	})

	t.Run("createClientSecret/password_only", func(t *testing.T) {
		examplepairs.Proven(t, "createClientSecret", "password_only")
		pair := pairs["createClientSecret"]["password_only"]
		f := newCredentialFixture(t)
		administerExampleDomain(f)
		session := signInWith(f, passwordOf(t, pair)) // without_capability: signed in with the password alone
		rr := sendClientSecretExample(f, routes["createClientSecret"], pair, session)
		assertRequirementResponse(t, pair, rr)
		if n := clientSecrets(f); n != 0 {
			t.Fatalf("refused creation stored %d client secrets", n)
		}
	})

	t.Run("createClientSecret/tier_refused", func(t *testing.T) {
		examplepairs.Proven(t, "createClientSecret", "tier_refused")
		pair := pairs["createClientSecret"]["tier_refused"]
		f := newCredentialFixture(t)
		administerExampleDomain(f)
		knownPassword(f, passwordOf(t, pair))
		session := twoFactorSession(f) // two_factor_session
		rr := sendClientSecretExample(f, routes["createClientSecret"], pair, session)
		assertRequirementResponse(t, pair, rr)
		if n := clientSecrets(f); n != 0 {
			t.Fatalf("refused creation stored %d client secrets", n)
		}
	})

	t.Run("listClientSecrets/listed", func(t *testing.T) {
		examplepairs.Proven(t, "listClientSecrets", "listed")
		pair := pairs["listClientSecrets"]["listed"]
		f := newCredentialFixture(t)
		clientSecret(f, nil, false) // client_secret
		rr := sendClientSecretExample(f, routes["listClientSecrets"], pair, f.session)
		if rr.Code != pair.Response.Status {
			t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
		}
		var got, want map[string][]map[string]any
		decodeInto(t, rr, &got)
		if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
			t.Fatal(err)
		}
		if len(got["data"]) != len(want["data"]) {
			t.Fatalf("listed %d client secrets, want %d", len(got["data"]), len(want["data"]))
		}
		for i := range got["data"] {
			sameInstant(t, got["data"][i], want["data"][i], "created_at", "expires_at")
		}
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
	})

	t.Run("revokeClientSecret/revoked", func(t *testing.T) {
		examplepairs.Proven(t, "revokeClientSecret", "revoked")
		pair := pairs["revokeClientSecret"]["revoked"]
		f := newCredentialFixture(t)
		cred := clientSecret(f, nil, false) // client_secret
		session := twoFactorSession(f)
		rr := sendClientSecretExample(f, routes["revokeClientSecret"], pair, session)
		assertRequirementResponse(t, pair, rr)
		if after := reloadCredential(f, cred.ID); after.RevokedAt == nil {
			t.Fatal("client secret not revoked")
		}
		if es := f.entries(handlers.ActionClientSecretRevoke); len(es) != 1 {
			t.Fatalf("%d revocation audit entries, want 1", len(es))
		}
	})

	t.Run("revokeClientSecret/missing", func(t *testing.T) {
		examplepairs.Proven(t, "revokeClientSecret", "missing")
		pair := pairs["revokeClientSecret"]["missing"]
		f := newCredentialFixture(t)
		session := twoFactorSession(f) // two_factor_session
		rr := sendClientSecretExample(f, routes["revokeClientSecret"], pair, session)
		assertRequirementResponse(t, pair, rr)
		// The audit middleware records failures only; the 404 is one.
		if es := f.entries(handlers.ActionClientSecretRevoke); len(es) != 1 || es[0].Outcome != models.AuditOutcomeFailed {
			t.Fatalf("refused revocation audit entries %+v, want one %q", es, models.AuditOutcomeFailed)
		}
	})

	t.Run("issueClientCredentialsToken/issued", func(t *testing.T) {
		examplepairs.Proven(t, "issueClientCredentialsToken", "issued")
		pair := pairs["issueClientCredentialsToken"]["issued"]
		f := newCredentialFixture(t)
		cred := usableClientSecret(f, false) // client_secret
		rr := sendClientSecretExample(f, routes["issueClientCredentialsToken"], pair, "")
		if rr.Code != pair.Response.Status {
			t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
		}
		var got, want map[string]any
		decodeInto(t, rr, &got)
		if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
			t.Fatal(err)
		}
		token, _ := got["access_token"].(string)
		claims, err := f.jwt.ValidateAccessToken(token)
		if err != nil || claims.AccountID != f.mailboxID || claims.MachineCredentialID != cred.ID || claims.TwoFactor {
			t.Fatalf("issued token is not the client secret's machine token: %+v (%v)", claims, err)
		}
		got["access_token"] = want["access_token"]
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
		if after := reloadCredential(f, cred.ID); after.LastUsedAt == nil {
			t.Fatal("client secret use not recorded")
		}
	})

	t.Run("issueClientCredentialsToken/wrong_secret", func(t *testing.T) {
		examplepairs.Proven(t, "issueClientCredentialsToken", "wrong_secret")
		pair := pairs["issueClientCredentialsToken"]["wrong_secret"]
		f := newCredentialFixture(t)
		cred := usableClientSecret(f, false) // client_secret
		rr := sendClientSecretExample(f, routes["issueClientCredentialsToken"], pair, "")
		assertRequirementResponse(t, pair, rr)
		if after := reloadCredential(f, cred.ID); after.LastUsedAt != nil {
			t.Fatal("refused exchange recorded a use")
		}
	})

	t.Run("issueClientCredentialsToken/revoked", func(t *testing.T) {
		examplepairs.Proven(t, "issueClientCredentialsToken", "revoked")
		pair := pairs["issueClientCredentialsToken"]["revoked"]
		f := newCredentialFixture(t)
		cred := usableClientSecret(f, true) // revoked_client_secret
		rr := sendClientSecretExample(f, routes["issueClientCredentialsToken"], pair, "")
		assertRequirementResponse(t, pair, rr)
		if after := reloadCredential(f, cred.ID); after.LastUsedAt != nil {
			t.Fatal("refused exchange recorded a use")
		}
	})
}
