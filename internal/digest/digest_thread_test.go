package digest

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDigestNamesItsThread(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for a PostgreSQL digest proof")
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_USER"), os.Getenv("DB_PASS"), os.Getenv("DB_NAME"))
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Message{}, &models.Quarantine{}); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("digest-%d.test", time.Now().UnixNano())
	domain := models.Domain{Name: name, Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "user", Address: "user@" + name, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	NewWorker(db, time.Hour).deliverDigest(mailbox.ID, []models.Quarantine{{MailboxID: mailbox.ID, Sender: "a@example.test", Subject: "held"}})
	var delivered models.Message
	if err := db.Where("mailbox_id = ?", mailbox.ID).First(&delivered).Error; err != nil {
		t.Fatal(err)
	}
	// getMessage links a message to its thread, so the digest needs one to link to.
	if !strings.HasSuffix(delivered.MsgID, "@"+name+">") || delivered.ThreadID != delivered.MsgID {
		t.Fatalf("digest message_id %q, thread_id %q; want one thread named by its own Message-ID", delivered.MsgID, delivered.ThreadID)
	}
}
