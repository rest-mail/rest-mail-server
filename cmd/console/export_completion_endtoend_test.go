package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

// These run the exports against the real public router on PostgreSQL (#2116
// AC03), for what a fake API cannot show: the answers the server really gives
// an account that never set anything, and its real message cursor.

func (s *migrationServer) exportSettings(t *testing.T, key string, account uint, path string, extra ...string) (int, settingsExport, string) {
	t.Helper()
	args := append([]string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(account), 10), "--path", path, "--json"}, extra...)
	var out, errs bytes.Buffer
	code := runExportSettings(context.Background(), args, key, s.client, time.Now, &out, &errs)
	var got settingsExport
	if code == exitOK {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, raw)
		}
	}
	return code, got, errs.String()
}

func TestExportSettingsFollowsAnAccountsSieveScriptOnTheRealRouter(t *testing.T) {
	s := newMigrationServer(t)
	address := "blank@" + migrationDomain
	id := s.newAccount(t, address)
	var stored int64
	if err := s.db.Model(&models.SieveScript{}).Where("mailbox_id = ?", id).Count(&stored).Error; err != nil || stored != 0 {
		t.Fatalf("a new account holds %d Sieve scripts (%v)", stored, err)
	}
	key := s.exportKey(t)
	path := filepath.Join(t.TempDir(), "settings.json")

	code, got, errs := s.exportSettings(t, key, id, path)
	if code != exitOK {
		t.Fatalf("export of an account that set nothing: exit %d: %s", code, errs)
	}
	if got.Account.Address != address || got.Sieve != (settingsSieve{}) || got.Vacation.Enabled || got.QuietHours.Enabled {
		t.Fatalf("export of an account that set nothing: %+v", got)
	}
	if !slices.Contains(got.Folders, settingsFolder{Name: "INBOX"}) {
		t.Errorf("folders %v lack INBOX", got.Folders)
	}

	route := "/api/v1/accounts/" + strconv.FormatUint(uint64(id), 10) + "/sieve"
	const script = "keep;\n"
	s.putSettings(t, http.MethodPut, route, `{"script":"keep;\n","active":true}`)
	if code, got, errs = s.exportSettings(t, key, id, path, "--overwrite"); code != exitOK || got.Sieve != (settingsSieve{Script: script, Active: true}) {
		t.Fatalf("export with a script: exit %d, %+v: %s", code, got.Sieve, errs)
	}
	s.putSettings(t, http.MethodDelete, route, "")
	if code, got, errs = s.exportSettings(t, key, id, path, "--overwrite"); code != exitOK || got.Sieve != (settingsSieve{}) {
		t.Fatalf("export after deleting the script: exit %d, %+v: %s", code, got.Sieve, errs)
	}
}

func TestExportSettingsRefusesAnotherAccountOnTheRealRouter(t *testing.T) {
	s := newMigrationServer(t)
	target := "private@" + migrationDomain
	id := s.newAccount(t, target)
	other := "other@" + migrationDomain
	otherID := s.newAccount(t, other)
	_, key, err := repositories.NewCredentialRepository(s.db).CreateAPIKey(otherID, otherID, "own", []auth.ScopedPermission{
		{Capability: models.CapMailRead, Scope: grants.Mailbox(other)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	code, _, errs := s.exportSettings(t, key, id, filepath.Join(dir, "settings.json"))
	if code != exitFailed || strings.Contains(errs, target) {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused export left %v", entries)
	}
}

// The folder holds more than two of the listing's 100-message pages, all with
// one received date, so only the id in the real cursor tells them apart.
func TestExportMaildirReadsEveryPageOfTheRealListing(t *testing.T) {
	const count = 205
	s := newMigrationServer(t)
	id := s.newAccount(t, "many@"+migrationDomain)
	source := t.TempDir()
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.Mkdir(filepath.Join(source, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	date := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	for i := range count {
		raw := fmt.Sprintf("Message-ID: <page-%d@old.example>\r\nFrom: a@old.example\r\nSubject: %d\r\n\r\nbody %d\r\n", i, i, i)
		path := filepath.Join(source, "cur", fmt.Sprintf("17000%05d.M%dP1.mail:2,S", i, i))
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, date, date); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.importMaildir(t, id, source); got.Imported != count {
		t.Fatalf("seed the account: %+v", got)
	}

	export := filepath.Join(t.TempDir(), "export")
	var out, errs bytes.Buffer
	code := runExportMaildir(context.Background(), []string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(id), 10), "--path", export, "--json"},
		s.exportKey(t), s.client, &out, &errs)
	if code != exitOK {
		t.Fatalf("export-maildir: exit %d: %s", code, &errs)
	}
	var summary struct{ Messages int }
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary.Messages != count {
		t.Fatalf("summary %s (%v), want %d messages", &out, err, count)
	}

	var want []string
	for _, message := range s.keptMail(t, id) {
		want = append(want, message.Raw)
	}
	files, err := os.ReadDir(filepath.Join(export, "cur"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(export, "cur", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(raw))
	}
	slices.Sort(want)
	slices.Sort(got)
	if len(want) != count || !slices.Equal(got, want) {
		t.Fatalf("exported %d messages, the account holds %d, and they differ or are not all %d", len(got), len(want), count)
	}
}
