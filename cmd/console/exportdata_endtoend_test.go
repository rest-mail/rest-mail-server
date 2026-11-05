package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestDataExportsThroughPublicAPI(t *testing.T) {
	source := newMigrationServer(t)
	from := source.newAccount(t, "export-from@"+migrationDomain)
	other := source.newAccount(t, "export-other@"+migrationDomain)
	ownerKey := dataExportKey(t, source, from, "export-from@"+migrationDomain, models.CapMailRead)
	otherKey := dataExportKey(t, source, other, "export-other@"+migrationDomain, models.CapMailRead)
	writeKey := dataExportKey(t, source, from, "export-from@"+migrationDomain, models.CapMailWrite)
	var contacts []map[string]string
	for index := range 103 {
		contacts = append(contacts, map[string]string{"email": fmt.Sprintf("person%03d@example.test", index), "name": fmt.Sprintf("Person %03d, Élodie; \\ notes\n%s", index, strings.Repeat("猫", 30))})
	}
	requestDataExportAPI(t, source, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/contacts/import", from), source.session,
		map[string]any{"contacts": contacts}, http.StatusOK)
	calendarMaildir := calendarImportMaildir(t, exportedICS)
	if got := source.importMaildir(t, from, calendarMaildir); got.Imported != 1 {
		t.Fatalf("seed calendar mail: %+v", got)
	}
	var message models.Message
	if err := source.db.Where("mailbox_id = ?", from).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	requestDataExportAPI(t, source, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/threads/%s/snooze", from, message.ThreadID), source.session,
		map[string]any{"snoozed_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, http.StatusOK)
	if err := source.db.Model(&message).Update("imap_deleted", true).Error; err != nil {
		t.Fatal(err)
	}
	var ownerContacts, ownerCalendar string
	for _, credential := range []struct{ name, token string }{{"owner", ownerKey}, {"administrator", source.exportKey(t)}} {
		for _, command := range []string{"export-contacts", "export-calendar"} {
			t.Run(credential.name+"/"+command, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "export")
				code, out, errs := exportDataAs(t, source, from, credential.token, command, path)
				if code != exitOK {
					t.Fatalf("export: exit %d: %s", code, errs)
				}
				if command == "export-contacts" {
					raw, err := os.ReadFile(path)
					if err != nil || strings.Count(string(raw), "BEGIN:VCARD\r\n") != len(contacts) {
						t.Fatalf("contacts: %s (%v)", out, err)
					}
					if credential.name == "owner" {
						ownerContacts = string(raw)
					} else if string(raw) != ownerContacts {
						t.Fatal("administrator export differs from owner's")
					}
				} else {
					files, err := filepath.Glob(filepath.Join(path, "*.ics"))
					if err != nil || len(files) != 1 {
						t.Fatalf("calendar: %v %v %s", files, err, out)
					}
					raw, err := os.ReadFile(files[0])
					if err != nil || string(raw) != exportedICS {
						t.Fatalf("calendar source changed: %q (%v)", raw, err)
					}
					ownerCalendar = string(raw)
				}
			})
		}
	}
	for _, credential := range []struct{ name, token string }{{"other-account", otherKey}, {"without-mail-read", writeKey}} {
		for _, command := range []string{"export-contacts", "export-calendar"} {
			t.Run(credential.name+"/"+command, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "export")
				code, _, errs := exportDataAs(t, source, from, credential.token, command, path)
				if code != exitFailed {
					t.Fatalf("export allowed: exit %d: %s", code, errs)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("refused export published output: %v", err)
				}
			})
		}
	}
	destination := newMigrationServer(t)
	to := destination.newAccount(t, "export-to@"+migrationDomain)
	if got := destination.importMaildir(t, to, calendarImportMaildir(t, ownerCalendar)); got.Imported != 1 {
		t.Fatalf("import exported calendar through existing Maildir import: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "calendar")
	code, _, errs := exportDataAs(t, destination, to, destination.exportKey(t), "export-calendar", path)
	if code != exitOK {
		t.Fatalf("re-export imported calendar: exit %d: %s", code, errs)
	}
	files, err := filepath.Glob(filepath.Join(path, "*.ics"))
	if err != nil || len(files) != 1 {
		t.Fatalf("re-exported files %v (%v)", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || string(raw) != ownerCalendar {
		t.Fatalf("calendar round trip changed source: %q (%v)", raw, err)
	}
	decoded := decodeExportedContacts(t, ownerContacts)
	if !reflect.DeepEqual(decoded, contacts) {
		t.Fatalf("vCard changed names/emails: got %+v, want %+v", decoded, contacts)
	}
	requestDataExportAPI(t, destination, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/contacts/import", to), destination.session,
		map[string]any{"contacts": decoded}, http.StatusOK)
	var restored []models.Contact
	if err := destination.db.Where("mailbox_id = ?", to).Order("email").Find(&restored).Error; err != nil {
		t.Fatal(err)
	}
	if len(restored) != len(contacts) {
		t.Fatalf("contact round trip restored %d, want %d", len(restored), len(contacts))
	}
	for index, contact := range restored {
		if contact.Email != contacts[index]["email"] || contact.Name != contacts[index]["name"] {
			t.Errorf("contact %d changed after JSON import: %+v", index, contact)
		}
	}
}

func dataExportKey(t *testing.T, server *migrationServer, holder uint, address, capability string) string {
	t.Helper()
	_, token, err := repositories.NewCredentialRepository(server.db).CreateAPIKey(holder, holder, "data-export", []auth.ScopedPermission{
		{Capability: capability, Scope: grants.Mailbox(address)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func exportDataAs(t *testing.T, server *migrationServer, account uint, token, command, path string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := runExportData(context.Background(), command, []string{"--server", server.apiURL, "--account-id", strconv.FormatUint(uint64(account), 10), "--path", path, "--json"},
		token, server.client, &out, &errs)
	if strings.Contains(out.String()+errs.String(), token) {
		t.Fatal("export printed credential")
	}
	return code, out.String(), errs.String()
}

func requestDataExportAPI(t *testing.T, server *migrationServer, method, path, token string, body any, status int) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, server.apiURL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	result, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		t.Fatalf("%s %s: HTTP %d %s (%v)", method, path, response.StatusCode, result, err)
	}
}

func calendarImportMaildir(t *testing.T, calendar string) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"cur", "new", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	raw := "From: organizer@example.test\r\nTo: invitee@example.test\r\nSubject: invitation\r\nMessage-ID: <calendar-export@example.test>\r\nDate: Wed, 4 Nov 2026 12:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/calendar; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(calendar)) + "\r\n"
	if err := os.WriteFile(filepath.Join(root, "cur", "calendar:2,S"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func decodeExportedContacts(t *testing.T, data string) []map[string]string {
	t.Helper()
	data = strings.ReplaceAll(data, "\r\n ", "")
	var contacts []map[string]string
	var contact map[string]string
	for _, line := range strings.Split(data, "\r\n") {
		key, value, _ := strings.Cut(line, ":")
		switch key {
		case "BEGIN":
			contact = map[string]string{}
		case "FN", "EMAIL":
			var decoded strings.Builder
			for index := 0; index < len(value); index++ {
				character := value[index]
				if character == '\\' {
					index++
					if index == len(value) {
						t.Fatal("truncated vCard escape")
					}
					character = value[index]
					if character == 'n' || character == 'N' {
						character = '\n'
					}
				}
				decoded.WriteByte(character)
			}
			field := "email"
			if key == "FN" {
				field = "name"
			}
			contact[field] = decoded.String()
		case "END":
			contacts = append(contacts, contact)
		}
	}
	return contacts
}
