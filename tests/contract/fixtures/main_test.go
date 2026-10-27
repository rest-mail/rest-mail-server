package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/tests/contract/fixtureplan"
	"gorm.io/gorm"
)

func fixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := os.Getenv("DB_NAME")
	if !strings.HasPrefix(name, "restmail_contract_fixture_") {
		t.Skip("set DB_NAME to an isolated restmail_contract_fixture_* database")
	}
	port, _ := strconv.Atoi(os.Getenv("DB_PORT"))
	db, err := rmdb.Connect(&config.Config{DBHost: os.Getenv("DB_HOST"), DBPort: port,
		DBName: name, DBUser: os.Getenv("DB_USER"), DBPass: os.Getenv("DB_PASS")})
	if err != nil {
		t.Fatal("connect to isolated fixture database:", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&models.Domain{}, &models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func retry(t *testing.T, db *gorm.DB, id uint) int {
	t.Helper()
	router := chi.NewRouter()
	handlers.NewQueueHandler(db).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, handlers.QueueRouteChecks{
		At: func(string) endpoint.Middleware {
			return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
		},
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/admin/queue/%d/retry", id), nil))
	return response.Code
}

func TestRetryFixtureWaitsForTheActualRetry(t *testing.T) {
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	// Reproduce the old contract failure: the sent message has already completed.
	live := models.OutboundQueue{Sender: "admin@restmail.test", Recipient: "someone@imap.restmail.test",
		Domain: "imap.restmail.test", Status: "delivered"}
	if err := db.Create(&live).Error; err != nil {
		t.Fatal(err)
	}
	if status := retry(t, db, live.ID); status != http.StatusBadRequest {
		t.Fatalf("retry of the already delivered example: %d, want 400", status)
	}
	id, err := prepareRetryFixture(db, "restmail.test")
	if err != nil {
		t.Fatal(err)
	}
	var due int64
	claimable := func() int64 {
		if err := db.Model(&models.OutboundQueue{}).Where("id = ? AND status IN ? AND next_attempt <= ? AND expires_at > ?",
			id, []string{"pending", "deferred"}, time.Now(), time.Now()).Count(&due).Error; err != nil {
			t.Fatal(err)
		}
		return due
	}
	if claimable() != 0 {
		t.Fatal("background worker can take the fixture before the retry example")
	}
	if status := retry(t, db, id); status != http.StatusOK {
		t.Fatalf("retry of the held fixture: %d, want 200", status)
	}
	if claimable() != 1 {
		t.Fatal("the actual retry did not make the fixture due")
	}
	var preserved models.OutboundQueue
	if err := db.First(&preserved, live.ID).Error; err != nil || preserved.Status != "delivered" {
		t.Fatal("fixture setup changed the existing sent message")
	}
}

func TestRetryFixtureIsFreshOnEveryRun(t *testing.T) {
	plan, err := fixtureplan.Default()
	if err != nil {
		t.Fatal(err)
	}
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	first, err := prepareRetryFixture(db, "restmail.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.OutboundQueue{}).Where("id = ?", first).Update("status", "delivered").Error; err != nil {
		t.Fatal(err)
	}
	second, err := prepareRetryFixture(db, "restmail.test")
	if err != nil || first == second {
		t.Fatal("a repeat reused its consumed fixture:", err)
	}
	var newest models.OutboundQueue
	if err := db.Where("recipient = ?", plan.Queue["retry"].Recipient).Order("created_at DESC").First(&newest).Error; err != nil || newest.ID != second {
		t.Fatal("listQueue does not reach the new held entry:", err)
	}
}

func TestRetryFixtureRequiresTheDisposableDomain(t *testing.T) {
	if _, err := prepareRetryFixture(nil, "production.example"); err == nil {
		t.Fatal("fixture accepted a different domain")
	}
	db := fixtureDB(t)
	if _, err := prepareRetryFixture(db, "restmail.test"); err == nil {
		t.Fatal("fixture accepted a database without restmail.test")
	}
	var rows int64
	if err := db.Model(&models.OutboundQueue{}).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatal("refused setup changed the queue:", err)
	}
}

func TestLinkFixtureRequiresTheDisposableDomain(t *testing.T) {
	if _, err := prepareLinkFixture(nil, "production.example"); err == nil {
		t.Fatal("fixture accepted a different domain")
	}
	db := fixtureDB(t)
	if _, err := prepareLinkFixture(db, "restmail.test"); err == nil {
		t.Fatal("fixture accepted a database without restmail.test")
	}
}

func TestAPIKeyFixtureRequiresTheDisposableDomain(t *testing.T) {
	if _, err := prepareAPIKeyFixture(nil, "production.example"); err == nil {
		t.Fatal("API key fixture accepted a different domain")
	}
}

func TestLinkFixtureSupportsUnmodifiedGeneratedProof(t *testing.T) {
	db := fixtureDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Domain{Name: "restmail.test"}).Error; err != nil {
		t.Fatal(err)
	}
	// The preceding createAccount probes can leave their generated account
	// with another password. Prepare its link proof without replacing its id.
	hosted := models.Domain{Name: "example.com"}
	if err := db.Create(&hosted).Error; err != nil {
		t.Fatal(err)
	}
	created := models.Mailbox{DomainID: hosted.ID, LocalPart: "someone", Address: "someone@example.com", Active: true}
	if err := db.Create(&created).Error; err != nil {
		t.Fatal(err)
	}
	first, err := prepareLinkFixture(db, "restmail.test")
	if err != nil {
		t.Fatal(err)
	}
	if first != created.ID {
		t.Fatal("link fixture replaced the preceding createAccount probe")
	}
	second, err := prepareLinkFixture(db, "restmail.test")
	if err != nil || first != second {
		t.Fatal("fixture was not idempotent:", first, second, err)
	}
	var account models.Mailbox
	if err := db.Preload("Domain").First(&account, first).Error; err != nil {
		t.Fatal(err)
	}
	if account.Address != "someone@example.com" || !account.Active || account.PasswordChangeRequired || account.Domain.Name != "example.com" {
		t.Fatalf("generated proof's account = %+v", account)
	}
	creds := repositories.NewCredentialRepository(db)
	credential, err := creds.MatchPassword(first, "x", time.Now())
	if err != nil || credential == nil {
		t.Fatal("generated password did not match:", err)
	}
	credential, err = creds.MatchPassword(first, "wrong-password", time.Now())
	if err != nil || credential != nil {
		t.Fatal("wrong password matched:", err)
	}
	passwords, err := creds.Passwords(first, time.Now())
	if err != nil || len(passwords) != 1 {
		t.Fatal("repeat setup changed credentials:", passwords, err)
	}
}

func TestDomainTenantFixtureRequiresTheDisposableDomain(t *testing.T) {
	if _, err := prepareDomainTenants(nil, "production.example"); err == nil {
		t.Fatal("domain tenant fixture accepted a different domain")
	}
}

func TestDomainTenantsAdministerOnlyTheirOwnDomain(t *testing.T) {
	db := fixtureDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}, &models.QuotaUsage{}, &models.Role{}, &models.Grant{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Domain{Name: "restmail.test", ServerType: "restmail", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Role{Name: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	first, err := prepareDomainTenants(db, "restmail.test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareDomainTenants(db, "restmail.test")
	if err != nil || first["a"] != second["a"] || first["b"] != second["b"] {
		t.Fatal("fixture was not idempotent:", first, second, err)
	}
	if first["a"].DomainID == first["b"].DomainID {
		t.Fatal("the tenants share a domain")
	}
	for tenant, want := range map[string]string{"a": "restmail.test", "b": "contract-other.test"} {
		var account models.Mailbox
		if err := db.Where("address = ?", first[tenant].Address).First(&account).Error; err != nil {
			t.Fatal(err)
		}
		var scopes []string
		if err := db.Model(&models.Grant{}).Where("holder_id = ?", account.ID).Pluck("scope", &scopes).Error; err != nil {
			t.Fatal(err)
		}
		if len(scopes) != 1 || scopes[0] != "domain:"+want {
			t.Fatalf("tenant %s holds %v, want the admin role on %s only", tenant, scopes, want)
		}
		credential, err := repositories.NewCredentialRepository(db).MatchPassword(account.ID, domainTenantPassword, time.Now())
		if err != nil || credential == nil {
			t.Fatalf("tenant %s cannot sign in with the fixture password: %v", tenant, err)
		}
	}
}
