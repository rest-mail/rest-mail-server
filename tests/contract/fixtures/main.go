// This command prepares state only for the disposable restmail.test contract topology.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/tests/contract/fixtureplan"
	"gorm.io/gorm"
)

func main() {
	domain := flag.String("test-domain", "", "confirm the disposable restmail.test topology")
	linkOnly := flag.Bool("link-account-only", false, "prepare the generated account-link proof after account-creation probes")
	apiKeys := flag.Bool("api-keys", false, "prepare a fresh account for real-MFA API key checks")
	domainTenants := flag.Bool("domain-tenants", false, "prepare the domain administrators of the ownership phase")
	mfaAdmin := flag.Bool("mfa-admin", false, "prepare the administrator the contract hooks enroll in real MFA")
	flag.Parse()
	modes := 0
	for _, on := range []bool{*linkOnly, *apiKeys, *domainTenants, *mfaAdmin} {
		if on {
			modes++
		}
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
		} else if *linkOnly {
			_, err = prepareLinkFixture(database, *domain)
		} else {
			err = prepareQueueFixtures(database, *domain)
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
	account := models.Mailbox{DomainID: hosted.ID, LocalPart: local, Address: local + "@" + domain,
		DisplayName: "Contract API keys", Active: true, QuotaBytes: hosted.DefaultQuotaBytes}
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		if err := grants.EnsureReceive(tx, &account); err != nil {
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
		account := models.Mailbox{DomainID: hosted.ID, LocalPart: "contract-mfa-admin", Address: mfaAdminAddress,
			DisplayName: "Contract MFA admin", Active: true, QuotaBytes: hosted.DefaultQuotaBytes}
		result := tx.Where("address = ?", account.Address).FirstOrCreate(&account)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if err := grants.EnsureReceive(tx, &account); err != nil {
				return err
			}
		}
		creds := repositories.NewCredentialRepository(tx)
		current, err := creds.MatchPassword(account.ID, mfaAdminPassword, time.Now())
		if err != nil {
			return err
		}
		if current == nil {
			hash, err := auth.HashPassword(mfaAdminPassword)
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
		_, err = grants.EnsureSystem(tx, account.ID, role.ID, grants.All, models.GrantActive)
		return err
	})
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
				if err := grants.EnsureReceive(tx, &account); err != nil {
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
		hosted := models.Domain{Name: "example.com", ServerType: "traditional"}
		if err := tx.Where("name = ?", hosted.Name).FirstOrCreate(&hosted).Error; err != nil {
			return err
		}
		account = models.Mailbox{DomainID: hosted.ID, LocalPart: "someone", Address: "someone@example.com", Active: true}
		result := tx.Where("address = ?", account.Address).FirstOrCreate(&account)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if err := grants.EnsureReceive(tx, &account); err != nil {
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
