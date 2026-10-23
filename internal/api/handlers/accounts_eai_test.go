package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// A hosted IDN is stored as an A-label today. Account provisioning, grants,
// linking and password/app-password sign-in must agree on that domain identity
// and on the NFC form of a UTF-8 local part.
func TestAccountsEAI(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	baseDomain := fx.domain.Name
	unicodeDomain := "städte." + baseDomain
	asciiDomain := "xn--stdte-hra." + baseDomain
	if err := db.Model(&fx.domain).Update("name", asciiDomain).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Grant{}).Where("holder_id = ?", fx.admin.ID).
		Update("scope", grants.Domain(asciiDomain)).Error; err != nil {
		t.Fatal(err)
	}
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	create := func(holder uint, address string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"address": address, "password": accountsPassword})
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts", holder, nil, string(body)))
		return rr
	}
	variants := []string{
		"MU\u0308NCHEN@" + unicodeDomain,
		"münchen@" + asciiDomain,
		"münchen@sta\u0308dte." + baseDomain,
	}
	for _, address := range variants {
		if rr := create(fx.user.ID, address); rr.Code != http.StatusForbidden {
			t.Fatalf("unprivileged creation %q: %d %s", address, rr.Code, rr.Body.String())
		}
	}
	rr := create(fx.admin.ID, variants[0])
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	account := decodeAccount(t, rr)
	want := "münchen@" + asciiDomain
	if account.Address != want {
		t.Fatalf("address = %q, want %q", account.Address, want)
	}
	var row models.Mailbox
	if err := db.First(&row, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.LocalPart != "münchen" || row.DomainID != fx.domain.ID {
		t.Fatalf("stored local part/domain = %q/%d", row.LocalPart, row.DomainID)
	}
	if _, err := mayReach(db, &auth.Claims{AccountID: fx.admin.ID}, account.ID, models.CapMailSend); err != nil {
		t.Fatalf("owner cannot send as the canonical account: %v", err)
	}
	for _, address := range variants {
		if rr := create(fx.admin.ID, address); rr.Code != http.StatusConflict {
			t.Errorf("duplicate %q: %d %s", address, rr.Code, rr.Body.String())
		}
	}

	jwt := auth.NewJWTService("eai-login-test", 15*time.Minute, 24*time.Hour)
	login := NewAuthHandler(db, jwt, "", nil)
	t.Cleanup(func() { db.Where("account_id = ?", account.ID).Delete(&models.RefreshToken{}) })
	repo := repositories.NewCredentialRepository(db)
	_, appPassword, err := repo.CreateAppPassword(account.ID, account.ID, "EAI test", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range variants {
		if rr := doLogin(login, map[string]string{"email": address, "password": accountsPassword}); rr.Code != http.StatusOK {
			t.Errorf("login %q: %d %s", address, rr.Code, rr.Body.String())
		}
		for _, password := range []string{accountsPassword, appPassword} {
			_, mailbox, err := repo.VerifyBasic(address, password, time.Now(), nil)
			if err != nil || mailbox == nil || mailbox.ID != account.ID {
				t.Fatalf("protocol sign-in %q: mailbox=%v, err=%v", address, mailbox, err)
			}
		}
	}
	// A different UTF-8 local part never becomes equivalent to this one.
	for _, address := range []string{"munchen@" + unicodeDomain, "münchen@elsewhere.test", "bad@@" + asciiDomain} {
		if rr := doLogin(login, map[string]string{"email": address, "password": accountsPassword}); rr.Code != http.StatusUnauthorized {
			t.Errorf("wrong identity %q: %d %s", address, rr.Code, rr.Body.String())
		}
		if _, _, err := repo.VerifyBasic(address, appPassword, time.Now(), nil); err == nil {
			t.Errorf("app password accepted for wrong identity %q", address)
		}
	}
	for i, address := range variants {
		body, _ := json.Marshal(map[string]string{"address": address, "password": accountsPassword})
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts/link", fx.user.ID, nil, string(body)))
		wantStatus := http.StatusConflict
		if i == 0 {
			wantStatus = http.StatusCreated
		}
		if rr.Code != wantStatus {
			t.Errorf("link %q: %d %s, want %d", address, rr.Code, rr.Body.String(), wantStatus)
		}
	}
}

func TestAccountsEAILegacy(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	repo := repositories.NewCredentialRepository(db)
	seed := func(local, password string) models.Mailbox {
		t.Helper()
		mb := models.Mailbox{DomainID: fx.domain.ID, LocalPart: local, Address: local + "@" + fx.domain.Name, Active: true}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatal(err)
		}
		hash, err := hashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SetPassword(mb.ID, hash, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		return mb
	}
	legacy := seed("mu\u0308nchen", accountsPassword)
	canonical := "münchen@" + fx.domain.Name
	for _, address := range []string{legacy.Address, canonical} {
		_, mb, err := repo.VerifyBasic(address, accountsPassword, time.Now(), nil)
		if err != nil || mb == nil || mb.ID != legacy.ID || mb.Address != legacy.Address {
			t.Fatalf("legacy sign-in %q: %v, %v", address, mb, err)
		}
	}
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	body, _ := json.Marshal(map[string]string{"address": canonical})
	rr := httptest.NewRecorder()
	accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts", fx.admin.ID, nil, string(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("NFC duplicate of legacy account: %d %s", rr.Code, rr.Body.String())
	}
	body, _ = json.Marshal(map[string]string{"address": canonical, "password": accountsPassword})
	rr = httptest.NewRecorder()
	accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts/link", fx.admin.ID, nil, string(body)))
	if rr.Code != http.StatusCreated || decodeAccount(t, rr).ID != legacy.ID {
		t.Fatalf("link legacy account: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := mayReach(db, &auth.Claims{AccountID: fx.admin.ID}, legacy.ID, models.CapMailSend); err != nil {
		t.Fatalf("legacy owner grant stopped matching: %v", err)
	}

	// Older databases can contain both spellings. Preserve their distinct
	// identities; neither a password nor an app password may cross between them.
	twin := seed("münchen", "different-password")
	_, appPassword, err := repo.CreateAppPassword(legacy.ID, legacy.ID, "legacy client", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		address, password string
		want              uint
	}{
		{legacy.Address, accountsPassword, legacy.ID},
		{twin.Address, "different-password", twin.ID},
		{legacy.Address, appPassword, legacy.ID},
		{twin.Address, accountsPassword, 0},
		{legacy.Address, "different-password", 0},
		{twin.Address, appPassword, 0},
	} {
		_, mb, err := repo.VerifyBasic(tc.address, tc.password, time.Now(), nil)
		if tc.want == 0 {
			if err == nil {
				t.Errorf("credential crossed identities for %q", tc.address)
			}
		} else if err != nil || mb == nil || mb.ID != tc.want {
			t.Errorf("exact identity %q: %v, %v; want %d", tc.address, mb, err, tc.want)
		}
	}
	// Without an exact spelling, two old canonically equivalent addresses are
	// ambiguous. Refuse instead of picking whichever row the database returns.
	seed("a\u0301\u0323", accountsPassword)
	seed("a\u0323\u0301", accountsPassword)
	var mb models.Mailbox
	if err := repositories.FindMailboxByAddress(db, "ạ\u0301@"+fx.domain.Name, &mb); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("ambiguous equivalent resolved to %+v, %v", mb, err)
	}
}
