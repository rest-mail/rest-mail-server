package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// putSettings sends one settings write to the real public router as the
// migration administrator, as the app would.
func (s *migrationServer) putSettings(t *testing.T, method, path, body string) {
	t.Helper()
	req, err := http.NewRequest(method, s.apiURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.session)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		var refusal bytes.Buffer
		_, _ = refusal.ReadFrom(resp.Body)
		t.Fatalf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, &refusal)
	}
}

// An account's settings, written through the public API, come back out of
// export-settings from the real router on PostgreSQL, with none of the
// account's credentials.
func TestExportSettingsReadsTheRealRouter(t *testing.T) {
	s := newMigrationServer(t)
	address := "settings@" + migrationDomain
	code, outcome := s.importPasswdFile(t, s.session, address+":"+dovecotSHA512Hash+"\n")
	if code != exitOK || len(outcome["created"]) != 1 {
		t.Fatalf("create %s: exit %d, %+v", address, code, outcome)
	}
	id := outcome["created"][0].ID
	account := "/api/v1/accounts/" + strconv.FormatUint(uint64(id), 10)
	script := "require \"fileinto\";\nif header :contains \"subject\" \"invoice\" { fileinto \"Archive\"; }\n"
	scriptBody, _ := json.Marshal(map[string]any{"script": script, "active": true})
	s.putSettings(t, http.MethodPut, account+"/sieve", string(scriptBody))
	s.putSettings(t, http.MethodPut, account+"/vacation", `{"enabled":true,"subject":"Away","body":"Back on Monday.","start_date":"2026-12-20T08:00:00Z","end_date":"2026-12-28T00:00:00Z"}`)
	s.putSettings(t, http.MethodPut, account+"/quiet-hours", `{"enabled":true,"start":"22:00","end":"07:30","time_zone":"Europe/Berlin"}`)
	s.putSettings(t, http.MethodPut, account+"/contact-reply-time", `{"enabled":true}`)

	// The account's own key is a credential it holds; it must not be exported.
	_, accountKey, err := repositories.NewCredentialRepository(s.db).CreateAPIKey(id, id, "settings", []auth.ScopedPermission{
		{Capability: models.CapMailRead, Scope: grants.Mailbox(address)},
	}, nil)
	if err != nil {
		t.Fatalf("create the account's key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	var out, errs bytes.Buffer
	at := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	if code := runExportSettings(context.Background(), []string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(id), 10), "--path", path, "--json"},
		accountKey, s.client, func() time.Time { return at }, &out, &errs); code != exitOK {
		t.Fatalf("export-settings: exit %d: %s", code, &errs)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got settingsExport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if got.Format != settingsFormat || got.ExportedAt != "2026-11-03T14:00:00Z" || got.Account.ID != id || got.Account.Address != address {
		t.Fatalf("header %+v %+v", got.Format+" "+got.ExportedAt, got.Account)
	}
	folders := map[string]bool{}
	for _, f := range got.Folders {
		folders[f.Name] = true
	}
	for _, want := range []string{"INBOX", "Sent", "Drafts", "Trash"} {
		if !folders[want] {
			t.Errorf("folders %v lack %s", got.Folders, want)
		}
	}
	if got.Sieve != (settingsSieve{Script: script, Active: true}) {
		t.Errorf("sieve %+v", got.Sieve)
	}
	if v := got.Vacation; !v.Enabled || v.Subject != "Away" || v.Body != "Back on Monday." || v.StartDate == nil || !v.StartDate.Equal(time.Date(2026, 12, 20, 8, 0, 0, 0, time.UTC)) ||
		v.EndDate == nil || !v.EndDate.Equal(time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("vacation %+v", v)
	}
	if got.QuietHours != (settingsQuietHours{Enabled: true, Start: "22:00", End: "07:30", TimeZone: "Europe/Berlin"}) {
		t.Errorf("quiet hours %+v", got.QuietHours)
	}
	if !got.ContactReplyTime.Enabled {
		t.Errorf("contact reply time %+v", got.ContactReplyTime)
	}
	stored := s.storedHash(t, address)
	for name, secret := range map[string]string{"the password hash": stored.SecretHash, "the account's API key": accountKey, "the session": s.session} {
		if secret == "" || strings.Contains(string(raw)+out.String()+errs.String(), secret) {
			t.Errorf("the export holds %s", name)
		}
	}
}
