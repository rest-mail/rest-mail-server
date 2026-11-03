package rfctest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type quotaAuditMailbox struct {
	mailbox
	used, limit          int64
	quotaErr, foldersErr error
	quotaCalls           int
}

func (m *quotaAuditMailbox) Folders() ([]imap.Folder, error) {
	if m.foldersErr != nil {
		return nil, m.foldersErr
	}
	return []imap.Folder{{Name: "INBOX"}, {Name: `Box "quoted"`}}, nil
}

func (m *quotaAuditMailbox) Quota() (int64, int64, error) {
	m.quotaCalls++
	return m.used, m.limit, m.quotaErr
}

// TestQuotaArguments audits RFC 2087 sections 4.2, 4.3 and 6: a command has
// exactly one astring, and a syntactically valid unknown root is NO. Neither
// malformed input nor an unknown root may reach the account quota backend.
// checklist: IMAP-QUOTA-02 IMAP-QUOTA-03
func TestQuotaArguments(t *testing.T) {
	for _, tc := range []struct{ command, status string }{
		{`GETQUOTA`, "BAD"}, {`GETQUOTA "" extra`, "BAD"},
		{`GETQUOTA  ""`, "BAD"}, {`GETQUOTA "" `, "BAD"},
		{`GETQUOTA "unterminated`, "BAD"}, {`GETQUOTA "bad\q"`, "BAD"},
		{`GETQUOTA other`, "NO"}, {`GETQUOTA "other"`, "NO"},
		{`GETQUOTAROOT`, "BAD"}, {`GETQUOTAROOT ""`, "BAD"},
		{`GETQUOTAROOT INBOX extra`, "BAD"}, {`GETQUOTAROOT "INBOX`, "BAD"},
		{`GETQUOTAROOT "INBOX"x`, "BAD"}, {`GETQUOTAROOT INBOX `, "BAD"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			box := &quotaAuditMailbox{used: 3073, limit: 10249}
			c := dialMailbox(t, box)
			c.line()
			c.login()
			lines, tagged := c.command("Q", "%s", tc.command)
			if len(lines) != 0 || !strings.HasPrefix(tagged, "Q "+tc.status+" ") {
				t.Fatalf("%s: %q / %q; want no data and %s", tc.command, lines, tagged, tc.status)
			}
			if box.quotaCalls != 0 {
				t.Fatalf("refused command read quota %d times", box.quotaCalls)
			}
		})
	}
}

// TestQuotaExchange audits the account's one empty-name root, resource
// triplets in 1024-octet units, astring decoding/encoding and authentication.
// checklist: IMAP-QUOTA-01 IMAP-QUOTA-04 IMAP-QUOTA-05 IMAP-QUOTA-06
func TestQuotaExchange(t *testing.T) {
	box := &quotaAuditMailbox{used: 3073, limit: 10249}
	c := dialMailbox(t, box)
	c.line()
	for _, command := range []string{`GETQUOTA ""`, `GETQUOTAROOT INBOX`} {
		lines, tagged := c.command("U", "%s", command)
		if len(lines) != 0 || !strings.HasPrefix(tagged, "U NO ") {
			t.Fatalf("unauthenticated %s: %q / %q", command, lines, tagged)
		}
	}
	if box.quotaCalls != 0 {
		t.Fatal("unauthenticated quota lookup")
	}
	c.login()
	for _, tc := range []struct {
		command string
		lines   []string
	}{
		{`getquota ""`, []string{`* QUOTA "" (STORAGE 3 10)`}},
		{`getquotaroot inbox`, []string{`* QUOTAROOT "INBOX" ""`, `* QUOTA "" (STORAGE 3 10)`}},
		{`GETQUOTAROOT "Box \"quoted\""`, []string{`* QUOTAROOT "Box \"quoted\"" ""`, `* QUOTA "" (STORAGE 3 10)`}},
	} {
		lines, tagged := c.command("Q", "%s", tc.command)
		requireOK(t, tagged, "Q")
		requireExactly(t, lines, tc.lines...)
	}
	if box.quotaCalls != 3 {
		t.Fatalf("quota calls=%d, want 3", box.quotaCalls)
	}
}

// TestQuotaLookupFailures checks that missing mailboxes and folder/quota
// backend failures produce NO without fabricated QUOTA/QUOTAROOT data.
// checklist: IMAP-QUOTA-03 IMAP-QUOTA-07
func TestQuotaLookupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, command        string
		quotaErr, foldersErr error
		calls                int
	}{
		{"missing mailbox", `GETQUOTAROOT missing`, nil, nil, 0},
		{"folders unavailable", `GETQUOTAROOT INBOX`, nil, errors.New("private folder failure"), 0},
		{"quota unavailable", `GETQUOTA ""`, errors.New("private quota failure"), nil, 1},
		{"root quota unavailable", `GETQUOTAROOT INBOX`, errors.New("private quota failure"), nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := &quotaAuditMailbox{quotaErr: tc.quotaErr, foldersErr: tc.foldersErr}
			c := dialMailbox(t, box)
			c.line()
			c.login()
			lines, tagged := c.command("Q", "%s", tc.command)
			if len(lines) != 0 || !strings.HasPrefix(tagged, "Q NO ") || strings.Contains(tagged, "private") {
				t.Fatalf("%q / %q", lines, tagged)
			}
			if box.quotaCalls != tc.calls {
				t.Fatalf("quota calls=%d, want %d", box.quotaCalls, tc.calls)
			}
		})
	}
}

// TestNamespaceNoArguments checks RFC 2342 section 6's argument-free command
// grammar, including trailing whitespace, without emitting namespace data.
// checklist: IMAP-NAMESPACE-02
func TestNamespaceNoArguments(t *testing.T) {
	c := dial(t)
	c.line()
	c.login()
	for _, command := range []string{"NAMESPACE x", "NAMESPACE ", "NAMESPACE ()"} {
		lines, tagged := c.command("N", "%s", command)
		if len(lines) != 0 || !strings.HasPrefix(tagged, "N BAD ") {
			t.Fatalf("%q: %q / %q", command, lines, tagged)
		}
	}
	lines, tagged := c.command("N", "NAMESPACE")
	requireOK(t, tagged, "N")
	requireExactly(t, lines, `* NAMESPACE (("" "/")) NIL NIL`)
}

// TestQuotaLiteralArguments covers both synchronizing and LITERAL- forms of
// quota roots and mailbox names, including a zero-octet root and quotes.
// checklist: IMAP-QUOTA-02 IMAP-QUOTA-05
func TestQuotaLiteralArguments(t *testing.T) {
	for _, sync := range []bool{false, true} {
		for _, tc := range []struct{ command, value string }{{"GETQUOTA", ""}, {"GETQUOTAROOT", `Box "quoted"`}} {
			t.Run(fmt.Sprintf("%s/sync=%t", tc.command, sync), func(t *testing.T) {
				box := &quotaAuditMailbox{used: 3072, limit: 10240}
				c := dialMailbox(t, box)
				c.line()
				c.login()
				plus := "+"
				if sync {
					plus = ""
				}
				header := fmt.Sprintf("Q %s {%d%s}\r\n", tc.command, len(tc.value), plus)
				suffix := tc.value + "\r\nN NOOP\r\n"
				if sync {
					writeLiteralWire(t, c, header)
					if line := c.line(); !strings.HasPrefix(line, "+ ") {
						t.Fatalf("continuation: %q", line)
					}
					writeLiteralWire(t, c, suffix)
				} else {
					// One writer preserves literal framing on net.Pipe; two concurrent
					// writers could deliver the payload before its command header.
					writeLiteralWire(t, c, header+suffix)
				}
				if tc.command == "GETQUOTAROOT" {
					if line := c.line(); line != `* QUOTAROOT "Box \"quoted\"" ""` {
						t.Fatalf("root: %q", line)
					}
				}
				if line := c.line(); line != `* QUOTA "" (STORAGE 3 10)` {
					t.Fatalf("quota: %q", line)
				}
				requireOK(t, c.line(), "Q")
				requireOK(t, c.line(), "N")
				if box.quotaCalls != 1 {
					t.Fatalf("quota calls=%d", box.quotaCalls)
				}
			})
		}
	}
}

// TestQuotaValueBounds keeps backend data inside the RFC 2087 / RFC 3501
// unsigned-32-bit resource grammar without emitting a partial QUOTAROOT.
// checklist: IMAP-QUOTA-08
func TestQuotaValueBounds(t *testing.T) {
	const maxUnits = int64(1<<32 - 1)
	for _, command := range []string{`GETQUOTA ""`, `GETQUOTAROOT INBOX`} {
		for _, tc := range []struct {
			name        string
			used, limit int64
			valid       bool
		}{
			{"max", maxUnits * 1024, maxUnits * 1024, true},
			{"negative used", -1, 1024, false}, {"negative limit", 1024, -1, false},
			{"used overflow", (maxUnits + 1) * 1024, 1024, false}, {"limit overflow", 1024, (maxUnits + 1) * 1024, false},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				box := &quotaAuditMailbox{used: tc.used, limit: tc.limit}
				c := dialMailbox(t, box)
				c.line()
				c.login()
				lines, tagged := c.command("Q", "%s", command)
				if tc.valid {
					requireOK(t, tagged, "Q")
					requireLine(t, lines, `* QUOTA "" (STORAGE 4294967295 4294967295)`)
				} else if len(lines) != 0 || !strings.HasPrefix(tagged, "Q NO ") {
					t.Fatalf("invalid resource: %q / %q", lines, tagged)
				}
			})
		}
	}
}
