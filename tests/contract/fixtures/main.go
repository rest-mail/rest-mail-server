// This command prepares state only for the disposable restmail.test contract topology.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/tests/contract/fixtureplan"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func main() {
	domain := flag.String("test-domain", "", "confirm the disposable restmail.test topology")
	retryOnly := flag.Bool("retry-only", false, "replenish only the retry example's queue entry, leaving every other fixture as it is")
	linkOnly := flag.Bool("link-account-only", false, "prepare the generated account-link proof after account-creation probes")
	apiKeys := flag.Bool("api-keys", false, "prepare a fresh account for real-MFA API key checks")
	domainTenants := flag.Bool("domain-tenants", false, "prepare the domain administrators of the ownership phase")
	mfaAdmin := flag.Bool("mfa-admin", false, "prepare the administrator the contract hooks enroll in real MFA")
	deliveryRecipient := flag.Bool("delivery-recipient", false, "prepare the mailbox deliverMessage's fuzz probes are pinned to")
	heldMailOnly := flag.Bool("held-mail-only", false, "replenish only the disposable held-mail targets, without touching the queue")
	queueEntryOnly := flag.Bool("queue-entry-only", false, "create one disposable deferred queue entry without changing other fixtures")
	reportPhase := flag.String("report-targets", "", "top up the held mail one report-only phase consumes (authorization or ownership); no gate calls this")
	flag.Parse()
	modes := 0
	for _, on := range []bool{*linkOnly, *apiKeys, *domainTenants, *mfaAdmin, *deliveryRecipient, *heldMailOnly, *queueEntryOnly, *reportPhase != ""} {
		if on {
			modes++
		}
	}
	if *retryOnly {
		modes++
	}
	if *domain != "restmail.test" || flag.NArg() != 0 || modes > 1 {
		fmt.Fprintln(os.Stderr, "contract fixtures require --test-domain=restmail.test")
		os.Exit(1)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot load contract fixture database configuration")
		os.Exit(1)
	}
	database, err := db.WaitForDB(cfg, 10*time.Second)
	if err == nil {
		if *apiKeys {
			var account *models.Mailbox
			account, err = prepareAPIKeyFixture(database, *domain)
			if err == nil {
				body, _ := json.Marshal(map[string]interface{}{"email": account.Address, "domain_id": account.DomainID})
				fmt.Printf("API_KEY_FIXTURE=%s\n", body)
			}
		} else if *domainTenants {
			var tenants map[string]domainTenant
			tenants, err = prepareDomainTenants(database, *domain)
			if err == nil {
				body, _ := json.Marshal(tenants)
				fmt.Printf("DOMAIN_TENANTS=%s\n", body)
			}
		} else if *mfaAdmin {
			err = prepareMFAAdmin(database, *domain)
		} else if *deliveryRecipient {
			err = prepareDeliveryRecipient(database, *domain)
		} else if *queueEntryOnly {
			var id uint
			id, err = prepareRetryFixture(database, *domain)
			if err == nil {
				body, _ := json.Marshal(map[string]uint{"id": id})
				fmt.Printf("QUEUE_ENTRY_FIXTURE=%s\n", body)
			}
		} else if *heldMailOnly {
			var targets heldMailTargets
			targets, err = prepareHeldMailTargets(database, *domain)
			if err == nil {
				body, _ := json.Marshal(targets)
				fmt.Printf("HELD_MAIL_FIXTURE=%s\n", body)
			}
		} else if *linkOnly {
			_, err = prepareLinkFixture(database, *domain)
		} else if *retryOnly {
			_, err = prepareRetryFixture(database, *domain)
		} else if *reportPhase != "" {
			err = prepareReportTargets(database, *domain, *reportPhase)
		} else {
			err = prepareQueueFixtures(database, *domain)
			if err == nil {
				err = prepareHeldMail(database, *domain)
			}
			if err == nil {
				err = prepareDMARCReport(database, *domain)
			}
			if err == nil {
				err = prepareCertificateProbeDomain(database, *domain)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot prepare the contract fixtures")
		os.Exit(1)
	}
	fmt.Println("contract fixtures prepared")
}

// A fresh account lets each CI run enroll real MFA without resetting either
// the primary contract admin or an enrollment a preceding run used.
func prepareAPIKeyFixture(database *gorm.DB, domain string) (*models.Mailbox, error) {
	if domain != "restmail.test" {
		return nil, errors.New("contract fixtures require restmail.test")
	}
	var hosted models.Domain
	if err := database.Where("name = ?", domain).First(&hosted).Error; err != nil {
		return nil, err
	}
	var role models.Role
	if err := database.Where("name = ?", "admin").First(&role).Error; err != nil {
		return nil, err
	}
	local := fmt.Sprintf("contract-api-keys-%d", time.Now().UnixNano())
	var resetInitialized bool
	if err := database.Raw("SELECT to_regclass(current_schema() || '.test_reset_baseline') IS NOT NULL").Scan(&resetInitialized).Error; err != nil {
		return nil, err
	}
	if resetInitialized {
		local = "contract-api-keys-reset"
	}
	account := models.Mailbox{DomainID: hosted.ID, LocalPart: local, Address: local + "@" + domain,
		DisplayName: "Contract API keys", Active: true, QuotaBytes: hosted.DefaultQuotaBytes}
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		if err := grants.EnsureDefaults(tx, &account); err != nil {
			return err
		}
		hash, err := auth.HashPassword("contract-api-key123!@")
		if err != nil {
			return err
		}
		if _, err := repositories.NewCredentialRepository(tx).SetPassword(account.ID, hash, nil, time.Now()); err != nil {
			return err
		}
		if err := tx.Create(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
			return err
		}
		_, err = grants.EnsureSystem(tx, account.ID, role.ID, "domain:"+domain, models.GrantActive)
		return err
	})
	return &account, err
}

// The administrator the contract hooks use for setup that takes a two-factor
// session, such as deleting the accounts probes made (deleting an account
// always takes 2FA). It is a superadmin: probes make accounts on more than one
// domain, and deleting one removes its grants, which only someone holding all
// of them may do (the primary admin holds them as each probe's owner). The hooks enroll it in TOTP through the
// public API and sign in with a real code, so no session is forged. Its
// grant is made here because granting through the API takes 2FA itself. The
// password is disposable test state, known to the hooks; it is not printed.
const mfaAdminAddress, mfaAdminPassword = "contract-mfa-admin@restmail.test", "contract-mfa-admin123!@"

// prepareDeliveryRecipient makes the mailbox a generated delivery reaches. A
// generated body has no Date or Message-ID, so the default inbound pipeline rightly
// refuses it with a 550 before delivery is reached. Its domain therefore stores an
// inbound pipeline with no filters, and the rest of delivery runs: the recipient's
// receive grant, its quota, storing the message and the answer. Only this domain
// has that policy. Every row names its id, so no sequence moves and no literal id
// in an example comes to name something else. Running it again changes nothing.
func prepareDeliveryRecipient(database *gorm.DB, domain string) error {
	if domain != "restmail.test" {
		return errors.New("contract fixtures require restmail.test")
	}
	plan, err := fixtureplan.Default()
	if err != nil {
		return err
	}
	recipient := plan.DeliveryRecipient
	local, name, _ := strings.Cut(recipient.Address, "@")
	return database.Transaction(func(tx *gorm.DB) error {
		account := models.Mailbox{ID: recipient.ID, DomainID: recipient.ID, LocalPart: local, Address: recipient.Address,
			DisplayName: "Contract delivery recipient", Active: true}
		for _, row := range []any{
			&models.Domain{ID: recipient.ID, Name: name, ServerType: "restmail", Active: true},
			&models.Pipeline{ID: recipient.ID, DomainID: recipient.ID, Direction: "inbound", Filters: json.RawMessage("[]"), Active: true},
			&account,
			&models.QuotaUsage{MailboxID: recipient.ID},
		} {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
				return err
			}
		}
		return grants.EnsureDefaults(tx, &account)
	})
}

func prepareCertificateProbeDomain(database *gorm.DB, domain string) error {
	if domain != "restmail.test" {
		return errors.New("contract fixtures require restmail.test")
	}
	plan, err := fixtureplan.Default()
	if err != nil {
		return err
	}
	fixture := plan.CertificateDomain
	row := models.Domain{ID: fixture.ID, Name: fixture.Name, ServerType: "traditional"}
	if err := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := database.First(&row, fixture.ID).Error; err != nil {
		return err
	}
	if row.Name != fixture.Name {
		return errors.New("certificate probe domain id belongs to another domain")
	}
	return nil
}

// The administrator a coverage probe of the API key operations is sent as. Those
// operations check the caller's current password against the password in the
// body, and a hook cannot supply it: Vertrag ends a generated probe whose body a
// hook changed. So the account's passwords are the ones the requests already
// carry: "x" is what the generator draws for a required string, and
// current-password is what the documented examples say. Both are plainly not
// secrets, and the handler's comparison with them is the real one. They are two
// password credentials, which the password check takes any of.
const probeAdminAddress = "contract-probe-admin@restmail.test"

var probeAdminPasswords = []string{"x", "current-password"}

func prepareMFAAdmin(database *gorm.DB, domain string) error {
	if domain != "restmail.test" {
		return errors.New("contract fixtures require restmail.test")
	}
	var role models.Role
	if err := database.Where("name = ?", "superadmin").First(&role).Error; err != nil {
		return err
	}
	return database.Transaction(func(tx *gorm.DB) error {
		var hosted models.Domain
		if err := tx.Where("name = ?", domain).First(&hosted).Error; err != nil {
			return err
		}
		if err := prepareSuperadmin(tx, hosted, role, mfaAdminAddress, "Contract MFA admin", []string{mfaAdminPassword}); err != nil {
			return err
		}
		return prepareSuperadmin(tx, hosted, role, probeAdminAddress, "Contract probe admin", probeAdminPasswords)
	})
}

// prepareSuperadmin makes the account if it is not there and gives it each
// password it does not already hold.
func prepareSuperadmin(tx *gorm.DB, hosted models.Domain, role models.Role, address, displayName string, passwords []string) error {
	localPart, _, _ := strings.Cut(address, "@")
	account := models.Mailbox{DomainID: hosted.ID, LocalPart: localPart, Address: address,
		DisplayName: displayName, Active: true, QuotaBytes: hosted.DefaultQuotaBytes}
	result := tx.Where("address = ?", account.Address).FirstOrCreate(&account)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		if err := grants.EnsureDefaults(tx, &account); err != nil {
			return err
		}
	}
	creds := repositories.NewCredentialRepository(tx)
	for index, password := range passwords {
		current, err := creds.MatchPassword(account.ID, password, time.Now())
		if err != nil {
			return err
		}
		if current != nil {
			continue
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		// SetPassword replaces the account's passwords, so only the first goes
		// through it and the others are stored beside it.
		if index == 0 {
			if _, err := creds.SetPassword(account.ID, hash, nil, time.Now()); err != nil {
				return err
			}
			continue
		}
		first, err := creds.Passwords(account.ID, time.Now())
		if err != nil {
			return err
		}
		if len(first) == 0 {
			return errors.New("the account has no first password")
		}
		publicID, err := auth.NewPublicID()
		if err != nil {
			return err
		}
		another := models.Credential{AccountID: account.ID, Kind: models.CredentialKindPassword, PublicID: publicID,
			Name: "Password", SecretHash: hash, Scope: first[0].Scope}
		if err := tx.Create(&another).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("mailbox_id = ?", account.ID).FirstOrCreate(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
		return err
	}
	_, err := grants.EnsureSystem(tx, account.ID, role.ID, grants.All, models.GrantActive)
	return err
}

// The domain tenants of the ownership phase (#657): an administrator of
// restmail.test and one of a second disposable domain, each holding the admin
// role on its own domain only. Changing grants through the API takes a
// two-factor session, which no contract principal has, so they are made here.
// The password is disposable test state; it is not printed.
const domainTenantPassword = "contract-domain-admin123!@"

// domainTenantDomains is each tenant's domain.
var domainTenantDomains = []struct{ tenant, name string }{{"a", "restmail.test"}, {"b", "contract-other.test"}}

type domainTenant struct {
	Address  string `json:"address"`
	DomainID uint   `json:"domain_id"`
	// MailboxID is the administrator's own mailbox, a mailbox of its domain
	// the other tenant's administrator must not reach.
	MailboxID uint `json:"mailbox_id"`
}

func prepareDomainTenants(database *gorm.DB, domain string) (map[string]domainTenant, error) {
	if domain != "restmail.test" {
		return nil, errors.New("contract fixtures require restmail.test")
	}
	var role models.Role
	if err := database.Where("name = ?", "admin").First(&role).Error; err != nil {
		return nil, err
	}
	tenants := map[string]domainTenant{}
	err := database.Transaction(func(tx *gorm.DB) error {
		for _, d := range domainTenantDomains {
			hosted := models.Domain{Name: d.name, ServerType: "restmail", Active: true}
			if err := tx.Where("name = ?", d.name).FirstOrCreate(&hosted).Error; err != nil {
				return err
			}
			account := models.Mailbox{DomainID: hosted.ID, LocalPart: "contract-domain-admin",
				Address: "contract-domain-admin@" + d.name, DisplayName: "Contract domain admin", Active: true,
				QuotaBytes: hosted.DefaultQuotaBytes}
			result := tx.Where("address = ?", account.Address).FirstOrCreate(&account)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				if err := grants.EnsureDefaults(tx, &account); err != nil {
					return err
				}
			}
			creds := repositories.NewCredentialRepository(tx)
			current, err := creds.MatchPassword(account.ID, domainTenantPassword, time.Now())
			if err != nil {
				return err
			}
			if current == nil {
				hash, err := auth.HashPassword(domainTenantPassword)
				if err != nil {
					return err
				}
				if _, err := creds.SetPassword(account.ID, hash, nil, time.Now()); err != nil {
					return err
				}
			}
			if err := tx.Where("mailbox_id = ?", account.ID).FirstOrCreate(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
				return err
			}
			if _, err := grants.EnsureSystem(tx, account.ID, role.ID, "domain:"+d.name, models.GrantActive); err != nil {
				return err
			}
			tenants[d.tenant] = domainTenant{Address: account.Address, DomainID: hosted.ID, MailboxID: account.ID}
		}
		return nil
	})
	return tenants, err
}

// Vertrag v0.29 generates this email and password for LinkAccountRequest's
// valid boundaries. Seed the proof, rather than rewriting generated requests
// or weakening the handler's credential checks. This is disposable test state.
func prepareLinkFixture(database *gorm.DB, domain string) (uint, error) {
	if domain != "restmail.test" {
		return 0, errors.New("contract fixtures require restmail.test")
	}
	var count int64
	if err := database.Model(&models.Domain{}).Where("name = ?", domain).Count(&count).Error; err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, errors.New("contract fixture domain is missing or ambiguous")
	}
	var account models.Mailbox
	err := database.Transaction(func(tx *gorm.DB) error {
		// Live, as the probes that made it left it: createDomain's probes remove it, and
		// what follows them, such as autodiscover, needs to find it (hooks.py).
		hosted := models.Domain{Name: "example.com", ServerType: "traditional", Active: true}
		if err := tx.Where("name = ?", hosted.Name).FirstOrCreate(&hosted).Error; err != nil {
			return err
		}
		account = models.Mailbox{DomainID: hosted.ID, LocalPart: "someone", Address: "someone@example.com", Active: true}
		result := tx.Where("address = ?", account.Address).FirstOrCreate(&account)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if err := grants.EnsureDefaults(tx, &account); err != nil {
				return err
			}
		}
		creds := repositories.NewCredentialRepository(tx)
		if result.RowsAffected == 0 {
			credential, err := creds.MatchPassword(account.ID, "x", time.Now())
			if err != nil {
				return err
			}
			if credential != nil {
				return nil
			}
		}
		hash, err := auth.HashPassword("x")
		if err != nil {
			return err
		}
		if _, err := creds.SetPassword(account.ID, hash, nil, time.Now()); err != nil {
			return err
		}
		return tx.Where("mailbox_id = ?", account.ID).FirstOrCreate(&models.QuotaUsage{MailboxID: account.ID}).Error
	})
	return account.ID, err
}

// prepareQueueFixtures holds one entry for the retry example and one for the
// bounce example: a bounced entry cannot be retried, so they cannot share one.
func prepareQueueFixtures(database *gorm.DB, domain string) error {
	for _, name := range []string{"retry", "bounce"} {
		if _, err := prepareQueueFixture(database, domain, name); err != nil {
			return err
		}
	}
	return nil
}

func prepareRetryFixture(database *gorm.DB, domain string) (uint, error) {
	return prepareQueueFixture(database, domain, "retry")
}

func prepareQueueFixture(database *gorm.DB, domain, name string) (uint, error) {
	if domain != "restmail.test" {
		return 0, errors.New("contract fixtures require restmail.test")
	}
	plan, err := fixtureplan.Default()
	if err != nil {
		return 0, err
	}
	entry, ok := plan.Queue[name]
	if !ok {
		return 0, fmt.Errorf("contract fixture plan has no %s queue entry", name)
	}
	var count int64
	if err := database.Model(&models.Domain{}).Where("name = ?", domain).Count(&count).Error; err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, errors.New("contract fixture domain is missing or ambiguous")
	}
	// The sent-message example can already be delivered when its retry runs.
	// A separate deferred entry cannot be claimed until the retry example itself
	// makes it due. Create a fresh entry on every run; listQueue orders newest first.
	now := time.Now()
	item := models.OutboundQueue{
		Sender: entry.Sender, Recipient: entry.Recipient, Domain: entry.Domain,
		RawMessage: "From: " + entry.Sender + "\r\nTo: " + entry.Recipient + "\r\nSubject: " + entry.Subject + "\r\n\r\n" + entry.Body,
		Status:     entry.Status, NextAttempt: now.Add(time.Duration(entry.NextAttemptHours) * time.Hour), ExpiresAt: now.Add(time.Duration(entry.ExpiresHours) * time.Hour),
	}
	if err := database.Create(&item).Error; err != nil {
		return 0, err
	}
	return item.ID, nil
}

// The quarantined message deleteQuarantine's found example deletes, held for
// the fixture admin, and the suppressed recipient clearSuppression's found
// example clears, on the fixture domain (#1334). Mail reaches neither state in
// a healthy run on its own. Fresh rows on every run; the hooks take the newest.
func prepareHeldMail(database *gorm.DB, domain string) error {
	_, err := prepareHeldMailTargets(database, domain)
	return err
}

type heldMailTargets struct {
	AccountID     uint `json:"account_id"`
	MessageID     uint `json:"message_id"`
	DomainID      uint `json:"domain_id"`
	SuppressionID uint `json:"suppression_id"`
}

func prepareHeldMailTargets(database *gorm.DB, domain string) (heldMailTargets, error) {
	var targets heldMailTargets
	if domain != "restmail.test" {
		return targets, errors.New("contract fixtures require restmail.test")
	}
	plan, err := fixtureplan.Default()
	if err != nil {
		return targets, err
	}
	var admin models.Mailbox
	if err := database.Where("address = ?", plan.Principals["admin"].Address).First(&admin).Error; err != nil {
		return targets, err
	}
	now := time.Now()
	err = database.Transaction(func(tx *gorm.DB) error {
		held := heldMessage(admin, "Held for review", now)
		if err := tx.Create(held).Error; err != nil {
			return err
		}
		address := fmt.Sprintf("contract-suppressed-%d@nowhere.invalid", now.UnixNano())
		suppressed := suppressedRecipient(admin, address, now)
		if err := tx.Create(suppressed).Error; err != nil {
			return err
		}
		targets = heldMailTargets{AccountID: admin.ID, MessageID: held.ID, DomainID: admin.DomainID, SuppressionID: suppressed.ID}
		return nil
	})
	if err != nil {
		return heldMailTargets{}, err
	}
	return targets, nil
}

func heldMessage(admin models.Mailbox, subject string, now time.Time) *models.Quarantine {
	return &models.Quarantine{
		MailboxID: admin.ID, Sender: "sender@example.net", Subject: subject,
		BodyPreview: "Preview only", QuarantineReason: "spam",
		RawMessage: "From: sender@example.net\r\nTo: " + admin.Address + "\r\nSubject: " + subject + "\r\n\r\nPreview only\r\n",
		ReceivedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
}

func suppressedRecipient(admin models.Mailbox, address string, now time.Time) *models.Suppression {
	return &models.Suppression{
		DomainID: admin.DomainID, Address: address,
		Reason: models.SuppressionHardBounce, FailureCategory: models.FailureBadMailbox,
		LastError: "550 5.1.1 no such mailbox", ExpiresAt: now.Add(30 * 24 * time.Hour),
	}
}

// reportPhaseTargets is how many quarantined messages and how many suppressed
// recipients each report-only phase consumes. deleteQuarantine and
// clearSuppression's found examples take one each time they are sent as the
// fixture admin. Authorization sends them twice: its examples first, then the
// matrix's admin cell. Ownership sends them once, in the examples of its account
// run and of its domain run; its cells name no such row.
var reportPhaseTargets = map[string]int{"authorization": 2, "ownership": 1}

// prepareReportTargets tops up the held mail a report-only phase consumes, and
// no more, so nothing outlives the phase it was made for. Only those phases call
// it, after the gate. The gate must keep seeing what it sees today: coverage's
// skipped-probe ratchet depends on prepareHeldMail's single rows being spent.
func prepareReportTargets(database *gorm.DB, domain, phase string) error {
	if domain != "restmail.test" {
		return errors.New("contract fixtures require restmail.test")
	}
	want, ok := reportPhaseTargets[phase]
	if !ok {
		return fmt.Errorf("no report-only phase is called %q", phase)
	}
	plan, err := fixtureplan.Default()
	if err != nil {
		return err
	}
	var admin models.Mailbox
	if err := database.Where("address = ?", plan.Principals["admin"].Address).First(&admin).Error; err != nil {
		return err
	}
	now := time.Now()
	return database.Transaction(func(tx *gorm.DB) error {
		// The rows the hooks would take: unreleased, and unexpired because the
		// suppression list leaves out expired ones.
		var held, suppressed int64
		if err := tx.Model(&models.Quarantine{}).Where("mailbox_id = ? AND released = ?", admin.ID, false).Count(&held).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Suppression{}).
			Where("domain_id = ? AND address LIKE ? AND expires_at > ?", admin.DomainID, "contract-suppressed-%", now).
			Count(&suppressed).Error; err != nil {
			return err
		}
		for i := int(held); i < want; i++ {
			if err := tx.Create(heldMessage(admin, "Held for the "+phase+" report", now)).Error; err != nil {
				return err
			}
		}
		for i := int(suppressed); i < want; i++ {
			address := fmt.Sprintf("contract-suppressed-%s-%d-%d@nowhere.invalid", phase, now.UnixNano(), i)
			if err := tx.Create(suppressedRecipient(admin, address, now)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
