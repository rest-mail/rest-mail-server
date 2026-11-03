package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const settingsTestKey = "rm_settings-test-key-of-no-value"

// settingsSecrets are values a server could hold next to an account's
// settings. The fake API puts each in a response; none may reach the file.
var settingsSecrets = []string{
	"$2a$12$password-hash-of-no-value",
	"app-password-secret-of-no-value",
	"rm_api-key-secret-of-no-value",
	"JBSWY3DPEHPK3PXP",
	"session-token-of-no-value",
	settingsTestKey,
}

// settingsAPI answers the routes export-settings reads, each with fields the
// export must leave out next to the ones it keeps.
func settingsAPI(t *testing.T, requests *[]string) *httptest.Server {
	t.Helper()
	routes := map[string]string{
		"/api/v1/accounts/12": `{"data":{"id":12,"address":"ada@example.com","display_name":"Ada Lovelace","is_primary":true,
			"password_hash":"$2a$12$password-hash-of-no-value","totp_secret":"JBSWY3DPEHPK3PXP"}}`,
		"/api/v1/accounts/12/folders": `{"data":[{"name":"Archive/2020","total":3,"unread":1},{"name":"INBOX","total":9,"unread":2},
			{"name":"Sent","total":0,"unread":0}]}`,
		"/api/v1/accounts/12/sieve": `{"data":{"id":4,"mailbox_id":12,"script":"require \"fileinto\";\nif header :contains \"subject\" \"invoice\" { fileinto \"Archive/2020\"; }\n",
			"script_hash":"abc","active":true,"app_password":"app-password-secret-of-no-value"}}`,
		"/api/v1/accounts/12/vacation": `{"data":{"enabled":true,"subject":"Away","body":"Back on Monday.",
			"start_date":"2026-12-20T09:00:00+01:00","end_date":"2026-12-28T00:00:00Z","api_key":"rm_api-key-secret-of-no-value"}}`,
		"/api/v1/accounts/12/quiet-hours":        `{"data":{"enabled":true,"start":"22:00","end":"07:30","time_zone":"Europe/Berlin","session":"session-token-of-no-value"}}`,
		"/api/v1/accounts/12/contact-reply-time": `{"data":{"enabled":true}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+settingsTestKey {
			http.Error(w, `{"error":{"code":"unauthorized","message":"no"}}`, http.StatusUnauthorized)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

var settingsTestTime = func() time.Time { return time.Date(2026, 11, 3, 15, 4, 5, 0, time.FixedZone("CET", 3600)) }

// wantSettingsFile is the whole file for the fake API's account: the format is
// pinned byte for byte, so a change to it is a deliberate one.
const wantSettingsFile = `{
  "format": "restmail.settings.v1",
  "exported_at": "2026-11-03T14:04:05Z",
  "account": {
    "id": 12,
    "address": "ada@example.com",
    "display_name": "Ada Lovelace"
  },
  "folders": [
    {
      "name": "Archive/2020"
    },
    {
      "name": "INBOX"
    },
    {
      "name": "Sent"
    }
  ],
  "sieve": {
    "script": "require \"fileinto\";\nif header :contains \"subject\" \"invoice\" { fileinto \"Archive/2020\"; }\n",
    "active": true
  },
  "vacation": {
    "enabled": true,
    "subject": "Away",
    "body": "Back on Monday.",
    "start_date": "2026-12-20T08:00:00Z",
    "end_date": "2026-12-28T00:00:00Z"
  },
  "quiet_hours": {
    "enabled": true,
    "start": "22:00",
    "end": "07:30",
    "time_zone": "Europe/Berlin"
  },
  "contact_reply_time": {
    "enabled": true
  }
}
`

func TestExportSettingsWritesEverySettingAndNoSecret(t *testing.T) {
	var requests []string
	srv := settingsAPI(t, &requests)
	path := filepath.Join(t.TempDir(), "settings.json")
	var out, errs bytes.Buffer
	code := runExportSettings(context.Background(), []string{"--server", srv.URL, "--account-id", "12", "--path", path, "--json"},
		settingsTestKey, srv.Client(), settingsTestTime, &out, &errs)
	if code != exitOK || errs.Len() != 0 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	if got, want := out.String(), `{"folders":3,"format":"restmail.settings.v1","path":"`+path+`"}`+"\n"; got != want {
		t.Fatalf("summary %q, want %q", got, want)
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(file) != wantSettingsFile {
		t.Fatalf("the file is\n%s\nwant\n%s", file, wantSettingsFile)
	}
	for _, secret := range settingsSecrets {
		if strings.Contains(string(file)+out.String()+errs.String(), secret) {
			t.Errorf("the export holds secret %q", secret)
		}
	}
	for _, field := range []string{"password_hash", "totp_secret", "app_password", "api_key", "session", "script_hash", "mailbox_id", "total", "unread"} {
		if strings.Contains(string(file), `"`+field+`"`) {
			t.Errorf("the export holds field %q, which it does not name", field)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode is %v (%v), want 0600", info.Mode(), err)
	}
	for _, request := range requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("export-settings sent %s; it only reads", request)
		}
	}
	if len(requests) != 6 {
		t.Fatalf("requests %v, want one per settings route", requests)
	}
}

func TestExportSettingsRefusesAnExistingFileUnlessOverwrite(t *testing.T) {
	var requests []string
	srv := settingsAPI(t, &requests)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--server", srv.URL, "--account-id", "12", "--path", path}
	var out, errs bytes.Buffer
	if code := runExportSettings(context.Background(), args, settingsTestKey, srv.Client(), settingsTestTime, &out, &errs); code != exitUsage || len(requests) != 0 {
		t.Fatalf("exit %d after %v, want a usage refusal before any request", code, requests)
	}
	if kept, _ := os.ReadFile(path); string(kept) != "keep" {
		t.Fatalf("the existing file was changed: %q", kept)
	}
	errs.Reset()
	if code := runExportSettings(context.Background(), append(args, "--overwrite"), settingsTestKey, srv.Client(), settingsTestTime, &out, &errs); code != exitOK {
		t.Fatalf("--overwrite: exit %d: %s", code, &errs)
	}
	if got, _ := os.ReadFile(path); string(got) != wantSettingsFile {
		t.Fatalf("--overwrite wrote\n%s", got)
	}
}

func TestExportSettingsWritesNothingWhenARouteIsRefused(t *testing.T) {
	var requests []string
	srv := settingsAPI(t, &requests)
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	var out, errs bytes.Buffer
	code := runExportSettings(context.Background(), []string{"--server", srv.URL, "--account-id", "13", "--path", path, "--json"},
		settingsTestKey, srv.Client(), settingsTestTime, &out, &errs)
	if code != exitFailed || out.Len() != 0 || !strings.Contains(errs.String(), "HTTP 404") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, &out, &errs)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused export left %v", entries)
	}
	if strings.Contains(errs.String(), settingsTestKey) {
		t.Fatal("the error printed the API key")
	}
}

func TestExportSettingsNeedsAKeyAndAnAccount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		token string
	}{
		{"no key", []string{"--server", "https://mail.example.com", "--account-id", "12", "--path", "x.json"}, ""},
		{"no account", []string{"--server", "https://mail.example.com", "--path", "x.json"}, settingsTestKey},
		{"no path", []string{"--server", "https://mail.example.com", "--account-id", "12"}, settingsTestKey},
		{"plain http", []string{"--server", "http://mail.example.com", "--account-id", "12", "--path", "x.json"}, settingsTestKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errs bytes.Buffer
			if code := runExportSettings(context.Background(), tc.args, tc.token, http.DefaultClient, settingsTestTime, &out, &errs); code != exitUsage {
				t.Fatalf("exit %d: %s", code, &errs)
			}
		})
	}
}
