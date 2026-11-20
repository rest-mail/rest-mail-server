package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/exportsession"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// #2116 AC03b: every export of account data records an attributable audit
// event, whatever it exports and in whichever format, carrying the actor, the
// account, the format, the rows and bytes written and whether it completed or
// aborted. Exporting someone else's account takes mail:export, which a grant
// to read their mail does not include. These run the restmail export commands
// against the real public router on PostgreSQL and read the audit log there.

// exportCommands are the four exports, each with the data and format its
// audit entries name.
var exportCommands = []struct{ command, data, format string }{
	{"export-maildir", "mail", "maildir"},
	{"export-settings", "settings", "json"},
	{"export-contacts", "contacts", "vcard"},
	{"export-calendar", "calendar", "icalendar"},
}

// runExport runs one of the four export commands with --json.
func (s *migrationServer) runExport(t *testing.T, command, key string, account uint, path string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"--server", s.apiURL, "--account-id", strconv.FormatUint(uint64(account), 10), "--path", path, "--json"}, extra...)
	var out, errs bytes.Buffer
	var code int
	switch command {
	case "export-maildir":
		code = runExportMaildir(context.Background(), args, key, s.client, &out, &errs)
	case "export-settings":
		code = runExportSettings(context.Background(), args, key, s.client, time.Now, &out, &errs)
	default:
		code = runExportData(context.Background(), command, args, key, s.client, &out, &errs)
	}
	return code, out.String(), errs.String()
}

// exportEntries are the account.export audit entries, oldest first.
func (s *migrationServer) exportEntries(t *testing.T) []models.AuditEntry {
	t.Helper()
	var entries []models.AuditEntry
	if err := s.db.Where("action = ?", exportsession.Action).Order("seq").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	return entries
}

// exportedBytes is the size of every regular file under path.
func exportedBytes(t *testing.T, path string) (files int, size int64) {
	t.Helper()
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			files++
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, size
}

// seedExportableAccount gives the account a message, a calendar invitation
// and two contacts, so every export has something to write.
func (s *migrationServer) seedExportableAccount(t *testing.T, id uint) {
	t.Helper()
	invitation, _ := calendarMessage("audited-meeting@example.test", 100)
	plain := "Message-ID: <audited@old.example>\r\nFrom: a@old.example\r\nSubject: audited\r\n\r\nbody\r\n"
	if got := s.importMaildir(t, id, seedMaildir(t, map[string][]string{"": {plain}, "Archive": {invitation}}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))); got.Imported != 2 {
		t.Fatalf("seed: %+v", got)
	}
	requestDataExportAPI(t, s, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/contacts/import", id), s.session,
		map[string]any{"contacts": []map[string]string{{"email": "one@example.test", "name": "One"}, {"email": "two@example.test", "name": "Two"}}}, http.StatusOK)
}

// checkExportPair checks that entries hold exactly one export of account by
// actor, through credential, as a start and an end with result, and answers
// the end.
func checkExportPair(t *testing.T, entries []models.AuditEntry, actor, account, credential uint, data, format, result string) models.AuditEntry {
	t.Helper()
	if len(entries) != 2 {
		t.Fatalf("%d account.export entries, want a start and an end: %+v", len(entries), entries)
	}
	start, end := entries[0], entries[1]
	for i, e := range entries {
		if e.ActorKind != models.AuditActorAccount || e.ActorID == nil || *e.ActorID != actor || e.ActorName == "" {
			t.Errorf("entry %d does not name the actor %d: kind %s id %v name %q", i, actor, e.ActorKind, e.ActorID, e.ActorName)
		}
		if e.CredentialID == nil || *e.CredentialID != credential {
			t.Errorf("entry %d does not name the key %d: %v", i, credential, e.CredentialID)
		}
		if e.Outcome != models.AuditOutcomeSuccess || e.Target["account_id"] != strconv.FormatUint(uint64(account), 10) ||
			e.Target["data"] != data || e.Target["format"] != format || e.Target["export_session"] == "" {
			t.Errorf("entry %d does not name the export of account %d as %s/%s: %+v", i, account, data, format, e)
		}
	}
	if start.Target["result"] != exportsession.Started || start.Target["rows"] != "" || start.Route != "/api/v1/accounts/{id}/exports" {
		t.Errorf("the first entry is not the start: %+v", start)
	}
	if end.Target["result"] != result || end.Target["export_session"] != start.Target["export_session"] ||
		end.Target["closed_by"] != "exporter" || end.Route != "/api/v1/accounts/{id}/exports/{session}/close" {
		t.Errorf("the second entry is not the %s end of the same export: %+v", result, end)
	}
	return end
}

// credentialOf is the id of the API key secret.
func (s *migrationServer) credentialOf(t *testing.T, holder uint) uint {
	t.Helper()
	var credential models.Credential
	if err := s.db.Where("account_id = ? AND kind = ?", holder, models.CredentialKindAPIKey).Order("id DESC").First(&credential).Error; err != nil {
		t.Fatal(err)
	}
	return credential.ID
}

// Each of the four formats, exported by the account itself, records its
// start and its completion, with the rows and bytes it wrote as the files on
// disk show them, and the audit chain still verifies.
func TestEveryExportFormatRecordsItsStartAndCompletion(t *testing.T) {
	s := newMigrationServer(t)
	address := "audited@" + migrationDomain
	id := s.newAccount(t, address)
	s.seedExportableAccount(t, id)
	key := dataExportKey(t, s, id, address, models.CapMailRead, models.CapMailExport)
	credential := s.credentialOf(t, id)

	wantRows := map[string]string{"export-maildir": "2", "export-settings": "1", "export-contacts": "2", "export-calendar": "1"}
	seen := 0
	for _, export := range exportCommands {
		t.Run(export.command, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "export")
			code, out, errs := s.runExport(t, export.command, key, id, path)
			if code != exitOK {
				t.Fatalf("exit %d: %s", code, errs)
			}
			entries := s.exportEntries(t)
			if len(entries) != seen+2 {
				t.Fatalf("%d new account.export entries, want 2", len(entries)-seen)
			}
			end := checkExportPair(t, entries[seen:], id, id, credential, export.data, export.format, exportsession.Completed)
			seen = len(entries)
			files, size := exportedBytes(t, path)
			if end.Target["rows"] != wantRows[export.command] || end.Target["bytes"] != strconv.FormatInt(size, 10) || files == 0 {
				t.Fatalf("the end records %s rows and %s bytes; the export wrote %d files of %d bytes, want %s rows (%s)",
					end.Target["rows"], end.Target["bytes"], files, size, wantRows[export.command], out)
			}
		})
	}
	if result, err := audit.VerifyDB(s.db); err != nil || !result.OK {
		t.Fatalf("the audit chain does not verify: %+v (%v)", result, err)
	}
}

// An export that fails records its end as aborted. A key that may export but
// not read the account fails every format after its start; export-maildir
// failing on its second folder records the messages it had written.
func TestEveryExportFormatRecordsAnAbort(t *testing.T) {
	s := newMigrationServer(t)
	address := "aborted@" + migrationDomain
	id := s.newAccount(t, address)
	s.seedExportableAccount(t, id)
	exportOnly := dataExportKey(t, s, id, address, models.CapMailExport)
	credential := s.credentialOf(t, id)

	seen := 0
	for _, export := range exportCommands {
		t.Run(export.command, func(t *testing.T) {
			code, out, errs := s.runExport(t, export.command, exportOnly, id, filepath.Join(t.TempDir(), "export"))
			if code != exitFailed || out != "" {
				t.Fatalf("an export that cannot read: exit %d, summary %q: %s", code, out, errs)
			}
			entries := s.exportEntries(t)
			end := checkExportPair(t, entries[seen:], id, id, credential, export.data, export.format, exportsession.Aborted)
			seen = len(entries)
			if end.Target["rows"] != "0" || end.Target["bytes"] != "0" {
				t.Fatalf("an export that wrote nothing records %s rows and %s bytes", end.Target["rows"], end.Target["bytes"])
			}
		})
	}

	t.Run("export-maildir part way", func(t *testing.T) {
		key := dataExportKey(t, s, id, address, models.CapMailRead, models.CapMailExport)
		path := filepath.Join(t.TempDir(), "export")
		// An older message over the limit, listed after Archive's newer
		// invitation, which is under it: whichever folder comes first, at
		// least one message is written before the export fails.
		big := "Message-ID: <too-big@old.example>\r\nFrom: a@old.example\r\nSubject: big\r\n\r\n" + strings.Repeat("0123456789abcdef\r\n", 512)
		if got := s.importMaildir(t, id, seedMaildir(t, map[string][]string{"Archive": {big}}, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))); got.Imported != 1 {
			t.Fatalf("seed: %+v", got)
		}
		code, _, errs := s.runExport(t, "export-maildir", key, id, path, "--max-message-bytes", "4096")
		if code != exitFailed {
			t.Fatalf("exit %d: %s", code, errs)
		}
		end := checkExportPair(t, s.exportEntries(t)[seen:], id, id, s.credentialOf(t, id), "mail", "maildir", exportsession.Aborted)
		files, size := exportedBytes(t, path)
		if end.Target["rows"] != strconv.Itoa(files) || end.Target["bytes"] != strconv.FormatInt(size, 10) || files == 0 {
			t.Fatalf("the abort records %s rows and %s bytes; %d files of %d bytes were written", end.Target["rows"], end.Target["bytes"], files, size)
		}
	})
}

// grantRole gives holder a role of its own holding capabilities at scope, as
// an operator would grant one.
func (s *migrationServer) grantRole(t *testing.T, holder uint, name, scope string, capabilities ...string) {
	t.Helper()
	role := models.Role{Name: name, Description: name}
	if err := s.db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	for _, capability := range capabilities {
		var c models.Capability
		if err := s.db.Where("name = ?", capability).First(&c).Error; err != nil {
			t.Fatalf("capability %s: %v", capability, err)
		}
		if err := s.db.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: c.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: holder, RoleID: role.ID, Scope: scope,
		Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
}

// An account's owner exports it by default. An administrator of its domain
// who also holds a grant to read its mail may read it, but may not export it:
// every format is refused before anything is written, prints no summary and
// leaves no export entry. Given a role that names mail:export, the same
// administrator's export goes through, recorded as that administrator's act
// on that account.
func TestExportOfAnotherAccountNeedsItsOwnGrant(t *testing.T) {
	s := newMigrationServer(t)
	target := "exported@" + migrationDomain
	id := s.newAccount(t, target)
	s.seedExportableAccount(t, id)
	var owner models.Mailbox
	if err := s.db.Where("address = ?", "migrator@"+migrationDomain).First(&owner).Error; err != nil {
		t.Fatal(err)
	}

	// The administrator: the admin role at the domain, as migrator holds it,
	// and a role to read the domain's mail. A mailbox capability reaches only
	// where mailbox:session is held too (grants.withinSessions), so the
	// reader role holds both. Its key asks for both read and export; the key
	// cannot hold more than its account.
	adminAddress := "second-admin@" + migrationDomain
	admin := s.newAccount(t, adminAddress)
	var adminRole models.Role
	if err := s.db.Where("name = ?", "admin").First(&adminRole).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: admin, RoleID: adminRole.ID,
		Scope: grants.Domain(migrationDomain), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	s.grantRole(t, admin, "mail-reader", grants.Domain(migrationDomain), models.CapMailRead, models.CapMailboxSession)
	_, adminKey, err := repositories.NewCredentialRepository(s.db).CreateAPIKey(admin, admin, "export", []auth.ScopedPermission{
		{Capability: models.CapMailRead, Scope: grants.Domain(migrationDomain)},
		{Capability: models.CapMailExport, Scope: grants.Domain(migrationDomain)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	adminCredential := s.credentialOf(t, admin)

	// The grant to read is real: the administrator reads the account's mail.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/accounts/%d/folders/INBOX/messages", s.apiURL, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	listing, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(listing), "audited") {
		t.Fatalf("the administrator cannot read the account's mail: HTTP %d %s", resp.StatusCode, listing)
	}

	for _, export := range exportCommands {
		t.Run("administrator without mail:export/"+export.command, func(t *testing.T) {
			dir := t.TempDir()
			code, out, errs := s.runExport(t, export.command, adminKey, id, filepath.Join(dir, "export"))
			if code != exitFailed || out != "" || !strings.Contains(errs, "mail:export") {
				t.Fatalf("exit %d, summary %q: %s", code, out, errs)
			}
			if files, _ := exportedBytes(t, dir); files != 0 {
				t.Fatalf("a refused export wrote %d files", files)
			}
			if entries := s.exportEntries(t); len(entries) != 0 {
				t.Fatalf("a refused export left account.export entries: %+v", entries)
			}
		})
	}

	ownerCredential := uint(0)
	ownerKey := s.exportKey(t)
	ownerCredential = s.credentialOf(t, owner.ID)
	t.Run("owner", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "export")
		if code, _, errs := s.runExport(t, "export-maildir", ownerKey, id, path); code != exitOK {
			t.Fatalf("the owner's export: exit %d: %s", code, errs)
		}
		checkExportPair(t, s.exportEntries(t), owner.ID, id, ownerCredential, "mail", "maildir", exportsession.Completed)
	})

	s.grantRole(t, admin, "exporter", grants.Mailbox(target), models.CapMailExport)
	for _, export := range exportCommands {
		t.Run("administrator with mail:export/"+export.command, func(t *testing.T) {
			before := len(s.exportEntries(t))
			path := filepath.Join(t.TempDir(), "export")
			if code, _, errs := s.runExport(t, export.command, adminKey, id, path); code != exitOK {
				t.Fatalf("exit %d: %s", code, errs)
			}
			end := checkExportPair(t, s.exportEntries(t)[before:], admin, id, adminCredential, export.data, export.format, exportsession.Completed)
			if _, size := exportedBytes(t, path); end.Target["bytes"] != strconv.FormatInt(size, 10) {
				t.Fatalf("the end records %s bytes; %d were written", end.Target["bytes"], size)
			}
		})
	}
}

// exportSessionCall sends one export-session request with key.
func (s *migrationServer) exportSessionCall(t *testing.T, key, path string, body any) (int, map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, s.apiURL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var answer map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&answer)
	return resp.StatusCode, answer
}

// An export closes once, by the account that opened it, with a result it
// names; a data in a format of another is refused before anything is recorded;
// and one its exporter never closes is closed by the sweeper as aborted,
// recorded with who opened it and no rows or bytes, since none were reported.
func TestExportSessionsCloseOnceAndAbandonedOnesAbort(t *testing.T) {
	s := newMigrationServer(t)
	address := "session@" + migrationDomain
	id := s.newAccount(t, address)
	key := dataExportKey(t, s, id, address, models.CapMailRead, models.CapMailExport)
	otherAddress := "other-session@" + migrationDomain
	other := s.newAccount(t, otherAddress)
	otherKey := dataExportKey(t, s, other, otherAddress, models.CapMailRead, models.CapMailExport)
	exports := fmt.Sprintf("/api/v1/accounts/%d/exports", id)

	if status, answer := s.exportSessionCall(t, key, exports, map[string]string{"data": "mail", "format": "json"}); status != http.StatusUnprocessableEntity {
		t.Fatalf("mail as json: HTTP %d %v", status, answer)
	}
	if entries := s.exportEntries(t); len(entries) != 0 {
		t.Fatalf("a refused open was recorded: %+v", entries)
	}

	status, answer := s.exportSessionCall(t, key, exports, map[string]string{"data": "contacts", "format": "vcard"})
	if status != http.StatusCreated {
		t.Fatalf("open: HTTP %d %v", status, answer)
	}
	session, _ := answer["data"].(map[string]any)["id"].(string)
	closePath := exports + "/" + session + "/close"
	if status, answer := s.exportSessionCall(t, key, closePath, map[string]any{"result": "finished", "rows": 1, "bytes": 1}); status != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown result: HTTP %d %v", status, answer)
	}
	if status, answer := s.exportSessionCall(t, key, closePath, map[string]any{"result": "completed", "rows": -1, "bytes": 1}); status != http.StatusUnprocessableEntity {
		t.Fatalf("negative rows: HTTP %d %v", status, answer)
	}
	for _, folder := range []string{"", strings.Repeat("x", 256)} {
		if status, answer := s.exportSessionCall(t, key, closePath, map[string]any{"result": "completed", "rows": 1, "bytes": 1, "skipped_folders": []string{folder}}); status != http.StatusUnprocessableEntity {
			t.Fatalf("a skipped folder of %d bytes: HTTP %d %v", len(folder), status, answer)
		}
	}
	otherPath := fmt.Sprintf("/api/v1/accounts/%d/exports/%s/close", other, session)
	if status, answer := s.exportSessionCall(t, otherKey, otherPath, map[string]any{"result": "completed", "rows": 1, "bytes": 1}); status != http.StatusNotFound {
		t.Fatalf("closed by another account: HTTP %d %v", status, answer)
	}
	if status, answer := s.exportSessionCall(t, key, closePath, map[string]any{"result": "completed", "rows": 3, "bytes": 99}); status != http.StatusOK {
		t.Fatalf("close: HTTP %d %v", status, answer)
	}
	if status, answer := s.exportSessionCall(t, key, closePath, map[string]any{"result": "aborted", "rows": 0, "bytes": 0}); status != http.StatusNotFound {
		t.Fatalf("closed twice: HTTP %d %v", status, answer)
	}
	end := checkExportPair(t, s.exportEntries(t), id, id, s.credentialOf(t, id), "contacts", "vcard", exportsession.Completed)
	if end.Target["rows"] != "3" || end.Target["bytes"] != "99" {
		t.Fatalf("the end records %+v", end.Target)
	}

	status, answer = s.exportSessionCall(t, key, exports, map[string]string{"data": "mail", "format": "maildir"})
	if status != http.StatusCreated {
		t.Fatalf("open: HTTP %d %v", status, answer)
	}
	abandoned, _ := answer["data"].(map[string]any)["id"].(string)
	if closed, err := exportsession.CloseAbandoned(s.db, time.Now()); err != nil || closed != 0 {
		t.Fatalf("a new export was swept: %d (%v)", closed, err)
	}
	if closed, err := exportsession.CloseAbandoned(s.db, time.Now().Add(exportsession.Timeout+time.Minute)); err != nil || closed != 1 {
		t.Fatalf("the abandoned export was not swept: %d (%v)", closed, err)
	}
	entries := s.exportEntries(t)
	last := entries[len(entries)-1]
	if last.Target["export_session"] != abandoned || last.Target["result"] != exportsession.Aborted || last.Target["closed_by"] != "timeout" ||
		last.ActorID == nil || *last.ActorID != id || last.Target["rows"] != "" || last.Target["bytes"] != "" {
		t.Fatalf("the sweep recorded %+v", last)
	}
	var left int64
	s.db.Model(&models.ExportSession{}).Count(&left)
	if left != 0 {
		t.Fatalf("%d export sessions left open", left)
	}
	if result, err := audit.VerifyDB(s.db); err != nil || !result.OK {
		t.Fatalf("the audit chain does not verify: %+v (%v)", result, err)
	}
}
