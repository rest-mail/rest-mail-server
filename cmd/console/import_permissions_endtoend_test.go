package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/rest-mail/rest-mail-server/internal/api"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/importsession"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

const migrationImportMail = "From: source@example.net\r\nSubject: historical message\r\nMessage-ID: <historical@example.net>\r\n\r\nprivate historical body\r\n"

func TestAC02IMAPImportFlagsPermissionsIdleAndAudit(t *testing.T) {
	t.Run("APPEND and COPY preserve storage and notify IDLE without push", func(t *testing.T) {
		push := make(importPushProbe, 4)
		var internal http.Handler
		s := newMigrationServerWithRouter(t, func(routers *api.Routers) {
			routers.Messages.SetNewMailNotifier(push)
			internal = routers.Internal
		})
		address := "history@" + migrationDomain
		account, password := s.importAppPassword(t, address)
		writer, err := s.signIn(t, address, password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.ID(&imap.IDData{Name: "cutover-proof", Version: "1"}).Wait(); err != nil {
			t.Fatal(err)
		}
		if err := writer.Create("Archive", nil).Wait(); err != nil {
			t.Fatal(err)
		}
		inboxEvents, stopInbox := s.idleImportFolder(t, address, password, "INBOX")
		archiveEvents, stopArchive := s.idleImportFolder(t, address, password, "Archive")
		date := time.Date(2020, 11, 7, 15, 1, 32, 0, time.UTC)
		flags := []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft, imap.FlagDeleted, "$Forwarded", "$Junk"}
		first, err := appendMigrationMessage(writer, "INBOX", migrationImportMail, &imap.AppendOptions{Flags: flags, Time: date})
		if err != nil || first.UID == 0 {
			t.Fatalf("APPEND flagged message: %+v, %v", first, err)
		}
		waitImportExists(t, inboxEvents, 1)
		second, err := appendMigrationMessage(writer, "INBOX", migrationImportMail, &imap.AppendOptions{Time: date})
		if err != nil || second.UID == 0 || second.UID == first.UID {
			t.Fatalf("repeat APPEND should store a new unread message: %+v, %v", second, err)
		}
		waitImportExists(t, inboxEvents, 2)
		if _, err := writer.Select("INBOX", nil).Wait(); err != nil {
			t.Fatal(err)
		}
		copied, err := writer.Copy(imap.UIDSetNum(first.UID, second.UID), "Archive").Wait()
		if err != nil {
			t.Fatalf("UID COPY: %v", err)
		}
		copiedIDs, finite := copied.DestUIDs.Nums()
		if !finite || len(copiedIDs) != 2 || copiedIDs[0] == first.UID || copiedIDs[1] == second.UID {
			t.Fatalf("COPYUID: %+v", copied)
		}
		waitImportExists(t, archiveEvents, 2)
		stopInbox()
		stopArchive()
		s.checkImportedIMAPRow(t, account, first.UID, "INBOX", date, flags)
		s.checkImportedIMAPRow(t, account, second.UID, "INBOX", date, nil)
		s.checkImportedIMAPRow(t, account, copiedIDs[0], "Archive", date, flags)
		s.checkImportedIMAPRow(t, account, copiedIDs[1], "Archive", date, nil)
		got := make(map[string]int)
		for _, row := range s.storedMail(t, account).Messages {
			if row.Raw == migrationImportMail {
				got[row.Folder]++
			}
		}
		if !reflect.DeepEqual(got, map[string]int{"INBOX": 2, "Archive": 2}) {
			t.Fatalf("folders after APPEND and COPY: %v", got)
		}
		if got := s.importAudits(t); len(got) != 0 {
			t.Fatalf("APPEND wrote %d import audit rows before LOGOUT, want none", len(got))
		}
		if err := writer.Logout().Wait(); err != nil {
			t.Fatal(err)
		}
		audits := s.importAudits(t)
		for deadline := time.Now().Add(10 * time.Second); len(audits) == 0 && time.Now().Before(deadline); audits = s.importAudits(t) {
			time.Sleep(20 * time.Millisecond)
		}
		if len(audits) != 1 {
			t.Fatalf("two APPENDs wrote %d import audit rows after LOGOUT, want one", len(audits))
		}
		entry := audits[0]
		if entry.ActorID == nil || *entry.ActorID != account || entry.CredentialID == nil || entry.Target["import_session"] == "" || entry.Target["closed_by"] != "closed" || entry.Target["client_name"] != "cutover-proof" || entry.Target["client_version"] != "1" {
			t.Fatalf("APPEND audit attribution: %+v", entry)
		}
		var folders map[string]apitypes.ImportFolderCounts
		wantCounts := map[string]apitypes.ImportFolderCounts{"INBOX": {Imported: 2, Bytes: int64(2 * len(migrationImportMail))}}
		if err := json.Unmarshal([]byte(entry.Target["folders"]), &folders); err != nil || !reflect.DeepEqual(folders, wantCounts) {
			t.Fatalf("APPEND audit folder totals: %v, %v; want %v", folders, err, wantCounts)
		}
		var transfers []models.AuditEntry
		if err := s.db.Where("action = ?", "messages.transfer").Find(&transfers).Error; err != nil {
			t.Fatal(err)
		}
		if len(transfers) != 1 || transfers[0].Target["folder"] != "Archive" || transfers[0].Target["source_folder"] != "INBOX" || transfers[0].Target["move"] != "false" || transfers[0].Target["messages"] != "2" {
			t.Fatalf("two messages in one COPY should produce one transfer audit: %+v", transfers)
		}
		select {
		case id := <-push:
			t.Fatalf("historical APPEND/COPY fired phone notifier for %d", id)
		default:
		}
		const sender = "live@example.invalid"
		raw := "From: " + sender + "\r\nTo: " + address + "\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) +
			"\r\nSubject: live mail\r\nMessage-ID: <live@example.invalid>\r\n\r\nlive\r\n"
		delivery, err := json.Marshal(apitypes.DeliverMessageBody{Address: address, Sender: sender, RawMessage: []byte(raw)})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(delivery))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		asIMAPGateway(internal).ServeHTTP(response, req)
		if response.Code != http.StatusCreated {
			t.Fatalf("push positive control delivery: HTTP %d %s", response.Code, response.Body.String())
		}
		select {
		case id := <-push:
			if id != account {
				t.Fatalf("live delivery notified %d, want %d", id, account)
			}
		default:
			t.Fatal("live unread inbox delivery did not call the same phone notifier")
		}
	})

	t.Run("already connected app password loses APPEND but keeps COPY", func(t *testing.T) {
		s := newMigrationServer(t)
		address := "revoked@" + migrationDomain
		account, password := s.importAppPassword(t, address)
		client, err := s.signIn(t, address, password)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Create("Archive", nil).Wait(); err != nil {
			t.Fatal(err)
		}
		seed, err := appendMigrationMessage(client, "INBOX", migrationImportMail, nil)
		if err != nil || seed.UID == 0 {
			t.Fatalf("APPEND with import grant: %+v, %v", seed, err)
		}
		var role models.Role
		if err := s.db.Where("name = ?", models.RoleImporter).Take(&role).Error; err != nil {
			t.Fatal(err)
		}
		removed := s.db.Where("holder_kind = ? AND holder_id = ? AND role_id = ? AND scope = ?", models.GrantHolderAccount, account, role.ID, grants.Mailbox(address)).Delete(&models.Grant{})
		if removed.Error != nil || removed.RowsAffected != 1 {
			t.Fatalf("revoke import grant: %d rows, %v", removed.RowsAffected, removed.Error)
		}
		before := s.storedMail(t, account)
		result, err := appendMigrationMessage(client, "INBOX", migrationImportMail, nil)
		var refused *imap.Error
		if !errors.As(err, &refused) || refused.Type != imap.StatusResponseTypeNo {
			t.Fatalf("APPEND after revoking live import grant: %+v, %v; want tagged NO", result, err)
		}
		if after := s.storedMail(t, account); !reflect.DeepEqual(after, before) {
			t.Fatal("refused APPEND changed messages or quota")
		}
		if _, err := client.Select("INBOX", nil).Wait(); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Copy(imap.UIDSetNum(seed.UID), "Archive").Wait(); err != nil {
			t.Fatalf("COPY with mail:write but without mail:import: %v", err)
		}
		if err := client.Logout().Wait(); err != nil {
			t.Fatal(err)
		}
		if closed, err := importsession.CloseIdle(s.db, time.Now().Add(2*importsession.IdleTimeout)); err != nil || closed != 1 {
			t.Fatalf("sweep revoked connection's import session: closed %d, %v", closed, err)
		}
		entries := s.importAudits(t)
		if len(entries) != 1 || entries[0].ActorID == nil || *entries[0].ActorID != account || entries[0].CredentialID == nil || entries[0].Target["closed_by"] != "idle" || entries[0].Target["imported"] != "1" || entries[0].Target["refused"] != "1" {
			t.Fatalf("revoked app password's import summary: %+v", entries)
		}
	})

	t.Run("scoped key can read but cannot import", func(t *testing.T) {
		s := newMigrationServer(t)
		address := "scope@" + migrationDomain
		account := s.newAccount(t, address)
		credential, key, err := repositories.NewCredentialRepository(s.db).CreateAPIKey(account, account, "reader", []auth.ScopedPermission{
			{Capability: models.CapMailRead, Scope: grants.Mailbox(address)},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		base := fmt.Sprintf("/api/v1/accounts/%d/messages", account)
		if status, body := s.importProofRequest(t, http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", account), key, nil); status != http.StatusOK {
			t.Fatalf("read-only key control: HTTP %d %s", status, body)
		}
		before := s.storedMail(t, account)
		body, err := json.Marshal(apitypes.ImportMessageBody{Folder: "INBOX", RawMessage: []byte(migrationImportMail), InternalDate: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if status, response := s.importProofRequest(t, http.MethodPost, base+"/import", key, body); status != http.StatusForbidden {
			t.Fatalf("read-only key import: HTTP %d %s; want 403", status, response)
		}
		if after := s.storedMail(t, account); !reflect.DeepEqual(after, before) {
			t.Fatal("refused scoped key changed messages or quota")
		}
		entry := s.requireDeniedImportAudit(t, account)
		if entry.CredentialID == nil || *entry.CredentialID != credential.ID {
			t.Fatalf("refused key lacks audit attribution: %+v", entry)
		}
	})
}

type importPushProbe chan uint

func (probe importPushProbe) NewMail(account uint) {
	select {
	case probe <- account:
	default:
	}
}

func (s *migrationServer) importAppPassword(t *testing.T, address string) (uint, string) {
	t.Helper()
	account := s.newAccount(t, address)
	_, password, err := repositories.NewCredentialRepository(s.db).CreateAppPassword(account, account, "IMAP cutover", nil)
	if err != nil {
		t.Fatal(err)
	}
	return account, password
}

func appendMigrationMessage(client *imapclient.Client, folder, raw string, options *imap.AppendOptions) (*imap.AppendData, error) {
	command := client.Append(folder, int64(len(raw)), options)
	_, writeErr := io.WriteString(command, raw)
	closeErr := command.Close()
	data, status := command.Wait()
	return data, errors.Join(status, writeErr, closeErr)
}

func (s *migrationServer) idleImportFolder(t *testing.T, address, password, folder string) (<-chan uint32, func()) {
	t.Helper()
	events := make(chan uint32, 8)
	client, err := imapclient.DialTLS(s.imapAddr, &imapclient.Options{
		TLSConfig: s.imapTLS,
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{Mailbox: func(update *imapclient.UnilateralDataMailbox) {
			if update.NumMessages != nil {
				select {
				case events <- *update.NumMessages:
				default:
				}
			}
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Login(address, password).Wait(); err != nil {
		t.Fatal(err)
	}
	if selected, err := client.Select(folder, nil).Wait(); err != nil || selected.NumMessages != 0 {
		t.Fatalf("SELECT %s before IDLE: %+v, %v", folder, selected, err)
	}
	idle, err := client.Idle()
	if err != nil {
		t.Fatal(err)
	}
	return events, func() {
		t.Helper()
		if err := idle.Close(); err != nil {
			t.Fatal(err)
		}
		if err := idle.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func waitImportExists(t *testing.T, events <-chan uint32, want uint32) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("IDLE received %d EXISTS, want %d", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("IDLE never received %d EXISTS", want)
	}
}

func (s *migrationServer) checkImportedIMAPRow(t *testing.T, account uint, uid imap.UID, folder string, date time.Time, flags []imap.Flag) {
	t.Helper()
	var row models.Message
	if err := s.db.Where("mailbox_id = ? AND id = ?", account, uid).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Folder != folder || row.RawMessage != migrationImportMail || !row.ReceivedAt.Equal(date) || row.IsDeleted {
		t.Fatalf("UID %d: folder %q, raw preserved %t, date %s, tombstone %t", uid, row.Folder, row.RawMessage == migrationImportMail, row.ReceivedAt, row.IsDeleted)
	}
	if row.IsRead != slices.Contains(flags, imap.FlagSeen) || row.IsAnswered != slices.Contains(flags, imap.FlagAnswered) ||
		row.IsFlagged != slices.Contains(flags, imap.FlagFlagged) || row.IsStarred != row.IsFlagged ||
		row.IsDraft != slices.Contains(flags, imap.FlagDraft) || row.IMAPDeleted != slices.Contains(flags, imap.FlagDeleted) {
		t.Fatalf("UID %d: read=%t answered=%t flagged=%t starred=%t draft=%t IMAP deleted=%t, want %v", uid, row.IsRead, row.IsAnswered, row.IsFlagged, row.IsStarred, row.IsDraft, row.IMAPDeleted, flags)
	}
	var wantKeywords []string
	for _, flag := range flags {
		if !strings.HasPrefix(string(flag), `\`) {
			wantKeywords = append(wantKeywords, string(flag))
		}
	}
	keywords := slices.Clone([]string(row.Keywords))
	slices.Sort(keywords)
	if !slices.Equal(keywords, wantKeywords) {
		t.Fatalf("UID %d: keywords %q, want %q", uid, keywords, wantKeywords)
	}
}

func (s *migrationServer) importAudits(t *testing.T) []models.AuditEntry {
	t.Helper()
	var rows []models.AuditEntry
	if err := s.db.Where("action = ?", "messages.import").Order("seq").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func (s *migrationServer) requireDeniedImportAudit(t *testing.T, account uint) models.AuditEntry {
	t.Helper()
	for _, row := range s.importAudits(t) {
		if row.Status == http.StatusForbidden && row.ErrorCode == "forbidden" && row.Target["account_id"] == strconv.FormatUint(uint64(account), 10) && row.Target["refused"] == "1" && row.Target["imported"] == "0" {
			return row
		}
	}
	t.Fatal("no attributable 403 import audit with refused=1, imported=0")
	return models.AuditEntry{}
}

func (s *migrationServer) importProofRequest(t *testing.T, method, path, token string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, s.apiURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(data)
}
