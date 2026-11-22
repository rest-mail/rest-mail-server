package localstore

import (
	"context"
	"testing"
)

func TestSQLiteSchemaContract(t *testing.T) {
	store, err := Open(t.TempDir(), AccountKey("server", "owner", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"migrations", "accounts", "folders", "messages", "bodies", "attachments", "account_catalog", "message_fts"} {
		var count int
		if err = store.reader.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing schema object %s: %v", table, err)
		}
	}
	var timeout int
	if err = store.reader.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("busy timeout=%d: %v", timeout, err)
	}
	if store.writer.Stats().MaxOpenConnections != 1 {
		t.Fatal("multiple writers configured")
	}
	if err = store.Identity(context.Background(), "opaque-account"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.writer.Exec("INSERT INTO attachments VALUES(1,999,'file.txt','text/plain',1)"); err == nil {
		t.Fatal("attachment foreign key not enforced")
	}
}
