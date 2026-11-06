package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/importsession"
	"gorm.io/gorm"
)

type importSessionFixture struct {
	t      *testing.T
	db     *gorm.DB
	fx     accountsFixture
	router http.Handler
}

func newImportSessionFixture(t *testing.T) *importSessionFixture {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 1<<20, nil, nil)
	h.directory = t.TempDir()
	return &importSessionFixture{t: t, db: db, fx: fx, router: importMessageRoutes(h)}
}

func (f *importSessionFixture) call(method, path string, actor uint, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	encoded := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		encoded = string(data)
	}
	r := authzRequest(method, path, actor, nil, encoded)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func (f *importSessionFixture) open(actor uint, client apitypes.OpenImportSessionBody) string {
	f.t.Helper()
	w := f.call(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import-sessions", f.fx.user.ID), actor, client)
	var out endpoint.Data[apitypes.ImportSession]
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Data.ID) != 32 || out.Data.IdleTimeoutSeconds != 3600 {
		f.t.Fatalf("open: %d %s", w.Code, w.Body)
	}
	return out.Data.ID
}

func (f *importSessionFixture) importInto(session, folder string, n int, valid bool) int {
	f.t.Helper()
	path := fmt.Sprintf("/api/v1/accounts/%d/messages/import", f.fx.user.ID)
	if session != "" {
		path += "?import_session=" + session
	}
	body := apitypes.ImportMessageBody{Folder: folder, RawMessage: []byte(fmt.Sprintf("Message-ID: <s%d@example.test>\r\nSubject: %d\r\n\r\nbody\r\n", n, n))}
	if valid {
		body.InternalDate = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	}
	return f.call(http.MethodPost, path, f.fx.user.ID, body).Code
}

func (f *importSessionFixture) entries() []models.AuditEntry {
	f.t.Helper()
	var entries []models.AuditEntry
	if err := f.db.Where("action = ?", importsession.Action).Order("seq").Find(&entries).Error; err != nil {
		f.t.Fatal(err)
	}
	return entries
}

// An import session is audited once, on close, with counts per folder: imported,
// deduplicated, refused and bytes. Never one entry per message.
func TestImportSessionWritesOneSummary(t *testing.T) {
	f := newImportSessionFixture(t)
	session := f.open(f.fx.user.ID, apitypes.OpenImportSessionBody{ClientName: "imapsync", ClientVersion: "2.290"})
	for i, want := range []struct {
		folder string
		n      int
		valid  bool
		code   int
	}{{"Archive", 1, true, 200}, {"Archive", 1, true, 200}, {"Sent", 2, true, 200}, {"Sent", 3, false, 422}} {
		if got := f.importInto(session, want.folder, want.n, want.valid); got != want.code {
			t.Fatalf("import %d: %d, want %d", i, got, want.code)
		}
	}
	if entries := f.entries(); len(entries) != 0 {
		t.Fatalf("audited before close: %+v", entries)
	}
	// Only the opener may close it, not even an admin of the account.
	if w := f.call(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/messages/import-sessions/%s", f.fx.user.ID, session), f.fx.admin.ID, nil); w.Code != http.StatusNotFound {
		t.Fatalf("other account close: %d %s", w.Code, w.Body)
	}
	w := f.call(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/messages/import-sessions/%s", f.fx.user.ID, session), f.fx.user.ID, nil)
	var summary endpoint.Data[apitypes.ImportSessionSummary]
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &summary) != nil {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	s := summary.Data
	if s.Imported != 2 || s.Deduplicated != 1 || s.Refused != 1 || s.ClientName != "imapsync" || s.Folders["Archive"].Imported != 1 || s.Folders["Archive"].Deduplicated != 1 || s.Folders["Sent"].Refused != 1 || s.Bytes == 0 {
		t.Fatalf("summary: %+v", s)
	}
	entries := f.entries()
	if len(entries) != 1 {
		t.Fatalf("%d audit entries, want one summary", len(entries))
	}
	target := entries[0].Target
	if target["import_session"] != session || target["client_name"] != "imapsync" || target["client_version"] != "2.290" || target["imported"] != "2" || target["deduplicated"] != "1" || target["refused"] != "1" || target["closed_by"] != "closed" {
		t.Fatalf("summary entry: %+v", target)
	}
	var folders map[string]apitypes.ImportFolderCounts
	if err := json.Unmarshal([]byte(target["folders"]), &folders); err != nil || folders["Sent"].Imported != 1 || folders["Sent"].Refused != 1 {
		t.Fatalf("folders %q: %v", target["folders"], err)
	}
	var left int64
	f.db.Model(&models.ImportSession{}).Count(&left)
	if left != 0 {
		t.Fatalf("%d sessions left after close", left)
	}
	// A closed session is gone: importing into it is refused and audited on its own.
	if got := f.importInto(session, "Archive", 9, true); got != http.StatusNotFound {
		t.Fatalf("import into closed session: %d", got)
	}
	if entries := f.entries(); len(entries) != 2 || entries[1].Target["refused"] != "1" || entries[1].ErrorCode != "import_session_not_found" {
		t.Fatalf("refusal of a closed session: %+v", entries)
	}
}

// 1,000 imports in one session are one audit entry.
func TestImportSessionThousandImportsOneEntry(t *testing.T) {
	f := newImportSessionFixture(t)
	session := f.open(f.fx.user.ID, apitypes.OpenImportSessionBody{})
	for i := range 1000 {
		if got := f.importInto(session, "Archive", i, true); got != http.StatusOK {
			t.Fatalf("import %d: %d", i, got)
		}
	}
	if w := f.call(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/messages/import-sessions/%s", f.fx.user.ID, session), f.fx.user.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	if entries := f.entries(); len(entries) != 1 || entries[0].Target["imported"] != "1000" {
		t.Fatalf("%d entries, want one summary of 1000", len(entries))
	}
}

// An import without a session is still audited, as a summary of its one message,
// and so is a refused one.
func TestImportWithoutSessionIsItsOwnSummary(t *testing.T) {
	f := newImportSessionFixture(t)
	if got := f.importInto("", "Archive", 1, true); got != http.StatusOK {
		t.Fatalf("import: %d", got)
	}
	if got := f.importInto("", "Archive", 2, false); got != http.StatusUnprocessableEntity {
		t.Fatalf("refused import: %d", got)
	}
	entries := f.entries()
	if len(entries) != 2 {
		t.Fatalf("%d entries, want two", len(entries))
	}
	if e := entries[0]; e.Target["imported"] != "1" || e.Target["closed_by"] != "single" || e.Target["import_session"] != "" || e.Status != 200 {
		t.Fatalf("single import: %+v", e)
	}
	if e := entries[1]; e.Target["refused"] != "1" || e.Status != 422 || e.ErrorCode == "" || e.Outcome != models.AuditOutcomeFailed {
		t.Fatalf("single refusal: %+v", e)
	}
}

// A session nobody closes is closed after IdleTimeout and written with its opener.
func TestImportSessionIdleTimeoutCloses(t *testing.T) {
	f := newImportSessionFixture(t)
	session := f.open(f.fx.user.ID, apitypes.OpenImportSessionBody{ClientName: "imapsync"})
	if got := f.importInto(session, "Archive", 1, true); got != http.StatusOK {
		t.Fatalf("import: %d", got)
	}
	if closed, err := importsession.CloseIdle(f.db, time.Now()); err != nil || closed != 0 {
		t.Fatalf("closed a live session: %d %v", closed, err)
	}
	if closed, err := importsession.CloseIdle(f.db, time.Now().Add(importsession.IdleTimeout+time.Minute)); err != nil || closed != 1 {
		t.Fatalf("idle close: %d %v", closed, err)
	}
	entries := f.entries()
	if len(entries) != 1 || entries[0].Target["closed_by"] != "idle" || entries[0].Target["imported"] != "1" || entries[0].Target["client_name"] != "imapsync" || entries[0].ActorID == nil || *entries[0].ActorID != f.fx.user.ID {
		t.Fatalf("idle summary: %+v", entries)
	}
}
