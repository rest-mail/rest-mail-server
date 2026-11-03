package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestImportedAliasAndForwardRowsAreUnchangedOnSecondRun(t *testing.T) {
	server := newMigrationServer(t)
	code, accounts := server.importPasswdFile(t, server.session,
		"alice@"+migrationDomain+":"+dovecotSHA512Hash+"\nbob@"+migrationDomain+":"+dovecotBLFHash+"\n")
	if code != exitOK || len(accounts["created"]) != 2 {
		t.Fatalf("destination accounts: exit %d, %+v", code, accounts)
	}
	var holder models.Mailbox
	if err := server.db.Where("address = ?", "migrator@"+migrationDomain).First(&holder).Error; err != nil {
		t.Fatal(err)
	}
	_, key, err := repositories.NewCredentialRepository(server.db).CreateAPIKey(holder.ID, holder.ID, "alias-migration", []auth.ScopedPermission{
		{Capability: "accounts:read", Scope: grants.Domain(migrationDomain)},
		{Capability: "accounts:write", Scope: grants.Domain(migrationDomain)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "postfix-virtual.cf")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	importFile := func(token string) (int, map[string][]aliasImportResult) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := runImportAliases(context.Background(), []string{"--server", server.apiURL, "--json", path}, strings.NewReader(""), token, server.client, &stdout, &stderr)
		var result map[string][]aliasImportResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("exit %d stdout %s stderr %s: %v", code, &stdout, &stderr, err)
		}
		return code, result
	}
	stored := func() []models.Alias {
		t.Helper()
		var rows []models.Alias
		if err := server.db.Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}
	write("# aliases and forwarding exported from the old bundle\n" +
		"Sales@EXAMPLE.test alice@example.test\n" +
		"bob@example.test alice@example.test\n" +
		"team@example.test alice@example.test,\n\t bob@example.test\n")
	code, first := importFile(key)
	if code != exitOK || len(first["created"]) != 4 || len(first["failed"]) != 0 {
		t.Fatalf("first run exit %d: %+v", code, first)
	}
	afterFirst := stored()
	want := [][2]string{
		{"sales@example.test", "alice@example.test"},
		{"bob@example.test", "alice@example.test"},
		{"team@example.test", "alice@example.test"},
		{"team@example.test", "bob@example.test"},
	}
	if len(afterFirst) != len(want) {
		t.Fatalf("stored alias and forward rows: %+v", afterFirst)
	}
	for index, row := range afterFirst {
		if !row.Active || row.SourceAddress != want[index][0] || row.DestinationAddress != want[index][1] || row.DomainID != holder.DomainID {
			t.Fatalf("row %d: %+v, want %v", index, row, want[index])
		}
		t.Logf("stored alias/forward row: id=%d source=%s destination=%s active=%v", row.ID, row.SourceAddress, row.DestinationAddress, row.Active)
	}
	code, second := importFile(key)
	if code != exitOK || len(second["existing"]) != 4 || len(second["created"]) != 0 || !reflect.DeepEqual(stored(), afterFirst) {
		t.Fatalf("second run exit %d: %+v; rows %+v", code, second, stored())
	}

	write("broken\n" +
		"@example.test alice@example.test\n" +
		"remote@example.test External@elsewhere.test\n" +
		"sales@example.test bob@example.test\n" +
		"outsider@ungranted.test alice@example.test\n" +
		"last@example.test bob@example.test\n")
	code, partial := importFile(key)
	if code != exitFailed || len(partial["failed"]) != 5 || len(partial["created"]) != 1 || partial["created"][0].Line != 6 {
		t.Fatalf("partial import exit %d: %+v", code, partial)
	}
	for index, failed := range partial["failed"] {
		if failed.Line != index+1 {
			t.Fatalf("failed line: %+v", failed)
		}
	}
	if !strings.Contains(partial["failed"][4].Reason, "HTTP 403") {
		t.Fatalf("source domain must be refused by the API: %+v", partial)
	}
	afterPartial := stored()
	if len(afterPartial) != 5 || !reflect.DeepEqual(afterPartial[:4], afterFirst) {
		t.Fatalf("failed imports changed earlier rules: %+v", afterPartial)
	}
	code, refused := importFile(server.apiKey)
	if code != exitFailed || len(refused["failed"]) != 6 || !reflect.DeepEqual(stored(), afterPartial) {
		t.Fatalf("write-only credential: exit %d %+v", code, refused)
	}
}
