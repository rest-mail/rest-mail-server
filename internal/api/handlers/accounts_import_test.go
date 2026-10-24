package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
)

const accountImportSHA512 = "{SHA512-CRYPT}$6$importsalt$ObInO4qcr134i8FQT.XNC6Ma04n9aRTAUNHmm8Yb1idoDBOXKoDTmdosY1xwfageGXo4ZSSrkIPRk9zMWfsG7/"

func TestAccountImportRefusesInvalidHashBeforeDatabase(t *testing.T) {
	h := NewAccountHandler(nil, nil, "")
	for _, body := range []string{
		`{"address":"import@example.test","password_hash":"{PLAIN}weak"}`,
		fmt.Sprintf(`{"address":"import@example.test","password":"long-enough-password","password_hash":%q}`, accountImportSHA512),
	} {
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts", 1, nil, body))
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "password_hash") {
			t.Fatalf("got %d %s", rr.Code, rr.Body)
		}
		if strings.Contains(rr.Body.String(), "importsalt") {
			t.Fatal("a validation refusal disclosed the hash")
		}
	}
}

func TestAccountImportPasswordHashAndProtocolSignIn(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	for _, tc := range []struct{ name, hash, password string }{
		{"sha512", accountImportSHA512, "old-password"},
		{"bcrypt", "", "old-bcrypt-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hash == "" {
				var err error
				tc.hash, err = hashPassword(tc.password)
				if err != nil {
					t.Fatal(err)
				}
			}
			body := fmt.Sprintf(`{"address":%q,"password_hash":%q}`, tc.name+"@"+fx.domain.Name, tc.hash)
			call := func(id uint) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts", id, nil, body))
				return rr
			}
			if rr := call(fx.user.ID); rr.Code != http.StatusForbidden {
				t.Fatalf("unprivileged import: %d %s", rr.Code, rr.Body)
			}
			rr := call(fx.admin.ID)
			if rr.Code != http.StatusCreated {
				t.Fatalf("import: %d %s", rr.Code, rr.Body)
			}
			account := decodeAccount(t, rr)
			if strings.Contains(rr.Body.String(), tc.hash) {
				t.Fatal("hash in account response")
			}
			repo := repositories.NewCredentialRepository(db)
			before, err := repo.Passwords(account.ID, time.Now())
			if err != nil || len(before) != 1 || before[0].SecretHash != tc.hash {
				t.Fatalf("import did not preserve the hash: %v", err)
			}
			// Every protocol gateway signs in through VerifyBasic; IMAP does
			// not maintain another password store.
			cred, mb, err := repo.VerifyBasic(account.Address, tc.password, time.Now(), nil)
			if err != nil || mb == nil || cred == nil {
				t.Fatalf("protocol sign-in: %v", err)
			}
			if cred.ID != before[0].ID || !strings.HasPrefix(cred.SecretHash, "{BLF-CRYPT}") || auth.CheckPassword(tc.password, cred.SecretHash) != nil {
				t.Fatal("password proof did not preserve the credential and upgrade its hash")
			}
			var stored models.Credential
			if err := db.First(&stored, cred.ID).Error; err != nil || stored.SecretHash != cred.SecretHash {
				t.Fatalf("upgraded hash not persisted: %v", err)
			}
			if _, _, err := repo.VerifyBasic(account.Address, "wrong-password", time.Now(), nil); err == nil {
				t.Fatal("wrong password signed in")
			}
		})
	}
}
