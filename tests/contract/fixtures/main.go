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
	flag.Parse()
	modes := 0
	for _, on := range []bool{*linkOnly, *apiKeys, *domainTenants} {
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
		} else if *linkOnly {
			_, err = prepareLinkFixture(database, *domain)
		} else {
			_, err = prepareRetryFixture(database, *domain)
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
			if err := tx.Where("address = ?", account.Address).FirstOrCreate(&account).Error; err != nil {
				return err
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
			tenants[d.tenant] = domainTenant{Address: account.Address, DomainID: hosted.ID}
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

func prepareRetryFixture(database *gorm.DB, domain string) (uint, error) {
	if domain != "restmail.test" {
		return 0, errors.New("contract fixtures require restmail.test")
	}
	plan, err := fixtureplan.Default()
	if err != nil {
		return 0, err
	}
	entry := plan.Queue["retry"]
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
