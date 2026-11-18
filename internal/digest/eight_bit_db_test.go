package digest

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

func digestEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// openDigestDB is a scratch database with the whole schema, so a digest sees only what the test holds.
func openDigestDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(digestEnv("DB_PORT", "5432"))
	cfg := &config.Config{DBHost: digestEnv("DB_HOST", "localhost"), DBPort: port, DBName: "postgres",
		DBUser: digestEnv("DB_USER", "restmail"), DBPass: digestEnv("DB_PASS", "restmail")}
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		testdb.Unreachable(t, "digest database unavailable: %v", err)
	}
	adminPool, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminPool.Close() })
	name := fmt.Sprintf("restmail_digest_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	db, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestDigestOfEightBitHolds: holds of mail a text column can't represent reach the owner in a
// digest that can be stored and read, whose subject and preview show them, and the digest leaves
// each hold's source as it was.
func TestDigestOfEightBitHolds(t *testing.T) {
	db := openDigestDB(t)
	domain := models.Domain{Name: "digest.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@digest.test", Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// A preview of exactly 99 ASCII octets and a two-octet character, which the digest cuts in two.
	cut := strings.Repeat("a", 99) + "é tail"
	sources := map[string]string{
		"Latin-1 no charset": "From: s@remote.test\r\nSubject: latin\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: 8bit\r\n\r\nStra\xdfe\r\n",
		"no MIME headers":    "From: s@remote.test\r\nSubject: nomime\r\n\r\nGr\xfc\xdfe\r\n",
		"binary attachment":  "From: s@remote.test\r\nSubject: binary\r\nMIME-Version: 1.0\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: 8bit\r\n\r\n\x01\xfe\xff\r\n",
		"NUL in text body":   "From: s@remote.test\r\nSubject: nul\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: 8bit\r\n\r\nbe\x00fore \xe9\r\n",
	}
	ids := map[string]uint{}
	for name, source := range sources {
		hold := models.Quarantine{MailboxID: mailbox.ID, Sender: "s@remote.test", Subject: "held \xe9 " + name, BodyPreview: "be\x00fore \xe9 " + name,
			RawMessage: source, QuarantineReason: "spf", ReceivedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := db.Create(&hold).Error; err != nil {
			t.Fatalf("hold %s: %v", name, err)
		}
		ids[name] = hold.ID
	}
	long := models.Quarantine{MailboxID: mailbox.ID, Sender: "s@remote.test", Subject: "cut", BodyPreview: cut, RawMessage: "Subject: cut\r\n\r\nbody\r\n",
		QuarantineReason: "spf", ReceivedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.Create(&long).Error; err != nil {
		t.Fatal(err)
	}

	NewWorker(db, time.Hour).SendDigests()

	var digests []models.Message
	if err := db.Where("mailbox_id = ?", mailbox.ID).Find(&digests).Error; err != nil {
		t.Fatal(err)
	}
	if len(digests) != 1 {
		t.Fatalf("digests = %d, want 1: a digest of 8-bit holds was not stored", len(digests))
	}
	digest := digests[0]
	if !models.SourceFitsText(digest.BodyText) || !models.SourceFitsText(digest.BodyHTML) {
		t.Fatalf("digest is not readable text: %q", digest.BodyText)
	}
	if !strings.Contains(digest.Subject, "5 message(s)") {
		t.Fatalf("subject %q", digest.Subject)
	}
	for name := range sources {
		if !strings.Contains(digest.BodyText, "held � "+name) || !strings.Contains(digest.BodyHTML, "held � "+name) {
			t.Fatalf("%s is not in the digest: %q", name, digest.BodyText)
		}
	}
	if !strings.Contains(digest.BodyText, strings.Repeat("a", 99)) {
		t.Fatalf("the long preview is not in the digest: %q", digest.BodyText)
	}
	for name, source := range sources {
		var hold models.Quarantine
		if err := db.First(&hold, ids[name]).Error; err != nil {
			t.Fatal(err)
		}
		if hold.RawMessage != source || hold.Released {
			t.Fatalf("%s: the digest changed the hold: released %v, %q", name, hold.Released, hold.RawMessage)
		}
	}
}
