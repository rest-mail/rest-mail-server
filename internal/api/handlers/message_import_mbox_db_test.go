package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// An APPEND of a message saved from an mbox begins with the mbox "From_" line. It is
// dropped and the message is stored from its real first header, and the summary counts
// it; any other first line that is not a header is still refused (#1498).
func TestImportDropsALeadingMboxSeparatorLine(t *testing.T) {
	f := newImportSessionFixture(t)
	session := f.open(f.fx.user.ID, apitypes.OpenImportSessionBody{ClientName: "imapsync"})
	path := fmt.Sprintf("/api/v1/accounts/%d/messages/import?import_session=%s", f.fx.user.ID, session)
	send := func(raw string) int {
		t.Helper()
		body := apitypes.ImportMessageBody{Folder: "Archive", RawMessage: []byte(raw),
			InternalDate: time.Date(2006, 10, 12, 9, 14, 2, 0, time.UTC)}
		return f.call(http.MethodPost, path, f.fx.user.ID, body).Code
	}
	const real = "Message-ID: <mbox@example.test>\r\nFrom: Alice <alice@example.com>\r\nSubject: from an mbox\r\n\r\nbody\r\n"
	if code := send("From alice@example.com Thu Oct 12 09:14:02 2006\r\n" + real); code != http.StatusOK {
		t.Fatalf("mbox separator: got %d, want 200", code)
	}
	var stored models.Message
	if err := f.db.Where("mailbox_id = ? AND message_id = ?", f.fx.user.ID, "<mbox@example.test>").First(&stored).Error; err != nil {
		t.Fatalf("the message was not stored: %v", err)
	}
	if stored.RawMessage != real || stored.RawSize != len(real) {
		t.Fatalf("stored source %q (%d bytes), want it to start at its first header", stored.RawMessage, stored.RawSize)
	}
	if stored.Subject != "from an mbox" || stored.Sender != "alice@example.com" {
		t.Fatalf("headers were not read after the separator: subject %q sender %q", stored.Subject, stored.Sender)
	}
	// A bare LF line end and an mbox writer's zone are the same separator.
	if code := send("From MAILER-DAEMON Fri Jul  8 12:08:34 +0000 2011\n" + strings.Replace(real, "<mbox@", "<mbox-lf@", 1)); code != http.StatusOK {
		t.Fatalf("LF mbox separator: got %d, want 200", code)
	}
	for _, broken := range []string{
		"not a header at all\r\n" + strings.Replace(real, "<mbox@", "<broken@", 1),
		"From alice@example.com\r\n" + strings.Replace(real, "<mbox@", "<nodate@", 1),
		"From alice@example.com yesterday\r\n" + strings.Replace(real, "<mbox@", "<baddate@", 1),
	} {
		if code := send(broken); code != http.StatusUnprocessableEntity {
			t.Fatalf("%q: got %d, want 422", strings.SplitN(broken, "\r\n", 2)[0], code)
		}
	}
	w := f.call(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/messages/import-sessions/%s", f.fx.user.ID, session), f.fx.user.ID, nil)
	var out endpoint.Data[apitypes.ImportSessionSummary]
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	s := out.Data
	if s.Imported != 2 || s.Refused != 3 || s.MboxSeparatorStripped != 2 || s.Folders["Archive"].MboxSeparatorStripped != 2 {
		t.Fatalf("summary %+v, want 2 imported, 3 refused, 2 separators stripped", s)
	}
	entries := f.entries()
	if len(entries) != 1 || entries[0].Target["mbox_separator_stripped"] != "2" {
		t.Fatalf("audit entries %+v, want one summary counting 2 separators", entries)
	}
}
