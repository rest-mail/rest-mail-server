package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

func TestIdentityOwnershipAndGrantCeilings(t *testing.T) {
	db := openAccessDB(t)
	router := identityRouter(t, db)
	accounts := seedAdmins(t, db, []fixtureAdmin{
		{name: "top"}, {name: "middle"}, {name: "leaf"}, {name: "sibling"}, {name: "independent"},
	})
	top, middle, leaf := accounts["top"].user, accounts["middle"].user, accounts["leaf"].user
	sibling, independent := accounts["sibling"].user, accounts["independent"].user
	var role models.Role
	if err := db.Where("name = ?", models.RoleOwner).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	link := func(owner, child models.Mailbox) models.Grant {
		t.Helper()
		if err := db.Model(&child).Update("owner_id", owner.ID).Error; err != nil {
			t.Fatal(err)
		}
		grant := models.Grant{
			HolderKind: models.GrantHolderAccount, HolderID: owner.ID, RoleID: role.ID,
			Scope: grants.Mailbox(child.Address), Mode: models.GrantActive,
			GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now(),
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatal(err)
		}
		return grant
	}
	link(top, middle)
	leafGrant := link(middle, leaf)
	// This is an owned mailbox but its owner has no grant over it yet.
	if err := db.Model(&sibling).Update("owner_id", middle.ID).Error; err != nil {
		t.Fatal(err)
	}

	signIn := func(account models.Mailbox) string {
		t.Helper()
		return identitySession(t, identityLogin(router, account.Address, accessPassword, ""), account.Address).AccessToken
	}
	topSession, leafSession, independentSession := signIn(top), signIn(leaf), signIn(independent)
	path := func(id uint) string { return fmt.Sprintf("/api/v1/accounts/%d/folders", id) }
	folders := func(session string, id uint) *httptest.ResponseRecorder {
		return doRequest(router, http.MethodGet, path(id), session, "")
	}
	const marker = "OwnershipProof"
	identityStatus(t, doRequest(router, http.MethodPost, path(leaf.ID), leafSession, `{"name":"OwnershipProof"}`), http.StatusCreated, "")
	requireLeafFolders := func(t *testing.T, session string) {
		t.Helper()
		rr := folders(session, leaf.ID)
		identityStatus(t, rr, http.StatusOK, "")
		var result struct{ Data []apitypes.FolderSummary }
		decodeInto(t, rr, &result)
		if len(result.Data) != 5 || !slices.ContainsFunc(result.Data, func(folder apitypes.FolderSummary) bool { return folder.Folder == marker }) {
			t.Fatalf("expected the leaf's marker and four standard folders, got %+v", result.Data)
		}
	}
	hidden := func(t *testing.T, session string, id uint) {
		t.Helper()
		rr, missing := folders(session, id), folders(session, 2147483647)
		identityStatus(t, rr, http.StatusNotFound, "not_found")
		identityStatus(t, missing, http.StatusNotFound, "not_found")
		if rr.Body.String() != missing.Body.String() {
			t.Fatalf("hidden mailbox can be distinguished from a missing mailbox: %s / %s", rr.Body, missing.Body)
		}
	}

	t.Run("independent_account_hidden_in_both_directions", func(t *testing.T) {
		identityStatus(t, folders(independentSession, independent.ID), http.StatusOK, "")
		requireLeafFolders(t, leafSession)
		hidden(t, independentSession, leaf.ID)
		hidden(t, topSession, independent.ID)
	})
	t.Run("transitive_grant_covers_only_its_current_scope", func(t *testing.T) {
		requireLeafFolders(t, topSession)
		hidden(t, topSession, sibling.ID)
		if err := db.Model(&leafGrant).Update("scope", grants.Mailbox(sibling.Address)).Error; err != nil {
			t.Fatal(err)
		}
		identityStatus(t, folders(topSession, sibling.ID), http.StatusOK, "")
		hidden(t, topSession, leaf.ID)
		identityStatus(t, folders(leafSession, leaf.ID), http.StatusForbidden, "forbidden")
		if err := db.Model(&leafGrant).Update("scope", grants.Mailbox(leaf.Address)).Error; err != nil {
			t.Fatal(err)
		}
		requireLeafFolders(t, topSession)
	})
	t.Run("inactive_intermediate_owner_cuts_the_link", func(t *testing.T) {
		if err := db.Model(&middle).Update("active", false).Error; err != nil {
			t.Fatal(err)
		}
		hidden(t, topSession, leaf.ID)
		identityStatus(t, folders(leafSession, leaf.ID), http.StatusForbidden, "forbidden")
		identityStatus(t, folders(topSession, top.ID), http.StatusOK, "")
		if err := db.Model(&middle).Update("active", true).Error; err != nil {
			t.Fatal(err)
		}
		requireLeafFolders(t, topSession)
	})
	t.Run("server_ceiling_refuses_read_on_a_visible_mailbox", func(t *testing.T) {
		names, overridden, err := grants.ServerCeiling(db, time.Now())
		if err != nil || overridden || !slices.Contains(names, models.CapMailRead) {
			t.Fatalf("expected a stored ceiling allowing reads, got %v, overridden=%v, err=%v", names, overridden, err)
		}
		names = slices.DeleteFunc(names, func(name string) bool { return name == models.CapMailRead })
		if err := db.Transaction(func(tx *gorm.DB) error {
			_, err := grants.SetServerCeiling(tx, names, models.AuditEntry{ActorKind: models.AuditActorSystem})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		identityStatus(t, folders(topSession, leaf.ID), http.StatusForbidden, "forbidden")
		hidden(t, topSession, independent.ID)
	})
}
