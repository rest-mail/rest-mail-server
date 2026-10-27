package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// TestCreateOwnedAccount: an owned account (an agent's, or a sub account) is
// created only with agents:create covering its domain, from a session that
// signed in with a second factor; machine credentials and password-only
// sessions are refused. The creator becomes its owner (#341, Decided 12).
func TestCreateOwnedAccount(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	create := func(claims *auth.Claims, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, claims))
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, r)
		return rr
	}
	twoFactor := func(id uint) *auth.Claims {
		return &auth.Claims{AccountID: id, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true}
	}
	body := fmt.Sprintf(`{"address":%q,"owned":true}`, "assistant@"+fx.domain.Name)

	if rr := create(twoFactor(fx.user.ID), body); rr.Code != http.StatusForbidden {
		t.Fatalf("without agents:create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := create(&auth.Claims{AccountID: fx.admin.ID, AMR: []string{auth.AMRPassword}}, body); rr.Code != http.StatusForbidden {
		t.Fatalf("a password-only session: %d %s", rr.Code, rr.Body.String())
	} else if code := errorCode(rr); code != "two_factor_required" {
		t.Fatalf("a password-only session: code %q", code)
	}

	rr := create(twoFactor(fx.admin.ID), body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	got := decodeAccount(t, rr)
	if got.OwnerID == nil || *got.OwnerID != fx.admin.ID {
		t.Fatalf("owner_id = %v, want %d", got.OwnerID, fx.admin.ID)
	}
	var stored models.Mailbox
	if err := db.First(&stored, got.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.OwnerID == nil || *stored.OwnerID != fx.admin.ID {
		t.Fatalf("stored owner = %v", stored.OwnerID)
	}

	// An ordinary account has no owner.
	rr = create(twoFactor(fx.admin.ID), fmt.Sprintf(`{"address":%q}`, "plain@"+fx.domain.Name))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create plain: %d %s", rr.Code, rr.Body.String())
	}
	if plain := decodeAccount(t, rr); plain.OwnerID != nil {
		t.Errorf("a plain account has owner %v", *plain.OwnerID)
	}
}

// TestOwnedAccountCeiling: what an owned account holds is its own grants and
// mailbox, cut down live to what its owner holds. Narrowing or removing the
// owner's access narrows the account's at once; an inactive owner leaves it
// holding nothing.
func TestOwnedAccountCeiling(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	owner := fx.admin.ID
	agent := models.Mailbox{DomainID: fx.domain.ID, LocalPart: "agent", Address: "agent@" + fx.domain.Name, Active: true, OwnerID: &owner}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	role := func(name string) uint {
		var r models.Role
		if err := db.Where("name = ?", name).First(&r).Error; err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	grant := func(holder uint, roleName, scope string) models.Grant {
		g := models.Grant{HolderKind: models.GrantHolderAccount, HolderID: holder, RoleID: role(roleName), Scope: scope,
			Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}
		if err := db.Create(&g).Error; err != nil {
			t.Fatal(err)
		}
		return g
	}
	// The owner owns the agent's mailbox, as creating it grants.
	grant(owner, models.RoleOwner, grants.Mailbox(agent.Address))
	// The agent is given more than its owner holds: readonly everywhere.
	grant(agent.ID, "readonly", grants.All)

	held, err := grants.Load(db, agent.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	domain := grants.Domain(fx.domain.Name)
	if !held.Allows(middleware.CapDomainsRead, domain) {
		t.Error("the agent cannot read its owner's domain")
	}
	if held.Allows(middleware.CapDomainsRead, grants.All) {
		t.Error("the agent reads every domain, beyond its owner's")
	}
	if !held.Allows(models.CapMailSend, grants.Mailbox(agent.Address)) {
		t.Error("the agent cannot send from its own mailbox")
	}

	// The owner loses the domain admin grant: the agent loses it at once.
	db.Where("holder_id = ? AND scope = ?", owner, domain).Delete(&models.Grant{})
	if held, err = grants.Load(db, agent.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if held.Allows(middleware.CapDomainsRead, domain) {
		t.Error("the agent still reads the domain after its owner lost it")
	}
	if !held.Allows(models.CapMailRead, grants.Mailbox(agent.Address)) {
		t.Error("the agent lost its own mailbox, which its owner still owns")
	}

	// An inactive owner leaves the agent holding nothing.
	db.Model(&models.Mailbox{}).Where("id = ?", owner).Update("active", false)
	if held, err = grants.Load(db, agent.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if held.AllowsSomewhere(models.CapMailRead) || held.AllowsSomewhere(middleware.CapDomainsRead) {
		t.Errorf("the agent of an inactive owner still holds %v", held.ScopedCapabilities())
	}
}
