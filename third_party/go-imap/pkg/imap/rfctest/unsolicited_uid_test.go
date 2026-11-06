package rfctest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// checklist: IMAP2-CMD-133
// checklist: IMAP2-RESP-215
func TestUnsolicitedFetchIncludesUID(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, command := range []string{"NOOP", "CHECK", "IDLE"} {
			t.Run(selection+"/"+command, func(t *testing.T) {
				box := &sharedMailbox{messages: []imap.Message{{UID: 1001}, {UID: 5002}, {UID: 9003}}}
				client := dialConfigured(t, box, nil, nil, func(session *server.Session) {
					session.SetIdlePollInterval(10 * time.Millisecond)
				})
				client.line()
				client.login()
				lines, tagged := client.command("C1", "CAPABILITY")
				requireOK(t, tagged, "C1")
				if !strings.Contains(strings.Join(lines, " "), "IMAP4rev1") || strings.Contains(strings.Join(lines, " "), "IMAP4rev2") {
					t.Fatalf("expected an IMAP4rev1 session, got %q", lines)
				}
				_, tagged = client.command("S1", "%s INBOX", selection)
				requireOK(t, tagged, "S1")

				box.remove(1001)
				box.set(5002, func(message *imap.Message) { message.Flagged = true })
				box.deliver(10004)
				want := []string{"* 1 EXPUNGE", `* 1 FETCH (UID 5002 FLAGS (\Flagged))`, "* 3 EXISTS"}
				if command == "IDLE" {
					if _, err := fmt.Fprint(client.conn, "U1 IDLE\r\n"); err != nil {
						t.Fatal(err)
					}
					if got := client.line(); got != "+ idling" {
						t.Fatalf("IDLE continuation = %q", got)
					}
					for _, expected := range want {
						if got := client.line(); got != expected {
							t.Fatalf("IDLE update = %q, want %q", got, expected)
						}
					}
					if _, err := fmt.Fprint(client.conn, "DONE\r\n"); err != nil {
						t.Fatal(err)
					}
					requireOK(t, client.line(), "U1")
				} else {
					lines, tagged = client.command("U1", "%s", command)
					requireOK(t, tagged, "U1")
					requireExactly(t, lines, want...)
				}

				lines, tagged = client.command("N1", "NOOP")
				requireOK(t, tagged, "N1")
				requireExactly(t, lines)
				lines, tagged = client.command("F1", "UID FETCH 5002 FLAGS")
				requireOK(t, tagged, "F1")
				requireExactly(t, lines, want[1])
			})
		}
	}
}
