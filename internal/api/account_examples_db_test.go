package api

import (
	"encoding/json"
	"maps"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// The account and credential pairs that create, link or look up an account run the
// real router on a scratch database holding the state each names, and check what
// the request changed as well as the answer (#635). Where the server chooses an id,
// a secret or a time, the answer is checked against what it stored and given the
// pair's value before the body is compared. They skip, and prove nothing, when no
// database is reachable.

// accountExampleLinkPassword is eve@restmail.test's password, which the live run's
// hook sends in place of the pair's supplied-by-contract-hook.
const accountExampleLinkPassword = "linkable-password-123"

// sendAccountExample sends the pair's request, path and body, as it is.
func sendAccountExample(f *credentialFixture, route requirementRoute, pair oapifly.ExamplePair, authorization string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(route.method, messageCollectionReadURI(route.path, pair), authorization, string(pair.Request.Body))
}

// onOwnAccount is pair sent for the fixture's own account, as the live run sends a
// pair that names no account.
func onOwnAccount(f *credentialFixture, pair oapifly.ExamplePair) oapifly.ExamplePair {
	path := maps.Clone(pair.Request.Path)
	if path == nil {
		path = map[string]any{}
	}
	path["id"] = float64(f.accountID)
	pair.Request.Path = path
	return pair
}

// withBodyField is pair with key of its request body set to value, as the live run's
// hooks set the fixture's secrets.
func withBodyField(t *testing.T, pair oapifly.ExamplePair, key string, value any) oapifly.ExamplePair {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	body[key] = value
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	pair.Request.Body = raw
	return pair
}

// restmailTestDomain hosts restmail.test, the pairs' domain, and gives chris@ the
// admin role on it when administer is set.
func restmailTestDomain(f *credentialFixture, administer bool) uint {
	f.t.Helper()
	domain := models.Domain{Name: "restmail.test", Active: true}
	if err := f.db.Create(&domain).Error; err != nil {
		f.t.Fatalf("seed domain: %v", err)
	}
	if administer {
		var admin models.Role
		if err := f.db.Where("name = ?", "admin").First(&admin).Error; err != nil {
			f.t.Fatal(err)
		}
		if err := f.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: f.mailboxID, RoleID: admin.ID,
			Scope: grants.Domain("restmail.test"), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
			f.t.Fatal(err)
		}
	}
	return domain.ID
}

// restmailTestAccount is an active account local@restmail.test with password.
func restmailTestAccount(f *credentialFixture, domainID uint, local, displayName, password string) uint {
	f.t.Helper()
	mb := models.Mailbox{DomainID: domainID, LocalPart: local, Address: local + "@restmail.test", DisplayName: displayName, Active: true}
	if err := f.db.Create(&mb).Error; err != nil {
		f.t.Fatalf("seed account: %v", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := repositories.NewCredentialRepository(f.db).SetPassword(mb.ID, hash, nil, time.Now()); err != nil {
		f.t.Fatalf("seed password: %v", err)
	}
	return mb.ID
}

// ownsMailbox reports whether chris@ holds a grant at address's own scope.
func ownsMailbox(f *credentialFixture, address string) bool {
	f.t.Helper()
	var n int64
	if err := f.db.Model(&models.Grant{}).Where("holder_kind = ? AND holder_id = ? AND scope = ?",
		models.GrantHolderAccount, f.mailboxID, grants.Mailbox(address)).Count(&n).Error; err != nil {
		f.t.Fatal(err)
	}
	return n > 0
}

// createdData is the answer's data and the pair's, decoded, after the answer's
// status is checked.
func createdData(t *testing.T, pair oapifly.ExamplePair, rr *httptest.ResponseRecorder) (got, want map[string]map[string]any) {
	t.Helper()
	if rr.Code != pair.Response.Status {
		t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
	}
	decodeInto(t, rr, &got)
	if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
		t.Fatal(err)
	}
	return got, want
}

// createdJustNow checks that data's created_at is between started and finished.
func createdJustNow(t *testing.T, data map[string]any, started, finished time.Time) time.Time {
	t.Helper()
	text, _ := data["created_at"].(string)
	createdAt, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || createdAt.Before(started.Truncate(time.Second)) || createdAt.After(finished) {
		t.Fatalf("created_at %v", data["created_at"])
	}
	return createdAt
}

// anotherMailbox is a second account in the fixture mailbox's domain.
func anotherMailbox(f *credentialFixture) uint {
	f.t.Helper()
	var mailbox models.Mailbox
	if err := f.db.First(&mailbox, f.mailboxID).Error; err != nil {
		f.t.Fatal(err)
	}
	return f.seedMailbox(mailbox.DomainID, "someone-else")
}

func TestAccountExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	credentials := func(f *credentialFixture, kind string) int64 {
		f.t.Helper()
		var n int64
		if err := f.db.Model(&models.Credential{}).Where("kind = ?", kind).Count(&n).Error; err != nil {
			f.t.Fatal(err)
		}
		return n
	}

	t.Run("createAPIKey/created", func(t *testing.T) {
		examplepairs.Proven(t, "createAPIKey", "created")
		pair := pairs["createAPIKey"]["created"]
		f := newCredentialFixture(t)
		f.own(0, "robot@example.com") // the key's mail:read at mailbox:robot@example.com is within chris@'s grants
		knownPassword(f, "current-password")
		session := twoFactorSession(f)
		started := time.Now()
		rr := sendAccountExample(f, routes["createAPIKey"], onOwnAccount(f, pair), session)
		finished := time.Now()
		got, want := createdData(t, pair, rr)
		data := got["data"]
		id, _ := data["id"].(float64)
		cred := reloadCredential(f, uint(id))
		if cred.Kind != models.CredentialKindAPIKey || cred.AccountID != f.mailboxID || data["public_id"] != cred.PublicID {
			t.Fatalf("answered %v for stored %+v", data, cred)
		}
		token, _ := data["token"].(string)
		wantToken, _ := want["data"]["token"].(string)
		if !strings.HasPrefix(token, "rmk_"+cred.PublicID+"_") || len(token) != len(wantToken) {
			t.Fatalf("token %q", token)
		}
		createdAt := createdJustNow(t, data, started, finished)
		expiresAt, err := time.Parse(time.RFC3339Nano, data["expires_at"].(string))
		if lifetime := expiresAt.Sub(createdAt); err != nil || lifetime < 90*24*time.Hour-time.Minute || lifetime > 90*24*time.Hour+time.Minute {
			t.Fatalf("expires_at %v, want 90 days after %v", data["expires_at"], data["created_at"])
		}
		for _, key := range []string{"id", "public_id", "created_at", "expires_at", "token"} {
			data[key] = want["data"][key]
		}
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
	})

	t.Run("createAPIKey/missing", func(t *testing.T) {
		examplepairs.Proven(t, "createAPIKey", "missing")
		pair := pairs["createAPIKey"]["missing"]
		f := newCredentialFixture(t)
		knownPassword(f, "current-password")
		rr := sendAccountExample(f, routes["createAPIKey"], pair, twoFactorSession(f))
		assertRequirementResponse(t, pair, rr)
		if n := credentials(f, models.CredentialKindAPIKey); n != 0 {
			t.Fatalf("refused creation stored %d API keys", n)
		}
	})

	// keyToEdit is a key of the fixture account's, made as createAPIKey makes one, holding
	// mail:read at robot@example.com, which the fixture account owns.
	keyToEdit := func(f *credentialFixture) (*models.Credential, string) {
		f.t.Helper()
		f.own(0, "robot@example.com")
		knownPassword(f, "current-password")
		cred, secret, err := repositories.NewCredentialRepository(f.db).CreateAPIKey(f.mailboxID, f.mailboxID, "Invoice robot",
			[]auth.ScopedPermission{{Capability: "mail:read", Scope: "mailbox:robot@example.com"}}, nil)
		if err != nil {
			f.t.Fatal(err)
		}
		return cred, secret
	}
	onKey := func(f *credentialFixture, pair oapifly.ExamplePair, id uint) oapifly.ExamplePair {
		pair = onOwnAccount(f, pair)
		pair.Request.Path["credential_id"] = float64(id)
		return pair
	}
	unchanged := func(f *credentialFixture, cred *models.Credential) {
		f.t.Helper()
		stored := reloadCredential(f, cred.ID)
		if len(stored.Scope.Permissions) != 1 || stored.Scope.Permissions[0].Capability != "mail:read" {
			f.t.Fatalf("a refused change stored %+v", stored.Scope.Permissions)
		}
	}

	t.Run("updateAPIKey/updated", func(t *testing.T) {
		examplepairs.Proven(t, "updateAPIKey", "updated")
		pair := pairs["updateAPIKey"]["updated"]
		f := newCredentialFixture(t)
		cred, secret := keyToEdit(f)
		rr := sendAccountExample(f, routes["updateAPIKey"], onKey(f, pair, cred.ID), twoFactorSession(f))
		if rr.Code != pair.Response.Status {
			t.Fatalf("got %d %s", rr.Code, rr.Body.String())
		}
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control %q", rr.Header().Get("Cache-Control"))
		}
		var got, want map[string]map[string]any
		decodeInto(t, rr, &got)
		if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
			t.Fatal(err)
		}
		data := got["data"]
		if _, leaked := data["token"]; leaked {
			t.Fatal("the answer carried a secret")
		}
		if id, _ := data["id"].(float64); uint(id) != cred.ID || data["public_id"] != cred.PublicID {
			t.Fatalf("answered %v for %+v", data, cred)
		}
		stored := reloadCredential(f, cred.ID)
		if stored.SecretHash != cred.SecretHash || stored.Name != cred.Name || stored.PublicID != cred.PublicID {
			t.Fatalf("the key changed beyond its permissions: %+v", stored)
		}
		wantStored := []auth.ScopedPermission{{Capability: "mail:send", Scope: "mailbox:robot@example.com"}}
		if len(stored.Scope.Permissions) != 1 || stored.Scope.Permissions[0] != wantStored[0] || stored.Scope.MailboxID != f.mailboxID {
			t.Fatalf("stored %+v", stored.Scope)
		}
		// The same secret still verifies, and what it runs with is the new permission.
		verified, _, err := repositories.NewCredentialRepository(f.db).VerifyAPIKey(secret, time.Now())
		if err != nil {
			t.Fatalf("the secret stopped working: %v", err)
		}
		if verified.Scope.Permissions[0] != wantStored[0] {
			t.Fatalf("the next request would run with %+v", verified.Scope.Permissions)
		}
		entries := f.entries("api_keys.update")
		if len(entries) != 1 {
			t.Fatalf("%d audit entries, want 1", len(entries))
		}
		entry := entries[0]
		if entry.Target["previous_permissions"] != `[{"capability":"mail:read","scope":"mailbox:robot@example.com"}]` ||
			entry.Target["permissions"] != `[{"capability":"mail:send","scope":"mailbox:robot@example.com"}]` ||
			entry.ActorID == nil || *entry.ActorID != f.mailboxID {
			t.Fatalf("audit entry %+v", entry)
		}
		for _, key := range []string{"id", "public_id", "created_at", "expires_at"} {
			data[key] = want["data"][key]
		}
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
	})

	for _, name := range []string{"missing", "already_revoked", "another_accounts_key", "app_password", "wrong_password", "beyond_grants"} {
		t.Run("updateAPIKey/"+name, func(t *testing.T) {
			examplepairs.Proven(t, "updateAPIKey", name)
			pair := pairs["updateAPIKey"][name]
			f := newCredentialFixture(t)
			cred, _ := keyToEdit(f)
			repo := repositories.NewCredentialRepository(f.db)
			target := cred.ID
			switch name {
			case "missing":
				target = 2147483647
			case "already_revoked":
				if err := repo.RevokeAPIKey(f.mailboxID, cred.ID, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "another_accounts_key":
				other, _, err := repo.CreateAPIKey(anotherMailbox(f), f.mailboxID, "Someone else's", []auth.ScopedPermission{{Capability: "mail:read", Scope: "mailbox:robot@example.com"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				target = other.ID
			case "app_password":
				password, _, err := repo.CreateAppPassword(f.mailboxID, f.mailboxID, "laptop", nil)
				if err != nil {
					t.Fatal(err)
				}
				target = password.ID
			}
			rr := sendAccountExample(f, routes["updateAPIKey"], onKey(f, pair, target), twoFactorSession(f))
			assertRequirementResponse(t, pair, rr)
			if name != "already_revoked" {
				unchanged(f, cred)
			}
			var n int64
			if err := f.db.Model(&models.AuditEntry{}).Where("action = ? AND outcome = ?", "api_keys.update", "success").Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("a refused change was audited as done %d times", n)
			}
		})
	}

	t.Run("createAccount/created", func(t *testing.T) {
		examplepairs.Proven(t, "createAccount", "created")
		pair := pairs["createAccount"]["created"]
		f := newCredentialFixture(t)
		restmailTestDomain(f, true) // chris@ administers restmail.test
		rr := sendAccountExample(f, routes["createAccount"], pair, f.session)
		got, want := createdData(t, pair, rr)
		data := got["data"]
		var account models.Mailbox
		if err := f.db.Where("address = ?", "new-account@restmail.test").First(&account).Error; err != nil {
			t.Fatalf("the account was not stored: %v", err)
		}
		if id, _ := data["id"].(float64); uint(id) != account.ID || account.DisplayName != "New Account" || !account.Active {
			t.Fatalf("answered %v for stored %+v", data, account)
		}
		if cred, err := repositories.NewCredentialRepository(f.db).MatchPassword(account.ID, "correct-horse-battery", time.Now()); err != nil || cred == nil {
			t.Fatalf("the account's password does not match (%v)", err)
		}
		if !ownsMailbox(f, account.Address) {
			t.Fatal("the creator was not given the account")
		}
		data["id"] = want["data"]["id"]
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
	})

	t.Run("createAccount/already_exists", func(t *testing.T) {
		examplepairs.Proven(t, "createAccount", "already_exists")
		pair := pairs["createAccount"]["already_exists"]
		f := newCredentialFixture(t)
		domainID := restmailTestDomain(f, true)
		restmailTestAccount(f, domainID, "admin", "Admin", "admin-password-123")
		rr := sendAccountExample(f, routes["createAccount"], pair, f.session)
		assertRequirementResponse(t, pair, rr)
		var n int64
		if err := f.db.Model(&models.Mailbox{}).Where("address = ?", "admin@restmail.test").Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("%d accounts at admin@restmail.test (%v), want 1", n, err)
		}
		if ownsMailbox(f, "admin@restmail.test") {
			t.Fatal("a refused creation gave the creator the account")
		}
	})

	t.Run("createAppPassword/created", func(t *testing.T) {
		examplepairs.Proven(t, "createAppPassword", "created")
		pair := pairs["createAppPassword"]["created"]
		f := newCredentialFixture(t)
		started := time.Now()
		// The live run's hook sends the mailbox's current password with every request.
		rr := sendAccountExample(f, routes["createAppPassword"], withBodyField(t, onOwnAccount(f, pair), "password", f.password), f.session)
		finished := time.Now()
		got, want := createdData(t, pair, rr)
		data := got["data"]
		id, _ := data["id"].(float64)
		cred := reloadCredential(f, uint(id))
		if cred.Kind != models.CredentialKindAppPassword || cred.AccountID != f.mailboxID || data["public_id"] != cred.PublicID {
			t.Fatalf("answered %v for stored %+v", data, cred)
		}
		password, _ := data["password"].(string)
		wantPassword, _ := want["data"]["password"].(string)
		if !strings.HasPrefix(password, "rm_"+cred.PublicID+"_") || len(password) != len(wantPassword) {
			t.Fatalf("app password %q", password)
		}
		createdJustNow(t, data, started, finished)
		for _, key := range []string{"id", "public_id", "created_at", "password"} {
			data[key] = want["data"][key]
		}
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
		if es := f.entries("credentials.create"); len(es) != 1 || es[0].Outcome != models.AuditOutcomeSuccess {
			t.Fatalf("creation audit entries %+v, want one %q", es, models.AuditOutcomeSuccess)
		}
	})

	t.Run("createAppPassword/missing", func(t *testing.T) {
		examplepairs.Proven(t, "createAppPassword", "missing")
		pair := pairs["createAppPassword"]["missing"]
		f := newCredentialFixture(t)
		rr := sendAccountExample(f, routes["createAppPassword"], withBodyField(t, pair, "password", f.password), f.session)
		assertRequirementResponse(t, pair, rr)
		if n := credentials(f, models.CredentialKindAppPassword); n != 0 {
			t.Fatalf("refused creation stored %d app passwords", n)
		}
	})

	t.Run("createAppPassword/password_only", func(t *testing.T) {
		examplepairs.Proven(t, "createAppPassword", "password_only")
		pair := pairs["createAppPassword"]["password_only"]
		f := newCredentialFixture(t)
		// enrolled_authenticator: the mailbox has 2FA, which the fixture's session,
		// signed in before it was on, did not take.
		if _, err := repositories.NewTwoFactorRepository(f.db).Enroll(f.mailboxID, "x", nil); err != nil {
			t.Fatalf("seed 2FA: %v", err)
		}
		if err := f.db.Model(&models.Credential{}).Where("account_id = ? AND kind = ?", f.mailboxID, models.CredentialKindTOTP).Update("confirmed_at", time.Now()).Error; err != nil {
			t.Fatal(err)
		}
		passwordOnly, err := f.jwt.GenerateSession(f.accountID, f.address, 0, []string{auth.AMRPassword}, false)
		if err != nil {
			t.Fatal(err)
		}
		rr := sendAccountExample(f, routes["createAppPassword"], withBodyField(t, onOwnAccount(f, pair), "password", f.password), "Bearer "+passwordOnly.AccessToken)
		assertRequirementResponse(t, pair, rr)
		if n := credentials(f, models.CredentialKindAppPassword); n != 0 {
			t.Fatalf("refused creation stored %d app passwords", n)
		}
	})

	t.Run("linkAccount/linked", func(t *testing.T) {
		examplepairs.Proven(t, "linkAccount", "linked")
		pair := pairs["linkAccount"]["linked"]
		f := newCredentialFixture(t)
		eve := restmailTestAccount(f, restmailTestDomain(f, false), "eve", "Eve", accountExampleLinkPassword)
		rr := sendAccountExample(f, routes["linkAccount"], withBodyField(t, pair, "password", accountExampleLinkPassword), f.session)
		got, want := createdData(t, pair, rr)
		data := got["data"]
		if id, _ := data["id"].(float64); uint(id) != eve {
			t.Fatalf("answered %v for eve@, id %d", data, eve)
		}
		if !ownsMailbox(f, "eve@restmail.test") {
			t.Fatal("the link gave chris@ no grant on eve@")
		}
		data["id"] = want["data"]["id"]
		rewriteBody(t, rr, got)
		assertRequirementResponse(t, pair, rr)
	})

	t.Run("linkAccount/wrong_password", func(t *testing.T) {
		examplepairs.Proven(t, "linkAccount", "wrong_password")
		pair := pairs["linkAccount"]["wrong_password"]
		f := newCredentialFixture(t)
		restmailTestAccount(f, restmailTestDomain(f, false), "eve", "Eve", accountExampleLinkPassword)
		rr := sendAccountExample(f, routes["linkAccount"], pair, f.session)
		assertRequirementResponse(t, pair, rr)
		if ownsMailbox(f, "eve@restmail.test") {
			t.Fatal("a refused proof linked eve@")
		}
	})

	t.Run("unlinkAccount/missing", func(t *testing.T) {
		examplepairs.Proven(t, "unlinkAccount", "missing")
		pair := pairs["unlinkAccount"]["missing"]
		f := newCredentialFixture(t)
		var before int64
		f.db.Model(&models.Grant{}).Count(&before)
		rr := sendAccountExample(f, routes["unlinkAccount"], pair, f.session)
		assertRequirementResponse(t, pair, rr)
		var after int64
		f.db.Model(&models.Grant{}).Count(&after)
		if after != before {
			t.Fatalf("a refused unlink changed the grants: %d, then %d", before, after)
		}
	})
}
