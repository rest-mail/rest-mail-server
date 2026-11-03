package rfctest

import (
	"encoding/base64"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

func binaryAuditClient(t *testing.T, raw string) (*client, *fetchMailbox) {
	t.Helper()
	box := newFetchMailbox()
	box.raw[10] = raw
	box.messages[0].Size = len(raw)
	return fetchClient(t, box, "EXAMINE"), box
}

// RFC3516 section 6 requires CRLF for textual line-oriented sections. Decode
// first, canonicalize ASCII-compatible text, then count/partial the result.
// BODY[] remains the stored representation and PEEK/SIZE never write flags.
// checklist: IMAP-BINARY-08
func TestBinaryTextCanonicalLineEndings(t *testing.T) {
	body := "one\ntwo\rthree\r\nfour"
	want := "one\r\ntwo\r\nthree\r\nfour"
	for _, tc := range []struct{ name, headers, encoded string }{
		{"default text", "", body},
		{"7bit", "Content-Type: text/plain\r\nContent-Transfer-Encoding: 7bit\r\n", body},
		{"8bit UTF8", "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n", body},
		{"base64", "Content-Type: text/plain; charset=us-ascii\r\nContent-Transfer-Encoding: base64\r\n", base64.StdEncoding.EncodeToString([]byte(body))},
		{"quoted printable", "Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n", "one=0Atwo=0Dthree=0D=0Afour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Subject: unchanged\r\n" + tc.headers + "\r\n" + tc.encoded
			c, box := binaryAuditClient(t, raw)
			replies, tagged := fetchCommand(t, c, "FETCH 1 (BINARY.SIZE[1] BINARY.PEEK[1])")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{want}) || !strings.Contains(replies[0].framing, fmt.Sprintf("BINARY.SIZE[1] %d BINARY[1] {%d}", len(want), len(want))) {
				t.Fatalf("canonical text and size disagree: %+v, want %q", replies, want)
			}
			replies, tagged = fetchCommand(t, c, "UID FETCH 10 BINARY.PEEK[1]<3.4>")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{want[3:7]}) || !strings.Contains(replies[0].framing, "BINARY[1]<3> {4}") {
				t.Fatalf("partial did not count canonical octets: %+v", replies)
			}
			replies, tagged = fetchCommand(t, c, "FETCH 1 BODY.PEEK[]")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{raw}) || len(box.updates[10]) != 0 {
				t.Fatalf("BINARY changed stored bytes or flags: %+v writes=%v", replies, box.updates[10])
			}
		})
	}
}

// NUL-bearing, nontext and unrecognized-charset octets remain opaque. This
// prevents a text-line repair from silently corrupting binary or UTF-16 data.
// checklist: IMAP-BINARY-09
func TestBinaryOpaqueOctetsPreserved(t *testing.T) {
	for _, tc := range []struct{ media, data string }{
		{"application/octet-stream", "a\nb\rc\r\n\x00d"},
		{"text/plain; charset=utf-16le", "\xff\xfea\x00\n\x00b\x00"},
		{"text/plain; charset=unknown-charset", "a\nb\rc"},
		{"text/plain; charset=utf-8", "a\x00\nb\r"},
	} {
		t.Run(tc.media, func(t *testing.T) {
			raw := "Content-Type: " + tc.media + "\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(tc.data))
			c, _ := binaryAuditClient(t, raw)
			replies, tagged := fetchCommand(t, c, "FETCH 1 (BINARY.SIZE[1] BINARY.PEEK[1])")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.data}) || !strings.Contains(replies[0].framing, fmt.Sprintf("BINARY.SIZE[1] %d", len(tc.data))) {
				t.Fatalf("opaque bytes changed: %+v", replies)
			}
			marker := fmt.Sprintf("BINARY[1] {%d}", len(tc.data))
			if strings.ContainsRune(tc.data, 0) {
				marker = fmt.Sprintf("BINARY[1] ~{%d}", len(tc.data))
			}
			if !strings.Contains(replies[0].framing, marker) {
				t.Fatalf("binary domain framing: %+v", replies)
			}
		})
	}
}

// RFC3516 section5 excludes RFC2047 encoded-word headers from CTE decoding.
// The whole-message response must preserve the actual encoded header octets.
// checklist: IMAP-BINARY-10
func TestBinaryEncodedHeadersStayEncoded(t *testing.T) {
	raw := "Subject: =?UTF-8?B?Y2Fmw6k=?=\r\nFrom: =?UTF-8?Q?Ren=C3=A9?= <a@example.test>\r\nContent-Type: text/plain\r\n\r\nbody\r\n"
	c, _ := binaryAuditClient(t, raw)
	replies, tagged := fetchCommand(t, c, "FETCH 1 BINARY.PEEK[]")
	requireOK(t, tagged, "F1")
	if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{raw}) {
		t.Fatalf("encoded message headers changed: %+v", replies)
	}
}

// savedPipelineMailbox supplies real COPYUID metadata instead of the
// UID-expunge-only fixture's deliberately unsupported CopyUID stub.
type savedPipelineMailbox struct {
	*uidExpungeMailbox
	copies []uint32
}

func (m *savedPipelineMailbox) CopyUID(uid uint32, _ string) (uint32, error) {
	m.copies = append(m.copies, uid)
	return 2000 + uid, nil
}

// RFC5182 section2.3's SAVE/COPY/STORE pipeline must run in dependency order.
// UID EXPUNGE then removes only the saved Deleted messages; an unrelated
// Deleted message stays. The fixture proves dispatch/framing, not storage COPY.
// checklist: IMAP-SEARCHRES-07
func TestSearchResPipelineThroughUIDExpunge(t *testing.T) {
	box := &savedPipelineMailbox{uidExpungeMailbox: &uidExpungeMailbox{expungeMailbox: newExpungeMailbox(4)}}
	c := expungeClient(t, box)
	markDeleted(t, c, "1")
	wrote := make(chan error, 1)
	go func() {
		_, err := io.WriteString(c.conn, "P1 SEARCH RETURN (SAVE) UID 110:120\r\nP2 COPY $ Archive\r\nP3 STORE $ +FLAGS.SILENT (\\Deleted)\r\nP4 UID EXPUNGE $\r\n")
		wrote <- err
	}()
	for _, want := range []string{"P1 OK SEARCH completed", "P2 OK [COPYUID 42 110,120 2110,2120] COPY completed", "P3 OK STORE completed", "* 3 EXPUNGE", "* 2 EXPUNGE", "P4 OK UID EXPUNGE completed"} {
		if got := c.line(); got != want {
			t.Fatalf("dependent pipeline: %q, want %q", got, want)
		}
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	requireUIDs(t, box.expungeMailbox, 100, 130)
	if !slices.Equal(box.copies, []uint32{110, 120}) {
		t.Fatalf("copied UIDs=%v", box.copies)
	}
	if !slices.Equal(box.attempts, []uint32{120, 110}) {
		t.Fatalf("expunged UIDs=%v", box.attempts)
	}
	runExactly(t, c, "FETCH $ (UID)")
	runExactly(t, c, "FETCH 1 (UID FLAGS)", `* 1 FETCH (UID 100 FLAGS (\Deleted))`)
}

type moveStoreAudit struct {
	*uidMoveMailbox
	stores []imap.FlagUpdate
}

func (m *moveStoreAudit) Store(_ uint32, update imap.FlagUpdate) error {
	m.stores = append(m.stores, update)
	return nil
}

// RFC6851 section3.3 forbids synthesizing STORE/Deleted as a MOVE side effect.
// UIDPLUS mapping must still precede EXPUNGE, with unrelated marks preserved.
// checklist: IMAP-MOVE-15
func TestMoveDoesNotSynthesizeStore(t *testing.T) {
	m := &moveStoreAudit{uidMoveMailbox: &uidMoveMailbox{moveMailbox: newMoveMailbox(), validity: 38505}}
	c := expungeClient(t, m)
	markDeleted(t, c, "1")
	m.stores = nil
	lines, tagged := c.command("M1", "UID MOVE 304:319 Archive")
	requireOK(t, tagged, "M1")
	requireExactly(t, lines, "* OK [COPYUID 38505 304,319 1202,1203] Moved", "* 3 EXPUNGE", "* 2 EXPUNGE")
	if len(m.stores) != 0 {
		t.Fatalf("MOVE synthesized STORE: %+v", m.stores)
	}
	runExactly(t, c, "FETCH 1 (UID FLAGS)", `* 1 FETCH (UID 300 FLAGS (\Deleted))`)
}

type utf8NamesAudit struct{ utf8Mailbox }

func (*utf8NamesAudit) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}, {Name: "Grüße/日本語"}}, nil
}

// RFC6855 section3 prohibits UTF8 mailbox output before ENABLE, then permits
// it after opt-in. The raw storage name stays the same in both sessions.
// checklist: IMAP-UTF8-06
func TestUTF8MailboxOutputRequiresEnable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			m := &utf8NamesAudit{}
			c := selectionClient(t, m)
			requireCaps(t, c.capabilities("C1"), []string{"UTF8=ACCEPT"}, []string{"UTF8=ONLY", "UTF8=ALL", "UTF8=APPEND", "UTF8=USER"})
			if enabled {
				enableUTF8(t, c)
			}
			name := "Gr&APwA3w-e/&ZeVnLIqe-"
			if enabled {
				name = "Grüße/日本語"
			}
			for _, cmd := range []string{"LIST", "LSUB"} {
				if _, err := fmt.Fprintf(c.conn, "L1 %s \"\" \"*\"\r\n", cmd); err != nil {
					t.Fatal(err)
				}
				if got := c.line(); got != `* `+cmd+` () "/" "INBOX"` {
					t.Fatalf("INBOX listing: %q", got)
				}
				line := c.line()
				if enabled {
					// EncodeMailboxName legally uses a counted literal for UTF8.
					if line != fmt.Sprintf(`* %s () "/" {%d}`, cmd, len(name)) {
						t.Fatalf("UTF8 literal marker: %q", line)
					}
					payload := make([]byte, len(name))
					if _, err := io.ReadFull(c.r, payload); err != nil {
						t.Fatal(err)
					}
					if string(payload) != name || c.line() != "" {
						t.Fatalf("UTF8 literal payload: %q", payload)
					}
				} else if line != `* `+cmd+` () "/" "`+name+`"` {
					t.Fatalf("legacy mailbox name: %q", line)
				}
				requireOK(t, c.line(), "L1")
			}
		})
	}
}

// RFC3516 section 6 does not insert a final line ending or damage an existing
// CRLF; decoded UTF8 is counted in octets, including at partial boundaries.
// checklist: IMAP-BINARY-08
func TestBinaryCanonicalTextBoundaries(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", ""}, {"plain", "plain"}, {"\r", "\r\n"}, {"\n", "\r\n"},
		{"\r\n", "\r\n"}, {"\r\r\n", "\r\n\r\n"}, {"a\n\nb", "a\r\n\r\nb"},
		{"café\n日本語", "café\r\n日本語"},
	} {
		t.Run(fmt.Sprintf("%q", tc.raw), func(t *testing.T) {
			raw := "Content-Type: text/plain; charset=utf-8\r\n\r\n" + tc.raw
			c, _ := binaryAuditClient(t, raw)
			replies, tagged := fetchCommand(t, c, "FETCH 1 (BINARY.SIZE[1] BINARY.PEEK[1])")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.want}) || !strings.Contains(replies[0].framing, fmt.Sprintf("BINARY.SIZE[1] %d BINARY[1] {%d}", len(tc.want), len(tc.want))) {
				t.Fatalf("canonical boundary: %+v, want %q", replies, tc.want)
			}
		})
	}
}

// RFC5182 section 2.1 preserves SAVE state on a NO search without SAVE. The
// printed example 5 omits SAVE but describes clearing; the normative rule wins.
// checklist: IMAP-SEARCHRES-10
func TestSearchResNonSaveFailurePreservesVariable(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	runExactly(t, c, "SEARCH RETURN (SAVE) FLAGGED")
	_, tagged := c.command("N1", "SEARCH CHARSET KOI8-R ALL")
	if !strings.HasPrefix(tagged, "N1 NO [BADCHARSET") {
		t.Fatalf("non-SAVE refusal: %q", tagged)
	}
	runExactly(t, c, "FETCH $ (UID)", `* 2 FETCH (UID 8 FLAGS (\Seen \Flagged))`, `* 10 FETCH (UID 70 FLAGS (\Flagged))`, `* 11 FETCH (UID 3800 FLAGS (\Flagged))`)
	_, tagged = c.command("N2", "SEARCH RETURN (SAVE) CHARSET KOI8-R ALL")
	if !strings.HasPrefix(tagged, "N2 NO [BADCHARSET") {
		t.Fatalf("SAVE refusal: %q", tagged)
	}
	runExactly(t, c, "FETCH $ (UID)")
}
