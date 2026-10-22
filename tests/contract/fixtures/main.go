// This command prepares state only for the disposable restmail.test contract topology.
package main

import (
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
	"gorm.io/gorm"
)

const retryRecipient = "contract-retry@retry.invalid"

func main() {
	domain := flag.String("test-domain", "", "confirm the disposable restmail.test topology")
	flag.Parse()
	if *domain != "restmail.test" || flag.NArg() != 0 {
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
		_, err = prepareRetryFixture(database, *domain)
	}
	if err == nil {
		_, err = prepareLinkFixture(database, *domain)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot prepare the contract fixtures")
		os.Exit(1)
	}
	fmt.Println("contract retry and account-link fixtures prepared")
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
			if credential == nil {
				return errors.New("contract link probe account has a different password")
			}
			return nil
		}
		hash, err := auth.HashPassword("x")
		if err != nil {
			return err
		}
		if _, err := creds.SetPassword(account.ID, hash, nil, time.Now()); err != nil {
			return err
		}
		return tx.Create(&models.QuotaUsage{MailboxID: account.ID}).Error
	})
	return account.ID, err
}

func prepareRetryFixture(database *gorm.DB, domain string) (uint, error) {
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
	// The sent-message example can already be delivered when its retry runs.
	// A separate deferred entry cannot be claimed until the retry example itself
	// makes it due. Create a fresh entry on every run; listQueue orders newest first.
	now := time.Now()
	item := models.OutboundQueue{
		Sender: "admin@restmail.test", Recipient: retryRecipient, Domain: "retry.invalid",
		RawMessage: "From: admin@restmail.test\r\nTo: " + retryRecipient + "\r\nSubject: contract retry fixture\r\n\r\nContract fixture.\r\n",
		Status:     "deferred", NextAttempt: now.Add(24 * time.Hour), ExpiresAt: now.Add(72 * time.Hour),
	}
	if err := database.Create(&item).Error; err != nil {
		return 0, err
	}
	return item.ID, nil
}
