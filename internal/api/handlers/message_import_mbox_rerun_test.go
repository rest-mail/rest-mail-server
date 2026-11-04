package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/mailimport"
)

// #1838: an mbox exported again after a flag-only edit on the source, as
// restmail import-mbox sends it. The rerun skips the edited message, keeping
// its first imported flags as docs/migrating-mail.md says, stores no second
// copy, and still imports a message that is new.
func TestMessageImportMboxFlagOnlyRerunStoresNoCopy(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Mailbox{}).Where("id = ?", fx.user.ID).Update("quota_bytes", 1<<30).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &fx.user); err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 1<<20, nil, nil)
	h.directory = t.TempDir()
	routes := importMessageRoutes(h)
	message := func(id, metadata string) string {
		return "From sender@example.test Mon Jan  2 15:04:05 2006\n" +
			"Message-ID: <" + id + "@fixture.invalid>\nFrom: sender@example.test\nSubject: " + id + "\n" + metadata + "\nbody " + id + "\n\n"
	}
	run := func(mbox string) (imported, skipped int) {
		t.Helper()
		err := mailimport.Mbox(t.Context(), strings.NewReader(mbox), "INBOX", "mboxrd", 1<<20, func(_ string, m apitypes.ImportMessageBody) error {
			body, err := json.Marshal(m)
			if err != nil {
				return err
			}
			r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.user.ID, nil, string(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			var answer struct {
				Data struct {
					Imported bool `json:"imported"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
				return err
			}
			if answer.Data.Imported {
				imported++
			} else {
				skipped++
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return imported, skipped
	}
	first := message("734.blf.0.0", "Status: RO\nX-Status: A\nX-UID: 1\n") + message("734.blf.0.1", "Status: O\nX-UID: 2\n")
	if imported, skipped := run(first); imported != 2 || skipped != 0 {
		t.Fatalf("first pass imported %d skipped %d", imported, skipped)
	}
	if imported, skipped := run(first); imported != 0 || skipped != 2 {
		t.Fatalf("unchanged rerun imported %d skipped %d", imported, skipped)
	}
	edited := message("734.blf.0.0", "X-IMAPbase: 1700000000 3\nStatus: O\nX-Status: F\nX-UID: 1\nX-Keywords: $Junk\n") +
		message("734.blf.0.1", "Status: O\nX-UID: 2\n") + message("734.blf.0.2", "Status: O\nX-UID: 3\n")
	if imported, skipped := run(edited); imported != 1 || skipped != 2 {
		t.Fatalf("rerun after a flag-only edit imported %d skipped %d, want 1 and 2", imported, skipped)
	}
	var rows []models.Message
	if err := db.Where("mailbox_id = ? AND folder = ?", fx.user.ID, "INBOX").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d stored messages for 3 source messages", len(rows))
	}
	if edited := rows[0]; edited.MsgID != "<734.blf.0.0@fixture.invalid>" || !edited.IsRead || !edited.IsAnswered || edited.IsFlagged || len(edited.Keywords) != 0 {
		t.Fatalf("the first import's flags were not retained: %s read=%t answered=%t flagged=%t keywords=%v", edited.MsgID, edited.IsRead, edited.IsAnswered, edited.IsFlagged, edited.Keywords)
	}
}
