package imap

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

func TestServer_IdleSeesImportFromAnotherClient(t *testing.T) {
	const raw = "Message-ID: <idle-import@example.test>\r\nSubject: Imported\r\n\r\nnew body\r\n"
	for _, operation := range []string{"APPEND", "COPY"} {
		t.Run(operation, func(t *testing.T) {
			api := newFakeAPI()
			api.seed(1, "INBOX", raw, "Imported")
			api.seed(2, "Archive", "Subject: Existing\r\n\r\nold body\r\n", "Existing")
			server := httptest.NewServer(api.handler())
			t.Cleanup(server.Close)
			backend := NewBackend(apiclient.New(server.URL), nil)
			idle := newTranscript(t, backend, func(session *imapserver.Session) {
				session.SetIdlePollInterval(10 * time.Millisecond)
			})
			writer := newTranscript(t, backend)
			idle.login("i1")
			writer.login("w1")
			lines, status := idle.command("i2", "SELECT Archive")
			if !strings.HasPrefix(status, "i2 OK ") || !slices.Contains(lines, "* 1 EXISTS") {
				t.Fatalf("initial SELECT: %q, %q; want one existing message", lines, status)
			}
			if _, status := writer.command("w2", "SELECT INBOX"); !strings.HasPrefix(status, "w2 OK ") {
				t.Fatalf("writer SELECT: %q", status)
			}
			idle.send("i3 IDLE")
			if continuation := idle.readLine(); !strings.HasPrefix(continuation, "+") {
				t.Fatalf("IDLE continuation = %q", continuation)
			}

			switch operation {
			case "APPEND":
				_, status = writer.appendMsg("w3", "Archive", raw)
			case "COPY":
				_, status = writer.command("w3", "UID COPY 1 Archive")
			}
			if !strings.HasPrefix(status, "w3 OK ") {
				t.Fatalf("%s: %q", operation, status)
			}
			switch operation {
			case "APPEND":
				request := api.lastImport(t)
				if request.Folder != "Archive" || string(request.RawMessage) != raw {
					t.Fatalf("%s import: folder %q, raw %q", operation, request.Folder, request.RawMessage)
				}
			case "COPY":
				api.mu.Lock()
				transfers := slices.Clone(api.transfers)
				api.mu.Unlock()
				if len(transfers) != 1 || transfers[0].Folder != "Archive" || !slices.Equal(transfers[0].MessageIDs, []uint{1}) || transfers[0].Move {
					t.Fatalf("COPY transfers: %+v", transfers)
				}
			}

			if notification := idle.readLine(); notification != "* 2 EXISTS" {
				t.Fatalf("IDLE after %s = %q, want * 2 EXISTS within five seconds", operation, notification)
			}
			idle.send("DONE")
			if lines, status := idle.readResponse("i3"); len(lines) != 0 || !strings.HasPrefix(status, "i3 OK ") {
				t.Fatalf("IDLE completion: %q, %q", lines, status)
			}
		})
	}
}
