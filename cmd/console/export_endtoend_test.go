package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// The "done when" of exporting mail (#1577): an account exported with
// export-maildir imports into another REST Mail server through import-maildir
// with its folders, message bytes and flags intact. Both servers are the real
// public router on PostgreSQL, each on a database of its own.

// exportKey is an API key of the destination's administrator that may read
// the domain's mail, as an operator exporting an account would hold.
func (s *migrationServer) exportKey(t *testing.T) string {
	t.Helper()
	var holder models.Mailbox
	if err := s.db.Where("address = ?", "migrator@"+migrationDomain).First(&holder).Error; err != nil {
		t.Fatalf("find the administrator: %v", err)
	}
	_, key, err := repositories.NewCredentialRepository(s.db).CreateAPIKey(holder.ID, holder.ID, "export", []auth.ScopedPermission{
		{Capability: models.CapMailRead, Scope: grants.Domain(migrationDomain)},
	}, nil)
	if err != nil {
		t.Fatalf("create the export key: %v", err)
	}
	return key
}

func (s *migrationServer) newAccount(t *testing.T, address string) uint {
	t.Helper()
	code, outcome := s.importPasswdFile(t, s.session, address+":"+dovecotSHA512Hash+"\n")
	if code != exitOK || len(outcome["created"]) != 1 {
		t.Fatalf("create %s: exit %d, %+v", address, code, outcome)
	}
	return outcome["created"][0].ID
}

// keptMessage is what must survive a round trip, without the ids each server
// gives its own rows.
type keptMessage struct {
	Folder      string
	Raw         string
	IsRead      bool
	IsAnswered  bool
	IsFlagged   bool
	IsDraft     bool
	IMAPDeleted bool
	Keywords    string
	ReceivedAt  string
}

func (s *migrationServer) keptMail(t *testing.T, account uint) []keptMessage {
	t.Helper()
	var rows []struct {
		Folder      string
		Raw         string
		IsRead      bool
		IsAnswered  bool
		IsFlagged   bool
		IsDraft     bool
		IMAPDeleted bool `gorm:"column:imap_deleted"`
		Keywords    string
		ReceivedAt  time.Time
	}
	if err := s.db.Raw(`SELECT folder, raw_message AS raw, is_read, is_answered, is_flagged, is_draft, imap_deleted,
		COALESCE(keywords::text, '') AS keywords, received_at FROM messages WHERE mailbox_id = ? AND is_deleted = false`, account).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	kept := make([]keptMessage, 0, len(rows))
	for _, row := range rows {
		var keywords []string
		_ = json.Unmarshal([]byte(row.Keywords), &keywords)
		for i := range keywords {
			keywords[i] = strings.ToLower(keywords[i])
		}
		slices.Sort(keywords)
		kept = append(kept, keptMessage{row.Folder, row.Raw, row.IsRead, row.IsAnswered, row.IsFlagged, row.IsDraft, row.IMAPDeleted,
			strings.Join(keywords, " "), row.ReceivedAt.UTC().Format(time.RFC3339Nano)})
	}
	slices.SortFunc(kept, func(a, b keptMessage) int { return strings.Compare(a.Folder+"\x00"+a.Raw, b.Folder+"\x00"+b.Raw) })
	return kept
}

func TestExportedAccountImportsIntoAnotherServerIntact(t *testing.T) {
	source := newMigrationServer(t)
	from := source.newAccount(t, "from@"+migrationDomain)
	// The account's mail is put there through the public API: folders, a
	// nested folder, every flag, a keyword, a Deleted message and a message
	// without a Message-ID.
	maildir, messages, _ := migrationMaildir(t)
	if got := source.importMaildir(t, from, maildir); got.Imported != len(messages) {
		t.Fatalf("seed the source account: %+v", got)
	}
	want := source.keptMail(t, from)
	if len(want) != len(messages) {
		t.Fatalf("the source account holds %d messages, want %d", len(want), len(messages))
	}

	export := filepath.Join(t.TempDir(), "export")
	var out, errs bytes.Buffer
	code := runExportMaildir(context.Background(), []string{"--server", source.apiURL, "--account-id", strconv.FormatUint(uint64(from), 10), "--path", export, "--json"},
		source.exportKey(t), source.client, &out, &errs)
	if code != exitOK {
		t.Fatalf("export-maildir: exit %d: %s", code, &errs)
	}
	var summary struct{ Messages int }
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary.Messages != len(messages) {
		t.Fatalf("export summary %s (%v), want %d messages", &out, err, len(messages))
	}

	destination := newMigrationServer(t)
	to := destination.newAccount(t, "to@"+migrationDomain)
	if got := destination.importMaildir(t, to, export); got != (maildirSummary{Messages: len(messages), Imported: len(messages)}) {
		t.Fatalf("import the export: %+v, want all %d imported", got, len(messages))
	}
	if got := destination.keptMail(t, to); !reflect.DeepEqual(got, want) {
		t.Fatalf("the round trip changed the mail:\nsource      %+v\ndestination %+v", want, got)
	}
}
