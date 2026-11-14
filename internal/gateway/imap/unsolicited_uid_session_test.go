package imap

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

func TestServer_UnsolicitedFetchIncludesUID(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, command := range []string{"NOOP", "CHECK", "IDLE"} {
			t.Run(selection+"/"+command, func(t *testing.T) {
				api := newFakeAPI()
				api.seed(17, "INBOX", "Subject: First\r\n\r\nfirst\r\n", "First")
				api.seed(41, "INBOX", "Subject: Second\r\n\r\nsecond\r\n", "Second")
				server := httptest.NewServer(api.handler())
				t.Cleanup(server.Close)
				backend := NewBackend(apiclient.New(server.URL), nil)
				observer := newTranscript(t, backend, func(session *imapserver.Session) {
					session.SetIdlePollInterval(10 * time.Millisecond)
				})
				writer := newTranscript(t, backend)
				observer.login("o1")
				writer.login("w1")
				if _, status := observer.command("o2", "%s INBOX", selection); !strings.HasPrefix(status, "o2 OK ") {
					t.Fatalf("observer %s: %q", selection, status)
				}
				if _, status := writer.command("w2", "SELECT INBOX"); !strings.HasPrefix(status, "w2 OK ") {
					t.Fatalf("writer SELECT: %q", status)
				}
				if command == "IDLE" {
					observer.send("o3 IDLE")
					if got := observer.readLine(); got != "+ idling" {
						t.Fatalf("IDLE continuation = %q", got)
					}
				}
				if lines, status := writer.command("w3", `UID STORE 41 +FLAGS.SILENT (\Deleted)`); len(lines) != 0 || !strings.HasPrefix(status, "w3 OK ") {
					t.Fatalf("writer STORE: %q, %q", lines, status)
				}
				const want = `* 2 FETCH (UID 41 FLAGS (\Deleted \Recent))`
				if command == "IDLE" {
					if got := observer.readLine(); got != want {
						t.Fatalf("IDLE update = %q, want %q", got, want)
					}
					observer.send("DONE")
					if lines, status := observer.readResponse("o3"); len(lines) != 0 || !strings.HasPrefix(status, "o3 OK ") {
						t.Fatalf("IDLE completion: %q, %q", lines, status)
					}
				} else {
					if lines, status := observer.command("o3", "%s", command); len(lines) != 1 || lines[0] != want || !strings.HasPrefix(status, "o3 OK ") {
						t.Fatalf("%s update = %q, %q; want %q", command, lines, status, want)
					}
				}
				if lines, status := observer.command("o4", "NOOP"); len(lines) != 0 || !strings.HasPrefix(status, "o4 OK ") {
					t.Fatalf("unchanged mailbox: %q, %q", lines, status)
				}
			})
		}
	}
}
