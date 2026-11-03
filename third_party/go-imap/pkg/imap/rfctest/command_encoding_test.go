package rfctest

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// requireLiteralSearch sends the controlled, unescaped quoted operands in the
// SEARCH fixtures as synchronizing literals. These CHAR8 operands remain legal
// on a rev1 backend without UTF8=ACCEPT, including multiple operands per query.
func requireLiteralSearch(t *testing.T, c *client, command, want string) {
	t.Helper()
	parts := strings.Split(command, `"`)
	if len(parts)%2 != 1 || len(parts) < 3 {
		t.Fatalf("expected quoted SEARCH operands: %q", command)
	}
	pending := "Q1 " + parts[0]
	for i := 1; i < len(parts); i += 2 {
		if strings.Contains(parts[i], `\`) {
			t.Fatalf("escaped fixture operand unsupported: %q", parts[i])
		}
		if _, err := fmt.Fprintf(c.conn, "%s{%d}\r\n", pending, len(parts[i])); err != nil {
			t.Fatal(err)
		}
		if line := c.line(); !strings.HasPrefix(line, "+ ") {
			t.Fatalf("SEARCH literal continuation: %q", line)
		}
		pending = parts[i] + parts[i+1]
	}
	if _, err := io.WriteString(c.conn, pending+"\r\n"); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for {
		line := c.line()
		if strings.HasPrefix(line, "Q1 ") {
			requireOK(t, line, "Q1")
			break
		}
		lines = append(lines, line)
	}
	requireExactly(t, lines, want)
}

func requireEncodingBAD(t *testing.T, line, tag string) {
	t.Helper()
	if line != tag+" BAD 8-bit text must be sent as a literal" {
		t.Fatalf("encoding refusal: %q", line)
	}
}

func requireEncodingClose(t *testing.T, c *client) {
	t.Helper()
	if line := c.line(); !strings.HasPrefix(line, "* BYE ") {
		t.Fatalf("unread literal closing explanation: %q", line)
	}
	if _, err := c.r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("unread literal stream remains usable: %v", err)
	}
}

// RFC3501 §9 limits typed atoms and quoted strings to 7-bit text. Literal
// octets are exempt; each continuation's original text is checked separately.
// checklist: IMAP-SYNTAX-06
func TestUnsupportedUTF8CommandText(t *testing.T) {
	c := selectionClient(t, &appendMailbox{})
	_, selected := c.command("S1", "SELECT INBOX")
	requireOK(t, selected, "S1")
	for _, command := range []string{
		`SEARCH CHARSET UTF-8 SUBJECT "café"`,
		`UID SEARCH CHARSET UTF-8 TEXT café`,
		`SUBSCRIBE "Grüße"`,
		"SELECT \"\xff\"",
		"LIST \"Grüße\" {1}", // refuse before inviting its literal
	} {
		_, line := c.command("A1", "%s", command)
		requireEncodingBAD(t, line, "A1")
		_, line = c.command("N1", "NOOP")
		requireOK(t, line, "N1")
	}
	// The first operand is a legal literal; the following quoted operand is not.
	sendLiteral(t, c, "A2 LIST ", "", ` "Grüße"`)
	requireEncodingBAD(t, c.line(), "A2")
	_, line := c.command("N2", "NOOP")
	requireOK(t, line, "N2")
	// Valid and malformed 8-bit literal octets both reach normal command handling.
	for _, operand := range []string{"Grüße", "\xff\xfe"} {
		sendLiteral(t, c, "L1 SUBSCRIBE ", operand, "")
		if line := c.line(); strings.HasPrefix(line, "L1 BAD ") {
			t.Fatalf("literal operand refused by typed-text validator: %q", line)
		}
	}
	for _, header := range []string{
		"A3 LIST \"Grüße\" {1+}\r\n",
		"A3 LIST {0+}\r\n \"Grüße\" {1+}\r\n",
	} {
		t.Run(header, func(t *testing.T) {
			c := selectionClient(t, &appendMailbox{})
			writeLiteralWire(t, c, header+"x\r\nN1 NOOP\r\n")
			requireEncodingBAD(t, c.line(), "A3")
			requireEncodingClose(t, c)
		})
	}
}

// APPEND has its own literal reader. Refuse invalid initial and continuation
// text before storing a message, while retaining ordinary 8-bit message bodies.
// checklist: IMAP-SYNTAX-06
func TestUnsupportedUTF8AppendText(t *testing.T) {
	for _, mailboxLiteral := range []bool{false, true} {
		for _, nonSync := range []bool{false, true} {
			t.Run(fmt.Sprintf("mailboxLiteral=%t/nonSync=%t", mailboxLiteral, nonSync), func(t *testing.T) {
				box := &appendMailbox{}
				c := selectionClient(t, box)
				marker := "{1}"
				if nonSync {
					marker = "{1+}"
				}
				line := "A1 APPEND \"Grüße\" " + marker + "\r\n"
				if mailboxLiteral {
					if _, err := io.WriteString(c.conn, "A1 APPEND {5}\r\n"); err != nil {
						t.Fatal(err)
					}
					if line := c.line(); !strings.HasPrefix(line, "+ ") {
						t.Fatalf("mailbox continuation: %q", line)
					}
					line = "INBOX (\\Fläg) " + marker + "\r\n"
				}
				if nonSync {
					writeLiteralWire(t, c, line+"x\r\nN1 NOOP\r\n")
				} else if _, err := io.WriteString(c.conn, line); err != nil {
					t.Fatal(err)
				}
				requireEncodingBAD(t, c.line(), "A1")
				if nonSync {
					requireEncodingClose(t, c)
				} else {
					_, line := c.command("N1", "NOOP")
					requireOK(t, line, "N1")
				}
				if len(box.records) != 0 {
					t.Fatalf("refused text reached APPEND backend: %+v", box.records)
				}
			})
		}
	}
	box := &appendMailbox{}
	c := selectionClient(t, box)
	raw := "Subject: historical\r\n\r\nGrüße\r\n"
	sendLiteral(t, c, "A2 APPEND INBOX ", raw, "")
	requireOK(t, c.line(), "A2")
	if len(box.records) != 1 || box.records[0].raw != raw {
		t.Fatalf("8-bit literal message changed: %+v", box.records)
	}
}

type utf8CredentialBackend struct{ calls int }

func (b *utf8CredentialBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	b.calls++
	if user != "é@example.test" || pass != "sëcret" {
		return nil, errors.New("invalid credentials")
	}
	return &utf8Mailbox{}, nil
}

// RFC6855 §5 does not extend LOGIN to UTF-8 quoted credentials. SASL PLAIN
// carries those credentials in base64 without introducing 8-bit command text.
// checklist: IMAP-SYNTAX-07
func TestUTF8CredentialsRequireAuthenticate(t *testing.T) {
	b := &utf8CredentialBackend{}
	c := securityClient(t, b, imap.NopLimiter{})
	for _, args := range []string{`"é@example.test" ascii`, `ascii "sëcret"`, `é@example.test sëcret`, `"é@example.test" {1}`} {
		_, line := c.command("L1", "LOGIN %s", args)
		requireEncodingBAD(t, line, "L1")
	}
	sendLiteral(t, c, "L2 LOGIN ", "é@example.test", ` "sëcret"`)
	requireEncodingBAD(t, c.line(), "L2")
	if b.calls != 0 {
		t.Fatalf("invalid LOGIN reached authentication backend %d times", b.calls)
	}
	for _, header := range []string{
		"L3 LOGIN \"é@example.test\" {1+}\r\n",
		fmt.Sprintf("L3 LOGIN {%d+}\r\n%s \"sëcret\" {1+}\r\n", len("é@example.test"), "é@example.test"),
	} {
		t.Run(header, func(t *testing.T) {
			b := &utf8CredentialBackend{}
			c := securityClient(t, b, imap.NopLimiter{})
			writeLiteralWire(t, c, header+"x\r\nN1 NOOP\r\n")
			requireEncodingBAD(t, c.line(), "L3")
			requireEncodingClose(t, c)
			if b.calls != 0 {
				t.Fatalf("unread literal reached authentication backend %d times", b.calls)
			}
		})
	}
	requireOK(t, c.authenticate("A1", "PLAIN", plainResponse("é@example.test", "sëcret")), "A1")
	if b.calls != 1 {
		t.Fatalf("AUTHENTICATE backend calls=%d, want1", b.calls)
	}
}
