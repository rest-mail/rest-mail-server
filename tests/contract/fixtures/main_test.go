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
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/tests/contract/fixtureplan"
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
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
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

func TestDeliveryRecipientRequiresTheDisposableDomain(t *testing.T) {
	if err := prepareDeliveryRecipient(nil, "production.example"); err == nil {
		t.Fatal("delivery recipient accepted a different domain")
	}
}

// The fuzz pin names this id before the run, so it has to be the mailbox's. A
// second run finds what the first made, and no sequence moves for later ids.
func TestDeliveryRecipientIsTheMailboxThePinNames(t *testing.T) {
	db := fixtureDB(t)
	plan, err := fixtureplan.Default()
	if err != nil {
		t.Fatal(err)
	}
	sequences := func() (values [2]int64) {
		for i, name := range []string{"domains_id_seq", "mailboxes_id_seq"} {
			if err := db.Raw("SELECT nextval('" + name + "')").Scan(&values[i]).Error; err != nil {
				t.Fatal(err)
			}
		}
		return values
	}
	before := sequences()
	for range 2 {
		if err := prepareDeliveryRecipient(db, "restmail.test"); err != nil {
			t.Fatal(err)
		}
	}
	if after := sequences(); after != [2]int64{before[0] + 1, before[1] + 1} {
		t.Fatalf("the recipient moved a sequence: %v, then %v", before, after)
	}
	var account models.Mailbox
	if err := db.First(&account, plan.DeliveryRecipient.ID).Error; err != nil {
		t.Fatal(err)
	}
	if account.Address != plan.DeliveryRecipient.Address || !account.Active {
		t.Fatalf("recipient = %+v", account)
	}
	if err := repositories.RequireMailboxReceive(db, &account); err != nil {
		t.Fatal("the recipient cannot receive:", err)
	}
	var policies []models.Pipeline
	if err := db.Where("domain_id = ?", account.DomainID).Find(&policies).Error; err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].Direction != "inbound" || !policies[0].Active || string(policies[0].Filters) != "[]" {
		t.Fatalf("the recipient's domain stores %+v", policies)
	}
	var elsewhere int64
	if err := db.Model(&models.Pipeline{}).Where("domain_id <> ?", account.DomainID).Count(&elsewhere).Error; err != nil {
		t.Fatal(err)
	}
	if elsewhere != 0 {
		t.Fatalf("%d pipelines were stored on other domains", elsewhere)
	}
}

func TestAPIKeyFixtureRequiresTheDisposableDomain(t *testing.T) {
	if _, err := prepareAPIKeyFixture(nil, "production.example"); err == nil {
		t.Fatal("API key fixture accepted a different domain")
	}
}

// createDomain's probes remove the generated domain, and the fixture hosts it again for
// what follows them, which needs it live.
func TestLinkFixtureHostsAnAbsentDomainLive(t *testing.T) {
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLinkFixture(db, "restmail.test"); err != nil {
		t.Fatal(err)
	}
	var hosted models.Domain
	if err := db.Where("name = ?", "example.com").First(&hosted).Error; err != nil {
		t.Fatal(err)
	}
	if !hosted.Active {
		t.Fatal("the generated domain was hosted but not live")
	}
}

func TestLinkFixtureSupportsUnmodifiedGeneratedProof(t *testing.T) {
	db := fixtureDB(t)
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
	if err := db.Create(&models.Domain{Name: "restmail.test", ServerType: "restmail", Active: true}).Error; err != nil {
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
	if first["a"].MailboxID == 0 || first["a"].MailboxID == first["b"].MailboxID {
		t.Fatal("the tenants do not each name a mailbox of their own:", first)
	}
	for tenant, want := range map[string]string{"a": "restmail.test", "b": "contract-other.test"} {
		var account models.Mailbox
		if err := db.Where("address = ?", first[tenant].Address).First(&account).Error; err != nil {
			t.Fatal(err)
		}
		var held []models.Grant
		if err := db.Preload("Role").Where("holder_kind = ? AND holder_id = ?", models.GrantHolderAccount, account.ID).Find(&held).Error; err != nil {
			t.Fatal(err)
		}
		wantScopes := map[string]string{"admin": "domain:" + want, models.RoleReceiver: "mailbox:" + account.Address}
		if len(held) != len(wantScopes) {
			t.Fatalf("tenant %s holds %v, want only %v", tenant, held, wantScopes)
		}
		for _, grant := range held {
			if scope, ok := wantScopes[grant.Role.Name]; !ok || grant.Scope != scope || grant.Mode != models.GrantActive {
				t.Fatalf("tenant %s holds %+v, want only active grants %v", tenant, grant, wantScopes)
			}
		}
		credential, err := repositories.NewCredentialRepository(db).MatchPassword(account.ID, domainTenantPassword, time.Now())
		if err != nil || credential == nil {
			t.Fatalf("tenant %s cannot sign in with the fixture password: %v", tenant, err)
		}
	}
}

// The bounce example takes an entry of its own, held like the retry entry, so
// the retry example still finds its entry deferred.
func TestQueueFixturesHoldABounceEntryApartFromTheRetryEntry(t *testing.T) {
	plan, err := fixtureplan.Default()
	if err != nil {
		t.Fatal(err)
	}
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := prepareQueueFixtures(db, "restmail.test"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bounce", "retry"} {
		var held models.OutboundQueue
		if err := db.Where("recipient = ? AND status = ? AND next_attempt > ?", plan.Queue[name].Recipient, "deferred", time.Now()).
			First(&held).Error; err != nil {
			t.Fatalf("no held %s entry: %v", name, err)
		}
	}
}

func TestProbeAdminHoldsWhatTheRequestsCarry(t *testing.T) {
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test", ServerType: "traditional", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	// Twice: the E2E suite and the contract run both prepare it.
	for range 2 {
		if err := prepareMFAAdmin(db, "restmail.test"); err != nil {
			t.Fatal(err)
		}
	}
	var account models.Mailbox
	if err := db.Where("address = ?", probeAdminAddress).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	creds := repositories.NewCredentialRepository(db)
	for password, held := range map[string]bool{"x": true, "current-password": true, "not-the-password": false, "": false} {
		match, err := creds.MatchPassword(account.ID, password, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if (match != nil) != held {
			t.Errorf("password %q: match %v, want held %v", password, match != nil, held)
		}
	}
	stored, err := creds.Passwords(account.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(probeAdminPasswords) {
		t.Fatalf("%d passwords stored after preparing twice, want %d", len(stored), len(probeAdminPasswords))
	}
	var two models.Mailbox
	if err := db.Where("address = ?", mfaAdminAddress).First(&two).Error; err != nil {
		t.Fatal(err)
	}
	if match, err := creds.MatchPassword(two.ID, mfaAdminPassword, time.Now()); err != nil || match == nil {
		t.Fatalf("the two-factor administrator's password: %v, %v", match, err)
	}
	if match, err := creds.MatchPassword(two.ID, "x", time.Now()); err != nil || match != nil {
		t.Fatalf("the two-factor administrator must not hold the probe password: %v, %v", match, err)
	}
}

func TestMFAAdminFixtureRequiresTheDisposableDomain(t *testing.T) {
	if err := prepareMFAAdmin(nil, "production.example"); err == nil {
		t.Fatal("MFA administrator fixture accepted a different domain")
	}
}
