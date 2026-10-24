package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// folderRulesFixture is an account with mail in INBOX, Work and Personal, behind the folder
// routes with its session.
type folderRulesFixture struct {
	db      *gorm.DB
	mailbox uint
	routes  http.Handler
}

func newFolderRulesFixture(t *testing.T) folderRulesFixture {
	t.Helper()
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	for _, folder := range []string{"INBOX", "Work", "Work", "Personal"} {
		if err := tx.Create(&models.Message{MailboxID: fx.mailbox.ID, Folder: folder, Sender: "a@b.test", SizeBytes: 10}).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	NewMessageHandler(tx, nil, nil, "", nil).RegisterFolders(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	return folderRulesFixture{db: tx, mailbox: fx.mailbox.ID, routes: router}
}

func (fx folderRulesFixture) send(t *testing.T, method, folder, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := accountFoldersPath(fx.mailbox)
	if folder != "" {
		path += "/" + url.PathEscape(folder)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	fx.routes.ServeHTTP(rr, req)
	return rr
}

// count is how many messages, the deleted marker among them, the folder holds.
func (fx folderRulesFixture) count(t *testing.T, folder string) int64 {
	t.Helper()
	var n int64
	if err := fx.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", fx.mailbox, folder).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func wantRefusal(t *testing.T, rr *httptest.ResponseRecorder, status int, text string) {
	t.Helper()
	if rr.Code != status || !strings.Contains(rr.Body.String(), text) {
		t.Fatalf("want %d with %q, got %d %s", status, text, rr.Code, rr.Body)
	}
}

// A standard folder is not renamed, and nothing is renamed to one (#757).
func TestRenameFolder_StandardFolders(t *testing.T) {
	fx := newFolderRulesFixture(t)
	for _, folder := range []string{"INBOX", "inbox", "Sent", "Drafts", "Trash", "Spam", "Archive"} {
		wantRefusal(t, fx.send(t, http.MethodPatch, folder, `{"name":"Old mail"}`), http.StatusBadRequest, "Cannot rename standard folder")
	}
	for _, name := range []string{"INBOX", "Inbox", "Sent", "Drafts", "Trash", "Spam", "Archive"} {
		wantRefusal(t, fx.send(t, http.MethodPatch, "Work", fmt.Sprintf(`{"name":%q}`, name)), http.StatusBadRequest,
			"Cannot rename a folder to a standard folder")
	}
	if got := fx.count(t, "INBOX"); got != 1 {
		t.Fatalf("INBOX holds %d messages, want 1", got)
	}
	if got := fx.count(t, "Work"); got != 2 {
		t.Fatalf("Work holds %d messages, want 2", got)
	}
}

// A rename onto a folder that exists is refused rather than merging the two (#757).
func TestRenameFolder_DoesNotMerge(t *testing.T) {
	fx := newFolderRulesFixture(t)
	wantRefusal(t, fx.send(t, http.MethodPatch, "Work", `{"name":"Personal"}`), http.StatusConflict, "already_exists")
	wantRefusal(t, fx.send(t, http.MethodPatch, "Work", `{"name":"Work"}`), http.StatusConflict, "already_exists")
	if work, personal := fx.count(t, "Work"), fx.count(t, "Personal"); work != 2 || personal != 1 {
		t.Fatalf("Work %d, Personal %d: want 2 and 1, unmerged", work, personal)
	}
	// A folder that does not exist is 404 whatever its new name.
	wantRefusal(t, fx.send(t, http.MethodPatch, "Ghost", `{"name":"Personal"}`), http.StatusNotFound, "not_found")

	rr := fx.send(t, http.MethodPatch, "Work", `{"name":"Work/2026"}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"Work/2026"`) {
		t.Fatalf("rename: got %d %s", rr.Code, rr.Body)
	}
	if moved, left := fx.count(t, "Work/2026"), fx.count(t, "Work"); moved != 2 || left != 0 {
		t.Fatalf("Work/2026 %d, Work %d: want 2 and 0", moved, left)
	}
}

// Create and rename take the name rule's words for a bad name, as a 422 on name.
func TestFolderNames_Refused(t *testing.T) {
	fx := newFolderRulesFixture(t)
	for name, want := range map[string]string{
		`"   "`:              "required",
		`" Receipts"`:        "must not begin or end with a space",
		`"a\u0000b"`:         "must not contain a control character",
		`"Work/../Personal"`: `must not contain \"..\"`,
		fmt.Sprintf("%q", strings.Repeat("x", 256)): "must be at most 255 bytes",
	} {
		for _, rr := range []*httptest.ResponseRecorder{
			fx.send(t, http.MethodPost, "", `{"name":`+name+`}`),
			fx.send(t, http.MethodPatch, "Work", `{"name":`+name+`}`),
		} {
			wantRefusal(t, rr, http.StatusUnprocessableEntity, `"name":"`+want+`"`)
		}
	}
	if got := fx.count(t, "Work"); got != 2 {
		t.Fatalf("Work holds %d messages, want 2", got)
	}
}

// INBOX in any case, and the folders listed empty or not, already exist; Spam and Archive
// are created when the account has neither.
func TestCreateFolder_ExistingNames(t *testing.T) {
	fx := newFolderRulesFixture(t)
	for _, name := range []string{"INBOX", "inbox", "Sent", "Drafts", "Trash", "Work"} {
		wantRefusal(t, fx.send(t, http.MethodPost, "", fmt.Sprintf(`{"name":%q}`, name)), http.StatusConflict, "already_exists")
	}
	if got := fx.count(t, "inbox"); got != 0 {
		t.Fatalf("a lowercase inbox was created: %d rows", got)
	}
	rr := fx.send(t, http.MethodPost, "", `{"name":"Archive"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create Archive: got %d %s", rr.Code, rr.Body)
	}
}

// INBOX is not deleted in any case.
func TestDeleteFolder_InboxInAnyCase(t *testing.T) {
	fx := newFolderRulesFixture(t)
	wantRefusal(t, fx.send(t, http.MethodDelete, "inbox", ""), http.StatusBadRequest, "Cannot delete standard folder")
	if got := fx.count(t, "INBOX"); got != 1 {
		t.Fatalf("INBOX holds %d messages, want 1", got)
	}
}
