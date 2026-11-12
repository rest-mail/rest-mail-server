package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// importProofDB copies a migrated database with its own import tables and roles.
func importProofDB(t *testing.T, extraModels ...any) *gorm.DB {
	t.Helper()
	setup := append([]any{withForeignKeys}, accountModels...)
	setup = append(setup, &models.Message{}, &models.Attachment{}, &models.ImportSession{})
	setup = append(setup, extraModels...)
	setup = append(setup, withFolderUIDMarks, withFolderModSeqs, withAudit, withRoles)
	db := openScratchDB(t, "importproof", setup)
	db.Logger = logger.Default.LogMode(logger.Silent)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	return db
}

func TestMessageImportPostgresConcurrencySourceQuotaAndAuditRollback(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 4096, nil, nil)
	h.directory = t.TempDir()
	router := importMessageRoutes(h)
	raw := []byte("Message-ID: <binary-history@example.test>\r\nFrom: Original <old@example.test>\r\nSubject: original\r\nAuthentication-Results: previous.example; spf=pass\r\nContent-Type: multipart/mixed; boundary=OLD\r\n\r\n--OLD\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\nbody\xff\x00\r\n--OLD\r\nContent-Type: application/octet-stream; name=empty.bin\r\nContent-Disposition: attachment; filename=empty.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n\r\n--OLD--\r\n")
	date := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	body := apitypes.ImportMessageBody{RawMessage: raw, Folder: "Archive", InternalDate: date, Flags: []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`, `\Deleted`, "$Forwarded", "$Junk"}}
	call := func(message apitypes.ImportMessageBody) *httptest.ResponseRecorder {
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.admin.ID, nil, string(encoded))
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		return w
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { <-start; results <- call(body) }()
	}
	close(start)
	imported, skipped := 0, 0
	var id uint
	for range 2 {
		w := <-results
		if w.Code != 200 {
			t.Fatalf("concurrent import: %d %s", w.Code, w.Body)
		}
		var result endpoint.Data[apitypes.ImportMessageResult]
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if id != 0 && id != result.Data.ID {
			t.Fatal("concurrent copies received different identities")
		}
		id = result.Data.ID
		if result.Data.Imported {
			imported++
		} else {
			skipped++
		}
	}
	if imported != 1 || skipped != 1 {
		t.Fatalf("concurrent results: imported=%d skipped=%d", imported, skipped)
	}
	// The same Message-ID with other octets is a different message, so it is kept.
	edited := body
	edited.RawMessage = bytes.Replace(raw, []byte("Subject: ori"), []byte("Subject: edi"), 1)
	if bytes.Equal(edited.RawMessage, raw) {
		t.Fatal("the edit changed nothing")
	}
	edit := call(edited)
	var different endpoint.Data[apitypes.ImportMessageResult]
	if err := json.Unmarshal(edit.Body.Bytes(), &different); err != nil || edit.Code != 200 || !different.Data.Imported || different.Data.ID == id {
		t.Fatalf("same Message-ID, other content: %d %s %v", edit.Code, edit.Body, err)
	}
	if err := db.Delete(&models.Attachment{}, "message_id = ?", different.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Delete(&models.Message{}, different.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", fx.user.ID).Update("message_count", 1).Error; err != nil {
		t.Fatal(err)
	}
	var stored models.Message
	if err := db.First(&stored, id).Error; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.Source(), raw) || stored.RawSize != len(raw) || stored.RawMessage != "" || !stored.ReceivedAt.Equal(date) || !stored.IsRead || !stored.IsAnswered || !stored.IsFlagged || !stored.IsDraft || !stored.IMAPDeleted || strings.Join(stored.Keywords, ",") != "$Forwarded,$Junk" || stored.IsDeleted {
		t.Fatalf("source/metadata changed: %+v", stored)
	}
	var attachments []models.Attachment
	if err := db.Where("message_id = ?", id).Find(&attachments).Error; err != nil || len(attachments) != 1 {
		t.Fatalf("empty attachment: %v %+v", err, attachments)
	}
	data, err := os.ReadFile(attachments[0].StorageRef)
	if err != nil || len(data) != 0 || attachments[0].SizeBytes != 0 {
		t.Fatalf("empty attachment lost: %q %v", data, err)
	}
	var usage models.QuotaUsage
	if err := db.Where("mailbox_id = ?", fx.user.ID).First(&usage).Error; err != nil || usage.MessageCount != 1 {
		t.Fatalf("duplicate quota: %+v %v", usage, err)
	}
	var account models.Mailbox
	if err := db.First(&account, fx.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	beforeQuota := account.QuotaUsedBytes
	// Refusing a mandatory success-audit insert must roll back the message,
	// attachment reference and both quota counters, not acknowledge partial mail.
	if err := db.Exec(`CREATE FUNCTION reject_import_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action = 'messages.import' THEN RAISE EXCEPTION 'private audit failure'; END IF; RETURN NEW; END; $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_import_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_import_audit()`).Error; err != nil {
		t.Fatal(err)
	}
	body.RawMessage = bytes.Replace(raw, []byte("binary-history@"), []byte("rollback-history@"), 1)
	w := call(body)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("audit refusal: %d %s", w.Code, w.Body)
	}
	var count int64
	if err := db.Model(&models.Message{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed import retained a message: %d %v", count, err)
	}
	if err := db.Model(&models.Attachment{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed import retained metadata: %d %v", count, err)
	}
	if err := db.First(&account, fx.user.ID).Error; err != nil || account.QuotaUsedBytes != beforeQuota {
		t.Fatalf("audit refusal changed quota: %+v %v", account, err)
	}
	if err := db.Where("mailbox_id = ?", fx.user.ID).First(&usage).Error; err != nil || usage.MessageCount != 1 {
		t.Fatalf("audit refusal changed counters: %+v %v", usage, err)
	}
}

// An account imports into its own mailbox by default, and nobody else's.
func TestMessageImportOwnAccountOnly(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &fx.user); err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 4096, nil, nil)
	h.directory = t.TempDir()
	router := importMessageRoutes(h)
	encoded, err := json.Marshal(apitypes.ImportMessageBody{
		RawMessage:   []byte("Message-ID: <own@example.test>\r\nSubject: mine\r\n\r\nbody\r\n"),
		InternalDate: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(account, caller uint) int {
		request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", account), caller, nil, string(encoded))
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		return w.Code
	}
	if got := call(fx.user.ID, fx.user.ID); got != http.StatusOK {
		t.Fatalf("own mailbox: %d, want 200", got)
	}
	if got := call(fx.admin.ID, fx.user.ID); got != http.StatusNotFound {
		t.Fatalf("someone else's mailbox: %d, want 404", got)
	}
}

// mail:import is a revocable grant like mail:receive (#1498): a new account
// holds it on its own mailbox, an owner removing it through the grants API
// makes import refuse that account with 403, and granting it again restores
// import. Receiving is untouched throughout.
func TestMessageImportGrantRevocable(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	cache := grants.NewCache(db, grants.CacheTTL)
	owner := &auth.Claims{AccountID: fx.admin.ID, AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true, AuthTime: jwt.NewNumericDate(time.Now())}
	w := httptest.NewRecorder()
	accountRoutes(NewAccountHandler(db, cache, "")).ServeHTTP(w, withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/accounts",
		strings.NewReader(fmt.Sprintf(`{"address":"agent@%s","owned":true}`, fx.domain.Name))), owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", w.Code, w.Body)
	}
	agent := decodeAccount(t, w)
	var mailbox models.Mailbox
	if err := db.First(&mailbox, agent.ID).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 4096, nil, nil)
	h.directory = t.TempDir()
	router := importMessageRoutes(h)
	imported := 0
	importAs := func() int {
		t.Helper()
		imported++
		encoded, err := json.Marshal(apitypes.ImportMessageBody{
			RawMessage:   []byte(fmt.Sprintf("Message-ID: <grant-%d@example.test>\r\nSubject: mine\r\n\r\nbody\r\n", imported)),
			InternalDate: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", agent.ID), agent.ID, nil, string(encoded))
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		return w.Code
	}
	if got := importAs(); got != http.StatusOK {
		t.Fatalf("new account import: %d, want 200", got)
	}

	var grant models.Grant
	if err := db.Where("holder_kind = ? AND holder_id = ? AND scope = ? AND role_id = (SELECT id FROM roles WHERE name = ?)",
		models.GrantHolderAccount, agent.ID, grants.Mailbox(agent.Address), models.RoleImporter).First(&grant).Error; err != nil {
		t.Fatalf("new account has no importer grant: %v", err)
	}
	grantHandler := grantRoutes(NewGrantHandler(db, cache, ""))
	change := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		grantHandler.ServeHTTP(w, withClaims(httptest.NewRequest(method, path, strings.NewReader(body)), owner))
		return w
	}
	if w := change(http.MethodDelete, fmt.Sprintf("/api/v1/admin/grants/%d", grant.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke importer: %d %s", w.Code, w.Body)
	}
	if got := importAs(); got != http.StatusForbidden {
		t.Fatalf("import after revoke: %d, want 403", got)
	}
	if allowed, err := grants.CanReceive(db, &mailbox); err != nil || !allowed {
		t.Fatalf("revoking import changed receiving: %v %v", allowed, err)
	}

	body := fmt.Sprintf(`{"holder":{"kind":"account","id":%d},"role":%q,"scope":%q}`, agent.ID, models.RoleImporter, grants.Mailbox(agent.Address))
	if w := change(http.MethodPost, "/api/v1/admin/grants", body); w.Code != http.StatusCreated {
		t.Fatalf("restore importer: %d %s", w.Code, w.Body)
	}
	if got := importAs(); got != http.StatusOK {
		t.Fatalf("import after restore: %d, want 200", got)
	}
}

func TestMessageImport_InboxInAnyCaseIsInbox(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 4096, nil, nil)
	h.directory = t.TempDir()
	router := importMessageRoutes(h)
	for _, folder := range []string{"", "inbox", "Inbox", "INBOX"} {
		t.Run(folder, func(t *testing.T) {
			body, err := json.Marshal(apitypes.ImportMessageBody{
				RawMessage: []byte("Subject: Lower case inbox\r\n\r\nhello\r\n"), Folder: folder,
				InternalDate: time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC),
			})
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, string(body)))
			if rr.Code != http.StatusOK {
				t.Fatalf("import: expected 200, got %d (%s)", rr.Code, rr.Body.String())
			}
			var result endpoint.Data[apitypes.ImportMessageResult]
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var msg models.Message
			if err := db.First(&msg, result.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if msg.Folder != "INBOX" || msg.MailboxID != fx.user.ID {
				t.Errorf("import stored mailbox=%d folder=%q, want mailbox=%d INBOX", msg.MailboxID, msg.Folder, fx.user.ID)
			}
		})
	}
}
