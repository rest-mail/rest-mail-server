package db_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

// freshDatabase is a new database with the server's own migration, dropped
// when the test ends.
func freshDatabase(t *testing.T, prefix string) *gorm.DB {
	t.Helper()
	env := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DB_PORT", "5432"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DBHost: env("DB_HOST", "localhost"), DBPort: port,
		DBName: env("DB_NAME", "restmail"), DBUser: env("DB_USER", "restmail"), DBPass: env("DB_PASS", "restmail")}
	server, err := rmdb.Connect(&cfg)
	if err != nil {
		testdb.Unreachable(t, "no database reachable: %v", err)
	}
	serverSQL, err := server.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSQL.Close() })
	name := fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), time.Now().UnixNano())
	if err := server.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Exec("DROP DATABASE " + name + " WITH (FORCE)").Error; err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	database, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

// modSeqFixture is one mailbox in a fresh database, with helpers that read
// the mod-sequence state the triggers keep.
type modSeqFixture struct {
	t       *testing.T
	db      *gorm.DB
	mailbox models.Mailbox
}

func newModSeqFixture(t *testing.T) *modSeqFixture {
	database := freshDatabase(t, "restmail_modseq")
	domain := models.Domain{Name: "modseq.test"}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@modseq.test", Active: true, QuotaBytes: 1 << 30}
	if err := database.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	return &modSeqFixture{t: t, db: database, mailbox: mailbox}
}

func (f *modSeqFixture) store(folder string) models.Message {
	f.t.Helper()
	m := models.Message{MailboxID: f.mailbox.ID, Folder: folder, Sender: "sender@example.test"}
	if err := f.db.Create(&m).Error; err != nil {
		f.t.Fatal(err)
	}
	return f.reload(m.ID)
}

func (f *modSeqFixture) reload(id uint) models.Message {
	f.t.Helper()
	var m models.Message
	if err := f.db.First(&m, id).Error; err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *modSeqFixture) update(id uint, column string, value any) models.Message {
	f.t.Helper()
	if err := f.db.Model(&models.Message{}).Where("id = ?", id).Update(column, value).Error; err != nil {
		f.t.Fatal(err)
	}
	return f.reload(id)
}

func (f *modSeqFixture) highest(folder string) uint64 {
	f.t.Helper()
	var row models.FolderModSeq
	err := f.db.Where("mailbox_id = ? AND folder = ?", f.mailbox.ID, folder).Take(&row).Error
	if err != nil {
		f.t.Fatalf("folder %s has no mod-sequence: %v", folder, err)
	}
	return row.HighestModSeq
}

// vanished maps each UID that left folder to the mod-sequence it left at.
func (f *modSeqFixture) vanished(folder string) map[uint64]uint64 {
	f.t.Helper()
	var rows []models.FolderVanishedUID
	if err := f.db.Where("mailbox_id = ? AND folder = ?", f.mailbox.ID, folder).Find(&rows).Error; err != nil {
		f.t.Fatal(err)
	}
	out := map[uint64]uint64{}
	for _, r := range rows {
		out[r.UID] = r.ModSeq
	}
	return out
}

// Each folder's mod-sequence rises on every change an IMAP client can see
// there, and the message changed takes the new value as its MODSEQ (RFC 7162
// §3.1; go-imap#162): a new message, a flag change, a move (in the folder it
// lands in), and an expunge. A write that changes nothing visible leaves both
// alone (§3.1.11).
func TestModSeqRisesOnEveryVisibleChange(t *testing.T) {
	f := newModSeqFixture(t)

	first := f.store("INBOX")
	if first.ModSeq == 0 || first.ModSeq != f.highest("INBOX") {
		t.Fatalf("new message mod_seq %d, INBOX highest %d", first.ModSeq, f.highest("INBOX"))
	}
	second := f.store("INBOX")
	if second.ModSeq <= first.ModSeq || second.ModSeq != f.highest("INBOX") {
		t.Fatalf("second message mod_seq %d after %d; INBOX highest %d", second.ModSeq, first.ModSeq, f.highest("INBOX"))
	}

	for _, change := range []struct {
		column string
		value  any
	}{
		{"is_read", true},
		{"is_answered", true},
		{"is_flagged", true},
		{"is_starred", true},
		{"is_draft", true},
		{"imap_deleted", true},
		{"keywords", `["$Forwarded"]`},
	} {
		before := f.highest("INBOX")
		got := f.update(first.ID, change.column, change.value)
		if got.ModSeq <= before || f.highest("INBOX") != got.ModSeq {
			t.Fatalf("%s: mod_seq %d, INBOX highest %d, was %d", change.column, got.ModSeq, f.highest("INBOX"), before)
		}
	}

	// The same value again, or a column no client sees, changes nothing.
	before, msgBefore := f.highest("INBOX"), f.reload(first.ID).ModSeq
	f.update(first.ID, "is_read", true)
	f.update(first.ID, "subject", "renamed")
	if got := f.reload(first.ID).ModSeq; got != msgBefore || f.highest("INBOX") != before {
		t.Fatalf("an invisible write moved mod_seq %d→%d, INBOX %d→%d", msgBefore, got, before, f.highest("INBOX"))
	}
	// Nor can a write set the mod-sequence itself.
	f.update(first.ID, "mod_seq", 1)
	if got := f.reload(first.ID).ModSeq; got != msgBefore {
		t.Fatalf("writing mod_seq directly set it to %d", got)
	}
}

// A message that leaves a folder, by a move, a permanent delete or a soft
// delete, raises that folder's mod-sequence and is recorded with it, so
// QRESYNC can answer VANISHED (EARLIER) (RFC 7162 §3.2.5.1). A move raises the
// destination's too, and coming back takes the record out. Nothing lowers a
// folder's mod-sequence, and deleting the mailbox removes it all.
func TestModSeqRecordsExpungedUIDs(t *testing.T) {
	f := newModSeqFixture(t)
	moved, deleted, trashed, kept := f.store("INBOX"), f.store("INBOX"), f.store("INBOX"), f.store("INBOX")

	before := f.highest("INBOX")
	got := f.update(moved.ID, "folder", "Archive")
	if f.highest("INBOX") <= before {
		t.Fatalf("a move out left INBOX at %d", f.highest("INBOX"))
	}
	if got.ModSeq != f.highest("Archive") {
		t.Fatalf("moved message mod_seq %d, Archive highest %d", got.ModSeq, f.highest("Archive"))
	}

	if err := f.db.Delete(&models.Message{}, deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.update(trashed.ID, "is_deleted", true)

	vanished := f.vanished("INBOX")
	if len(vanished) != 3 {
		t.Fatalf("INBOX vanished %v, want UIDs %d, %d and %d", vanished, moved.ID, deleted.ID, trashed.ID)
	}
	if !(vanished[uint64(moved.ID)] < vanished[uint64(deleted.ID)] && vanished[uint64(deleted.ID)] < vanished[uint64(trashed.ID)]) ||
		vanished[uint64(trashed.ID)] != f.highest("INBOX") {
		t.Fatalf("vanished mod-sequences %v do not rise to INBOX's highest %d", vanished, f.highest("INBOX"))
	}
	if _, ok := vanished[uint64(kept.ID)]; ok {
		t.Fatalf("a message still in INBOX is recorded as vanished")
	}

	// Moving the message back takes its record out of INBOX and records it in
	// Archive.
	f.update(moved.ID, "folder", "INBOX")
	if _, ok := f.vanished("INBOX")[uint64(moved.ID)]; ok {
		t.Fatal("a message moved back is still recorded as vanished from INBOX")
	}
	if _, ok := f.vanished("Archive")[uint64(moved.ID)]; !ok {
		t.Fatal("a message moved out of Archive is not recorded there")
	}

	// Deleting every message never lowers the folder's mod-sequence.
	before = f.highest("INBOX")
	if err := f.db.Where("mailbox_id = ?", f.mailbox.ID).Delete(&models.Message{}).Error; err != nil {
		t.Fatal(err)
	}
	if f.highest("INBOX") < before {
		t.Fatalf("INBOX fell from %d to %d", before, f.highest("INBOX"))
	}

	// The mailbox's own deletion takes its rows with it.
	f.store("INBOX")
	if err := f.db.Delete(&f.mailbox).Error; err != nil {
		t.Fatal(err)
	}
	var left int64
	f.db.Model(&models.FolderModSeq{}).Where("mailbox_id = ?", f.mailbox.ID).Count(&left)
	if left != 0 {
		t.Fatalf("%d folder mod-sequences outlived their mailbox", left)
	}
}
