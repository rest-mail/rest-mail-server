package handlers

import (
	"fmt"
	"os"
	"testing"
	"time"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
)

// The folder listing's uid_next is the folder's IMAP UIDNEXT. It must not fall
// when the newest message is deleted for good, must rise when a message is
// stored or moved into the folder, and starts at 1 for a folder that never held
// one (RFC 3501 §2.3.1.1, #804). The marks are kept by triggers in the
// database, so this runs against Postgres with the server's own migration.
func TestFolderUIDNextSurvivesPermanentDelete(t *testing.T) {
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		testdb.Unreachable(t, "no database reachable: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	name := fmt.Sprintf("restmail_uidnext_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)").Error; err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	db, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dbSQL, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbSQL.Close() })
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	fixture := seedMsgDeleteFixture(t, db, 0)
	h := NewMessageHandler(db, nil, nil, "", nil)

	store := func(folder string) models.Message {
		t.Helper()
		m := models.Message{MailboxID: fixture.mailbox.ID, Folder: folder, Sender: "sender@example.test"}
		if err := db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
		return m
	}
	uidNext := func() map[string]uint64 {
		t.Helper()
		list, err := h.listFolders(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), accountPath{ID: uint32(fixture.mailbox.ID)})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]uint64{}
		for _, f := range list.Data {
			out[f.Folder] = f.UIDNext
		}
		return out
	}

	first, newest := store("INBOX"), store("INBOX")
	if got := uidNext()["INBOX"]; got != uint64(newest.ID)+1 {
		t.Fatalf("INBOX uid_next %d, want %d", got, newest.ID+1)
	}
	if got := uidNext()["Sent"]; got != 1 {
		t.Fatalf("an unused Sent has uid_next %d, want 1", got)
	}

	// EXPUNGE deletes for good; the mark keeps the newest ID.
	if err := db.Delete(&models.Message{}, newest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got := uidNext()["INBOX"]; got != uint64(newest.ID)+1 {
		t.Fatalf("after deleting the newest, INBOX uid_next %d, want %d", got, newest.ID+1)
	}

	// A flag change does not move the mark; a move into Archive raises Archive's.
	if err := db.Model(&models.Message{}).Where("id = ?", first.ID).Update("is_read", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Message{}).Where("id = ?", first.ID).Update("folder", "Archive").Error; err != nil {
		t.Fatal(err)
	}
	got := uidNext()
	if got["Archive"] != uint64(first.ID)+1 || got["INBOX"] != uint64(newest.ID)+1 {
		t.Fatalf("after moving %d to Archive: %v", first.ID, got)
	}

	// New mail gets a higher ID than the expunged one, and moves UIDNEXT on.
	later := store("INBOX")
	if later.ID <= newest.ID {
		t.Fatalf("new message got ID %d, not above the expunged %d", later.ID, newest.ID)
	}
	if got := uidNext()["INBOX"]; got != uint64(later.ID)+1 {
		t.Fatalf("after new mail, INBOX uid_next %d, want %d", got, later.ID+1)
	}
}
