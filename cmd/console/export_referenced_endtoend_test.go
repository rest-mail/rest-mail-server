package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	"gorm.io/gorm"
)

func TestExportPreservesInlineAndReferencedSourceOctets(t *testing.T) {
	var routes *api.Routers
	s := newMigrationServerWithRouter(t, func(r *api.Routers) { routes = r })
	account := s.newAccount(t, "source-owners@"+migrationDomain)
	cfg := &config.Config{
		Attachments: config.AttachmentStorageConfig{Type: "filesystem"},
		Messages:    config.MessageStorageConfig{Directory: t.TempDir()},
	}
	store, err := messagestore.NewSources(s.db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	routes.Messages.SetMessageSources(store)

	const head = "Subject: binary export\r\n\r\n"
	const raw = head + "Line one\n.Line two\r\n\x00\xff"
	object, err := store.Stage(t.Context(), strings.NewReader(raw), int64(len(raw)), []byte(head))
	if err != nil {
		t.Fatal(err)
	}
	inline := models.Message{MailboxID: account, Folder: "INBOX", Sender: "sender@example.test", RawMessage: raw, RawSize: len(raw)}
	if err := s.db.Create(&inline).Error; err != nil {
		t.Fatal(err)
	}
	referenced := models.Message{MailboxID: account, Folder: "Archive", Sender: "sender@example.test", SourceRef: &object.Ref}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := store.Publish(t.Context(), tx, object.Ref, 1); err != nil {
			return err
		}
		return store.CreateMessage(t.Context(), tx, &referenced)
	}); err != nil {
		t.Fatal(err)
	}

	export := filepath.Join(t.TempDir(), "export")
	var out, errs bytes.Buffer
	args := []string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(account), 10), "--path", export, "--json"}
	if code := runExportMaildir(t.Context(), args, s.exportKey(t), s.client, &out, &errs); code != exitOK {
		t.Fatalf("export: exit %d: %s %s", code, &out, &errs)
	}
	for _, folder := range []string{"", ".Archive"} {
		cur := filepath.Join(export, folder, "cur")
		files, err := os.ReadDir(cur)
		if err != nil || len(files) != 1 {
			t.Fatalf("%s: got %d files, error %v", cur, len(files), err)
		}
		got, err := os.ReadFile(filepath.Join(cur, files[0].Name()))
		if err != nil || string(got) != raw {
			t.Fatalf("%s: got %q, error %v", cur, got, err)
		}
	}
}
