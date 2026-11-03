package rfctest

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// acceptingCredentials deliberately accepts anything: malformed PLAIN framing
// must be rejected by the protocol layer before a permissive host can accept it.
type acceptingCredentials struct {
	calls      int
	user, pass string
}

func (b *acceptingCredentials) Authenticate(user, pass string) (imap.Mailbox, error) {
	b.calls++
	b.user, b.pass = user, pass
	return mailbox{}, nil
}

// RFC4616 section2 requires exactly two NUL delimiters, nonempty authcid and
// password, and well-formed UTF-8. Both SASL-IR and continuation exchanges must
// refuse malformed fields without invoking even a permissive backend.
// checklist: IMAP-PLAIN-01 IMAP-PLAIN-02
func TestPlainMalformedPayloads(t *testing.T) {
	for _, initial := range []bool{false, true} {
		for _, tc := range []struct{ name, payload string }{
			{"missing delimiters", "alice"},
			{"one delimiter", "\x00alice"},
			{"empty identity", "\x00\x00secret"},
			{"empty password", "\x00alice\x00"},
			{"both empty", "\x00\x00"},
			{"extra delimiter", "\x00alice\x00secret\x00tail"},
			{"invalid identity UTF8", "\x00\xff\x00secret"},
			{"invalid password UTF8", "\x00alice\x00\xc0\xaf"},
			{"surrogate identity", "\x00\xed\xa0\x80\x00secret"},
			{"invalid authzid UTF8", "\xff\x00alice\x00secret"},
			{"unauthorized identity", "bob\x00alice\x00secret"},
		} {
			t.Run(fmt.Sprintf("initial=%t/%s", initial, tc.name), func(t *testing.T) {
				b := &acceptingCredentials{}
				c := securityClient(t, b, imap.NopLimiter{})
				response := base64.StdEncoding.EncodeToString([]byte(tc.payload))
				var tagged string
				if initial {
					lines, result := c.command("A1", "AUTHENTICATE PLAIN %s", response)
					requireExactly(t, lines)
					tagged = result
				} else {
					tagged = c.authenticate("A1", "PLAIN", response)
				}
				if !strings.HasPrefix(tagged, "A1 NO ") || b.calls != 0 {
					t.Fatalf("malformed PLAIN: reply=%q backend calls=%d", tagged, b.calls)
				}
				_, selected := c.command("S1", "SELECT INBOX")
				if strings.HasPrefix(selected, "S1 OK ") {
					t.Fatalf("malformed PLAIN authenticated the session: %q", selected)
				}
				_, retry := c.command("A2", "AUTHENTICATE PLAIN %s", plainResponse("alice", "secret"))
				requireOK(t, retry, "A2")
				if b.calls != 1 || b.user != "alice" || b.pass != "secret" {
					t.Fatalf("retry credentials changed: %+v", b)
				}
			})
		}
	}
}

// RFC4616 section2 requires support through 255 octets in each component;
// RFC4959 section6 requires the encoded initial response to fit the parser.
// The empty authzid derives the identity; the explicit one must match it.
// checklist: IMAP-PLAIN-03 IMAP-SASLIR-05
func TestPlainRequiredCredentialLengths(t *testing.T) {
	for _, user := range []string{strings.Repeat("u", 255), strings.Repeat("é", 127) + "u"} {
		for _, explicit := range []bool{false, true} {
			for _, initial := range []bool{false, true} {
				t.Run(fmt.Sprintf("utf8=%t/explicit=%t/initial=%t", strings.Contains(user, "é"), explicit, initial), func(t *testing.T) {
					b := &acceptingCredentials{}
					c := securityClient(t, b, imap.NopLimiter{})
					pass := strings.Repeat("密", 85)
					authzid := ""
					if explicit {
						authzid = user
					}
					encoded := base64.StdEncoding.EncodeToString([]byte(authzid + "\x00" + user + "\x00" + pass))
					var tagged string
					if initial {
						lines, result := c.command("A1", "AUTHENTICATE plain %s", encoded)
						requireExactly(t, lines)
						tagged = result
					} else {
						tagged = c.authenticate("A1", "plain", encoded)
					}
					requireOK(t, tagged, "A1")
					if b.calls != 1 || b.user != user || b.pass != pass {
						t.Fatalf("255-octet credentials changed: %+v", b)
					}
				})
			}
		}
	}
}

// RFC5161 section3.1 explicitly permits LOGIN/ENABLE and ENABLE/SELECT
// pipelining. The selected mailbox must retain the already enabled mode.
// checklist: IMAP-ENABLE-07
func TestEnablePipelinedLoginAndSelect(t *testing.T) {
	box := &utf8Mailbox{}
	c := dialMailbox(t, box)
	c.line()
	wrote := make(chan error, 1)
	go func() {
		_, err := fmt.Fprintf(c.conn, "L1 LOGIN %s %s\r\nE1 ENABLE utf8=accept X-UNKNOWN\r\nS1 SELECT INBOX\r\n", testUser, testPass)
		wrote <- err
	}()
	requireOK(t, c.line(), "L1")
	if got := c.line(); got != "* ENABLED UTF8=ACCEPT" {
		t.Fatalf("pipelined ENABLE: %q", got)
	}
	requireOK(t, c.line(), "E1")
	for {
		line := c.line()
		if strings.HasPrefix(line, "S1 ") {
			requireOK(t, line, "S1")
			break
		}
		if !strings.HasPrefix(line, "* ") {
			t.Fatalf("pipelined SELECT: %q", line)
		}
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if box.enables != 1 {
		t.Fatalf("backend enable calls=%d, want1", box.enables)
	}
	_, tagged := c.command("Q1", "SEARCH CHARSET UTF-8 ALL")
	if !strings.HasPrefix(tagged, "Q1 BAD ") {
		t.Fatalf("enabled mode lost after pipelined SELECT: %q", tagged)
	}
}

// RFC7888 section3's two-literal LOGIN example must be one command, without
// either continuation. Its counted spaces and following command stay intact.
// checklist: IMAP-LITMINUS-05
func TestLiteralLoginRFC7888Example(t *testing.T) {
	b := &acceptingCredentials{}
	c := securityClient(t, b, imap.NopLimiter{})
	wrote := make(chan error, 1)
	go func() {
		_, err := io.WriteString(c.conn, "A001 LOGIN {11+}\r\nFRED FOOBAR {7+}\r\nfat man\r\nN1 NOOP\r\n")
		wrote <- err
	}()
	requireOK(t, c.line(), "A001")
	requireOK(t, c.line(), "N1")
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if b.calls != 1 || b.user != "FRED FOOBAR" || b.pass != "fat man" {
		t.Fatalf("RFC literal octets changed: %+v", b)
	}
}

// RFC4959 section3 requires the base64 alphabet, with padding only at the
// end. Go's general base64 decoder tolerates CR/LF; SASL wire tokens cannot.
// checklist: IMAP-SASLIR-06
func TestPlainBase64Alphabet(t *testing.T) {
	valid := plainResponse("alice", "secret")
	for _, initial := range []bool{false, true} {
		for _, encoded := range []string{valid[:4] + "\r" + valid[4:], valid[:4] + "\t" + valid[4:], "=AAA", "AAA=BBB", valid + "!"} {
			t.Run(fmt.Sprintf("initial=%t/encoded=%q", initial, encoded), func(t *testing.T) {
				b := &acceptingCredentials{}
				c := securityClient(t, b, imap.NopLimiter{})
				var tagged string
				if initial {
					lines, result := c.command("A1", "AUTHENTICATE PLAIN %s", encoded)
					requireExactly(t, lines)
					tagged = result
				} else {
					tagged = c.authenticate("A1", "PLAIN", encoded)
				}
				if !strings.HasPrefix(tagged, "A1 NO ") || b.calls != 0 {
					t.Fatalf("invalid base64: reply=%q backend calls=%d", tagged, b.calls)
				}
				_, retry := c.command("A2", "AUTHENTICATE PLAIN %s", valid)
				requireOK(t, retry, "A2")
			})
		}
	}
}

// RFC2177 section3 permits mailbox events while IDLE owns the continuation.
// Follow its SELECT/new-mail/expunge example, also checking a flag update and
// that the queued next command receives no continuation or stale events.
// checklist: IMAP-IDLE-03
func TestIdleMailboxNotificationTranscript(t *testing.T) {
	box := &sharedMailbox{messages: []imap.Message{{UID: 1}, {UID: 2}, {UID: 3}}}
	c := dialConfigured(t, box, nil, nil, func(s *server.Session) { s.SetIdlePollInterval(10 * time.Millisecond) })
	c.line()
	c.login()
	_, tagged := c.command("A001", "SELECT INBOX")
	requireOK(t, tagged, "A001")
	if _, err := io.WriteString(c.conn, "A002 IDLE\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := c.line(); got != "+ idling" {
		t.Fatalf("IDLE continuation: %q", got)
	}
	box.deliver(4)
	if got := c.line(); got != "* 4 EXISTS" {
		t.Fatalf("new-mail notification: %q", got)
	}
	box.remove(2)
	if got := c.line(); got != "* 2 EXPUNGE" {
		t.Fatalf("expunge notification: %q", got)
	}
	box.set(3, func(msg *imap.Message) { msg.Seen = true })
	if got := c.line(); got != `* 2 FETCH (FLAGS (\Seen))` {
		t.Fatalf("renumbered flag notification: %q", got)
	}
	wrote := make(chan error, 1)
	go func() { _, err := io.WriteString(c.conn, "DONE\r\nA003 NOOP\r\n"); wrote <- err }()
	if got := c.line(); got != "A002 OK IDLE terminated" {
		t.Fatalf("IDLE completion: %q", got)
	}
	if got := c.line(); got != "A003 OK NOOP completed" {
		t.Fatalf("post-IDLE command: %q", got)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
}

// RFC7888 section3 prohibits the non-synchronizing form in server responses.
// Check the actual FETCH framing, independently from payload octet counts.
// checklist: IMAP-LITMINUS-06
func TestServerLiteralResponsesSynchronize(t *testing.T) {
	c := fetchClient(t, newFetchMailbox(), "EXAMINE")
	replies, tagged := fetchCommand(t, c, "FETCH 1 (BODY.PEEK[] BODY.PEEK[HEADER] BODY.PEEK[TEXT])")
	requireOK(t, tagged, "F1")
	if len(replies) != 1 || len(replies[0].literals) != 3 {
		t.Fatalf("FETCH literal responses: %+v", replies)
	}
	if strings.Contains(replies[0].framing, "+}") {
		t.Fatalf("server emitted a non-synchronizing literal: %q", replies[0].framing)
	}
}
