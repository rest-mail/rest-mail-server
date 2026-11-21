package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"golang.org/x/crypto/bcrypt"
)

func TestAC05MigrationRunbookCredentialsPartialFailureAndRestart(t *testing.T) {
	s := newMigrationServer(t)
	address := "runbook@" + migrationDomain
	passwdFile := filepath.Join(t.TempDir(), "users")
	if err := os.WriteFile(passwdFile, []byte(address+":"+dovecotSHA512Hash+":1000:1000::/home/runbook::\n"), 0600); err != nil {
		t.Fatal(err)
	}
	accounts := func(token string) (int, map[string][]passwdFileResult) {
		var stdout, stderr bytes.Buffer
		code := runImportPasswdFile(context.Background(), []string{"--server", s.apiURL, "--json", passwdFile}, strings.NewReader(""), token, s.client, &stdout, &stderr)
		checkMigrationOutputSecrets(t, stdout.String()+stderr.String(), token, dovecotSHA512Hash, dovecotSHA512Password)
		var results map[string][]passwdFileResult
		if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
			t.Fatalf("passwd-file exit %d did not produce a JSON result: %v", code, err)
		}
		return code, results
	}
	t.Run("account hash requires a human administrator", func(t *testing.T) {
		code, result := accounts(s.apiKey)
		if code != exitFailed || len(result["created"]) != 0 || len(result["failed"]) != 1 || !strings.Contains(result["failed"][0].Reason, "HTTP 403") {
			t.Fatalf("passwd-file with machine key: exit %d, %+v; want HTTP 403 and no created account", code, result)
		}
		var count int64
		if err := s.db.Model(&models.Mailbox{}).Where("address = ?", address).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("machine key left %d mailboxes: %v", count, err)
		}
	})
	code, result := accounts(s.session)
	if code != exitOK || len(result["created"]) != 1 || result["created"][0].Address != address {
		t.Fatalf("passwd-file with human session: exit %d, %+v", code, result)
	}
	account := result["created"][0].ID
	credential := s.storedHash(t, address)
	if credential.SecretHash != dovecotSHA512Hash {
		t.Fatal("stored credential did not retain the original Dovecot hash")
	}
	s.mustReadInbox(t, address, dovecotSHA512Password)
	upgraded := s.storedHash(t, address)
	cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(upgraded.SecretHash, "{BLF-CRYPT}")))
	if err != nil || cost != auth.PasswordHashCost || upgraded.ID != credential.ID || !strings.HasPrefix(upgraded.SecretHash, "{BLF-CRYPT}") {
		t.Fatalf("sign-in did not upgrade the same credential to bcrypt cost %d", auth.PasswordHashCost)
	}
	code, result = accounts(s.session)
	if code != exitOK || len(result["created"]) != 0 || !reflect.DeepEqual(addressesOf(result["existing"]), []string{address}) || s.storedHash(t, address).SecretHash != upgraded.SecretHash {
		t.Fatalf("passwd-file rerun: exit %d, %+v; existing account and new hash must be retained", code, result)
	}

	root := t.TempDir()
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	const firstName = "001:2,S"
	const secondName = "002:2,F"
	secondMail := strings.Replace(migrationImportMail, "<historical@", "<second@", 1) + strings.Repeat("more mail\r\n", 400)
	date := time.Date(2020, 11, 7, 15, 1, 32, 0, time.UTC)
	for name, raw := range map[string]string{firstName: migrationImportMail, secondName: secondMail} {
		path := filepath.Join(root, "cur", name)
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, date, date); err != nil {
			t.Fatal(err)
		}
	}
	updateQuota := func(limit int64) {
		t.Helper()
		if err := s.db.Model(&models.Mailbox{}).Where("id = ?", account).Update("quota_bytes", limit).Error; err != nil {
			t.Fatal(err)
		}
	}
	updateQuota(512)
	args := []string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(account), 10), "--path", root, "--json"}
	var stdout, stderr bytes.Buffer
	code = runImportMail(context.Background(), "maildir", append(append([]string{}, args...), "--dry-run"), "", s.client, &stdout, &stderr)
	var dry maildirSummary
	if err := json.Unmarshal(stdout.Bytes(), &dry); err != nil || code != exitOK || dry != (maildirSummary{Messages: 2}) || len(s.storedMail(t, account).Messages) != 0 || len(s.importAudits(t)) != 0 {
		t.Fatalf("maildir dry-run should validate both files and not write: exit %d, result %+v, error %v", code, dry, err)
	}
	t.Run("quota refusal retains a restart point", func(t *testing.T) {
		stdout.Reset()
		stderr.Reset()
		code := runImportMail(context.Background(), "maildir", args, s.apiKey, s.client, &stdout, &stderr)
		checkMigrationOutputSecrets(t, stdout.String()+stderr.String(), s.apiKey, "private historical body")
		var failure struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(stderr.Bytes(), &failure); code != exitFailed || stdout.Len() != 0 || err != nil || failure.Error.Code != "import_failed" {
			t.Fatalf("maildir over quota: exit %d, failure %+v, JSON %v; want nonzero exit and import_failed", code, failure, err)
		}
		for _, text := range []string{secondName, "HTTP " + strconv.Itoa(http.StatusConflict), "completed 1 imports, skipped 0 existing copies; rerun to resume"} {
			if !strings.Contains(failure.Error.Message, text) {
				t.Fatalf("failure did not explain %q: %s", text, failure.Error.Message)
			}
		}
	})
	partial := s.storedMail(t, account)
	if len(partial.Messages) != 1 || partial.Counted != 1 || partial.Used == 0 || partial.Messages[0].Raw != migrationImportMail || !partial.Messages[0].IsRead || partial.Messages[0].IsFlagged || !partial.Messages[0].ReceivedAt.Equal(date) {
		t.Fatalf("maildir partial import: %d messages, %d counted, %d bytes; want only the first intact", len(partial.Messages), partial.Counted, partial.Used)
	}
	updateQuota(1 << 20)
	if got := s.importMaildir(t, account, root); got != (maildirSummary{Messages: 2, Imported: 1, Skipped: 1}) {
		t.Fatalf("maildir restart: %+v, want one import and one skip", got)
	}
	complete := s.storedMail(t, account)
	if len(complete.Messages) != 2 || complete.Counted != 2 || complete.Used <= partial.Used || !reflect.DeepEqual(complete.Messages[0], partial.Messages[0]) || complete.Messages[1].Raw != secondMail || !complete.Messages[1].IsFlagged || complete.Messages[1].IsRead || !complete.Messages[1].ReceivedAt.Equal(date) {
		t.Fatalf("maildir restart did not preserve the first row and add the second with its original flags/date")
	}
	if got := s.importMaildir(t, account, root); got != (maildirSummary{Messages: 2, Skipped: 2}) || !reflect.DeepEqual(complete, s.storedMail(t, account)) {
		t.Fatalf("maildir repeat: %+v, want two skips and identical rows and quota", got)
	}
	entries := s.importAudits(t)
	if len(entries) != 3 {
		t.Fatalf("three live runs wrote %d import audit summaries", len(entries))
	}
	for index, want := range [][3]string{{"1", "0", "1"}, {"1", "1", "0"}, {"0", "2", "0"}} {
		target := entries[index].Target
		if got := [3]string{target["imported"], target["deduplicated"], target["refused"]}; got != want || target["import_session"] == "" {
			t.Fatalf("maildir audit run %d: imported, skipped, refused = %v, want %v", index+1, got, want)
		}
	}
}

func checkMigrationOutputSecrets(t *testing.T, output string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(output, secret) {
			t.Fatal("migration command output disclosed a secret or message contents")
		}
	}
}
