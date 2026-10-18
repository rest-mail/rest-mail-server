// This command prepares state only for the disposable restmail.test contract topology.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
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
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot prepare the contract retry fixture")
		os.Exit(1)
	}
	fmt.Println("contract retry fixture prepared")
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
