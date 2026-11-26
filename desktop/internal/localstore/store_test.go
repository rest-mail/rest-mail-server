package localstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir(), AccountKey("https://mail.test", "owner@test", 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func fixture(id uint, subject, body string) apitypes.Message {
	return apitypes.Message{ID: id, Folder: "INBOX", Subject: subject, BodyText: body, Sender: "jose@xn--bcher-kva.test", SenderName: "José", RecipientsTo: json.RawMessage(`["reader@example.test"]`), RecipientsCc: json.RawMessage(`["copy@example.test"]`), ReceivedAt: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), SizeBytes: len(body)}
}

func TestSchemaMigrationsWALAndResume(t *testing.T) {
	root := t.TempDir()
	key := AccountKey("server", "owner", 1)
	store, err := Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	var mode string
	if err = store.reader.QueryRow("SELECT max(version) FROM migrations").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err = store.reader.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("mode=%s err=%v", mode, err)
	}
	if err = store.Put(context.Background(), fixture(1, "persisted", "stored body"), true); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	message, err := store.Get(context.Background(), 1, true)
	if err != nil || message.BodyText != "stored body" {
		t.Fatalf("%+v %v", message, err)
	}
	if info, err := os.Stat(store.path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions: %v %v", info, err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v %v", info, err)
	}
}

func TestForwardMigrationRebuildsExistingHeaders(t *testing.T) {
	root := t.TempDir()
	key := AccountKey("server", "owner", 1)
	path := filepath.Join(root, key+".sqlite")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`CREATE TABLE migrations(version INTEGER PRIMARY KEY,applied_at TEXT NOT NULL);` + migrations[0] + `INSERT INTO migrations VALUES(1,'then');`)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(fixture(1, "migrated", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO messages(id,folder,thread_id,subject,sender,recipients_to,recipients_cc,received_at,size_bytes,is_read,has_attachments,metadata) VALUES(1,'INBOX','','migrated','','','',0,0,0,0,?)`, string(metadata)); err != nil {
		t.Fatal(err)
	}
	database.Close()
	store, err := Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	result, err := store.Search(context.Background(), "migrated", "", 50, 0)
	if err != nil || len(result.Data) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRejectsFutureSchemaAndSymlinks(t *testing.T) {
	root := t.TempDir()
	key := AccountKey("server", "owner", 1)
	store, err := Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.writer.Exec("INSERT INTO migrations VALUES(999,'future')")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if _, err = Open(root, key); err == nil {
		t.Fatal("opened a future schema")
	}
	otherKey := AccountKey("other", "owner", 1)
	if err = os.Symlink(store.path, filepath.Join(root, otherKey+".sqlite")); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(root, otherKey); err == nil {
		t.Fatal("followed a symlink")
	}
}

func TestTestCopyIsolationIncludingInvalidInstance(t *testing.T) {
	config := t.TempDir()
	for _, instance := range []string{"sqlite", "INVALID / instance"} {
		root, err := Root(config, instance)
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
		if strings.HasPrefix(root, config) {
			t.Fatal("test copy used owner config")
		}
		store, err := Open(root, AccountKey("server", "owner", 1))
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
	}
	if _, err := os.Stat(filepath.Join(config, "REST Mail")); !os.IsNotExist(err) {
		t.Fatalf("owner directory touched: %v", err)
	}
	if AccountKey("one", "owner", 1) == AccountKey("two", "owner", 1) || AccountKey("one", "owner", 1) == AccountKey("one", "other", 1) {
		t.Fatal("account namespace collision")
	}
}

func TestSearchOperatorsIDNDiacriticsAndRanking(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	primary := fixture(1, "Café planning", "rocket launch résumé")
	primary.HasAttachments = true
	other := fixture(2, "Other", "Café planning rocket launch")
	other.Folder = "Archive"
	other.IsRead = true
	if err := store.PutBatch(ctx, []apitypes.Message{primary, other}, true, 0); err != nil {
		t.Fatal(err)
	}
	cases := map[string]int{"cafe": 2, "resume": 1, "from:jose": 2, "from:jose@bücher.test": 2, "from:jose@xn--bcher-kva.test": 2, "to:reader@example.test": 2, "subject:cafe": 1, "has:attachment": 1, "is:unread": 1, "before:2026-11-21": 2, "before:2026-11-20": 0, "after:2026-11-20": 2, "after:2026-11-21": 0, "in:archive": 1, `subject:"Café planning" has:attachment is:unread in:INBOX`: 1, `"rocket launch"`: 2, "NOT": 0, `subject:' OR 1=1 --`: 0}
	for query, want := range cases {
		t.Run(query, func(t *testing.T) {
			result, err := store.Search(ctx, query, "", 50, 0)
			if err != nil || len(result.Data) != want {
				t.Fatalf("%s: count=%d want=%d error=%v", query, len(result.Data), want, err)
			}
		})
	}
	result, err := store.Search(ctx, "cafe", "", 50, 0)
	if err != nil || result.Data[0].ID != 1 || !strings.Contains(result.Data[0].Snippet, "⟦") {
		t.Fatalf("ranking/highlight: %+v %v", result, err)
	}
	for _, query := range []string{`"unfinished`, "before:tomorrow", "from:", "is:read", "has:dog", "unknown:thing"} {
		if _, err := store.Search(ctx, query, "", 50, 0); err == nil {
			t.Fatalf("accepted %q", query)
		}
	}
}

func TestHTMLExtractedOnlyAndHeaderRefreshPreservesBody(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	message := fixture(1, "HTML", "")
	message.BodyHTML = `<p>Crème &amp; brûlée</p><script>secretcode</script><style>stylesheetword</style><img alt="attributeonly">`
	if err := store.Put(ctx, message, true); err != nil {
		t.Fatal(err)
	}
	message.BodyHTML = ""
	message.IsRead = true
	if err := store.Put(ctx, message, false); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{"creme": 1, "brulee": 1, "secretcode": 0, "stylesheetword": 0, "attributeonly": 0, "is:unread": 0} {
		result, err := store.Search(ctx, query, "", 50, 0)
		if err != nil || len(result.Data) != want {
			t.Fatalf("%s: %+v %v", query, result, err)
		}
	}
	cached, err := store.Get(ctx, 1, true)
	if err != nil || !strings.Contains(cached.BodyHTML, "<script>") {
		t.Fatalf("original body lost: %+v %v", cached, err)
	}
	if err = store.Remove(ctx, 1); err != nil {
		t.Fatal(err)
	}
	result, err := store.Search(ctx, "brulee", "", 50, 0)
	if err != nil || len(result.Data) != 0 {
		t.Fatalf("stale FTS row: %+v %v", result, err)
	}
}

func TestScanCheckpointPrunesMovesDeletesAndResumes(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.PutFolders(ctx, []apitypes.FolderSummary{{Folder: "INBOX"}, {Folder: "Archive"}}); err != nil {
		t.Fatal(err)
	}
	_, generation, err := store.BeginScan(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SavePage(ctx, "INBOX", "next", generation, 42, []apitypes.Message{fixture(1, "first", ""), fixture(2, "second", "")}, false); err != nil {
		t.Fatal(err)
	}
	cursor, resumed, err := store.BeginScan(ctx, "INBOX")
	if err != nil || cursor != "next" || generation != resumed {
		t.Fatalf("resume: %s %d %v", cursor, resumed, err)
	}
	if err = store.SavePage(ctx, "INBOX", "", generation, 42, nil, true); err != nil {
		t.Fatal(err)
	}
	_, generation, err = store.BeginScan(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	moved := fixture(2, "second", "")
	moved.Folder = "Archive"
	moved.IsRead = true
	if err = store.Put(ctx, moved, false); err != nil {
		t.Fatal(err)
	}
	if err = store.SavePage(ctx, "INBOX", "", generation, 42, []apitypes.Message{fixture(3, "arrival", "")}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, 1, false); err != sql.ErrNoRows {
		t.Fatalf("deleted row remains: %v", err)
	}
	message, err := store.Get(ctx, 2, false)
	if err != nil || message.Folder != "Archive" || !message.IsRead {
		t.Fatalf("move lost: %+v %v", message, err)
	}
}
