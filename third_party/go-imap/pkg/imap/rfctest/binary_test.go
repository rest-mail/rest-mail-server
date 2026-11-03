package rfctest

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// binaryRaw is a multipart message: part 1 quoted-printable text, part 2
// base64 octets holding a NUL, part 3 7bit text.
var binaryRaw = crlf(`From: a@example.com
Subject: parts
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

caf=C3=A9
--b
Content-Type: application/octet-stream
Content-Transfer-Encoding: base64

` + base64.StdEncoding.EncodeToString([]byte("\x00\x01binary")) + `
--b
Content-Type: text/plain

plain
--b--
`)

func binaryMailbox() *fetchMailbox {
	box := newFetchMailbox()
	box.messages = []imap.Message{{UID: 10}, {UID: 11}, {UID: 12}}
	box.raw = map[uint32]string{
		10: binaryRaw,
		11: crlf("Subject: uu\nContent-Transfer-Encoding: x-uuencode\n\nbegin 644 f\n"),
		12: crlf("Subject: broken\nContent-Transfer-Encoding: base64\n\n!!not base64!!\n"),
	}
	return box
}

// TestBinaryFetch: with BINARY advertised (RFC 3516 §4.1), FETCH BINARY,
// BINARY.PEEK and BINARY.SIZE answer a section with its transfer encoding
// removed (§§4.2-4.3): quoted-printable and base64 decoded, canonical 7bit text,
// BINARY[] the whole message. Data holding a NUL comes as a literal8, other
// data as a literal; a partial counts decoded octets and labels the origin;
// BINARY.SIZE is the decoded length BINARY sends; a part that does not exist
// is empty, as BODY's is.
//
// checklist: IMAP-BINARY-01 IMAP-BINARY-02 IMAP-BINARY-03
// checklist: IMAP2-RESP-201 IMAP2-RESP-204
func TestBinaryFetch(t *testing.T) {
	box := binaryMailbox()
	c := fetchClient(t, box, "SELECT")
	requireCaps(t, c.capabilities("C1"), []string{"BINARY"}, nil)
	for _, tc := range []struct {
		items, framing string
		literals       []string
	}{
		{"BINARY.PEEK[1]", "* 1 FETCH (UID 10 FLAGS () BINARY[1] {5})", []string{"café"}},
		{"BINARY.PEEK[2]", "* 1 FETCH (UID 10 FLAGS () BINARY[2] ~{8})", []string{"\x00\x01binary"}},
		{"BINARY.PEEK[3]", "* 1 FETCH (UID 10 FLAGS () BINARY[3] {5})", []string{"plain"}},
		{"binary.peek[2]<2.3>", "* 1 FETCH (UID 10 FLAGS () BINARY[2]<2> {3})", []string{"bin"}},
		{"(BINARY.SIZE[1] BINARY.SIZE[2] BINARY.SIZE[9])", "* 1 FETCH (UID 10 FLAGS () BINARY.SIZE[1] 5 BINARY.SIZE[2] 8 BINARY.SIZE[9] 0)", nil},
		{"BINARY.PEEK[9]", "* 1 FETCH (UID 10 FLAGS () BINARY[9] {0})", []string{""}},
		{"BINARY.PEEK[]", fmt.Sprintf("* 1 FETCH (UID 10 FLAGS () BINARY[] {%d})", len(binaryRaw)), []string{binaryRaw}},
	} {
		replies, tagged := fetchCommand(t, c, "FETCH 1 "+tc.items)
		requireOK(t, tagged, "F1")
		if len(replies) != 1 || replies[0].framing != tc.framing || fmt.Sprint(replies[0].literals) != fmt.Sprint(tc.literals) {
			t.Fatalf("FETCH 1 %s: %+v, want %q %q", tc.items, replies, tc.framing, tc.literals)
		}
	}
	if len(box.updates[10]) != 0 {
		t.Fatalf("BINARY.PEEK and BINARY.SIZE set flags: %+v", box.updates[10])
	}
}

// TestBinarySeen: BINARY, like BODY, sets \Seen and reports it in the same
// FETCH; BINARY.PEEK does not (RFC 3516 §4.2).
//
// checklist: IMAP-BINARY-04
func TestBinarySeen(t *testing.T) {
	box := binaryMailbox()
	c := fetchClient(t, box, "SELECT")
	replies, tagged := fetchCommand(t, c, "FETCH 1 BINARY[1]")
	requireOK(t, tagged, "F1")
	if len(replies) != 1 || replies[0].framing != `* 1 FETCH (UID 10 FLAGS (\Seen) BINARY[1] {5})` {
		t.Fatalf("FETCH BINARY[1]: %+v", replies)
	}
	if len(box.updates[10]) != 1 {
		t.Fatalf("BINARY did not store \\Seen: %+v", box.updates[10])
	}
}

// TestBinaryUnknownCTE: a section whose transfer encoding the server cannot
// remove, an unknown one or base64 that does not decode, fails the FETCH with
// NO [UNKNOWN-CTE] and no FETCH response (RFC 3516 §4.3), BINARY.SIZE too.
//
// checklist: IMAP-BINARY-05
// checklist: IMAP2-RESP-203 IMAP2-RESP-205
func TestBinaryUnknownCTE(t *testing.T) {
	c := fetchClient(t, binaryMailbox(), "EXAMINE")
	for _, command := range []string{"FETCH 2 BINARY.PEEK[1]", "FETCH 3 BINARY[1]", "UID FETCH 11 BINARY.SIZE[1]"} {
		replies, tagged := fetchCommand(t, c, command)
		if !strings.HasPrefix(tagged, "F1 NO [UNKNOWN-CTE] ") || len(replies) != 0 {
			t.Fatalf("%s: %+v %q", command, replies, tagged)
		}
	}
}

// TestBinaryGrammar: section-binary is "[" [section-part] "]" (RFC 3516 §7):
// no HEADER, TEXT, MIME or zero part, and BINARY.SIZE takes no partial; each
// is BAD with no response.
//
// checklist: IMAP-BINARY-06
func TestBinaryGrammar(t *testing.T) {
	c := fetchClient(t, binaryMailbox(), "EXAMINE")
	for _, item := range []string{"BINARY[HEADER]", "BINARY[TEXT]", "BINARY[1.MIME]", "BINARY[0]", "BINARY[1.]", "BINARY.SIZE[1]<0.1>", "BINARY[1]<0>", "BINARY.PEEK[1"} {
		replies, tagged := fetchCommand(t, c, "FETCH 1 "+item)
		if !strings.HasPrefix(tagged, "F1 BAD ") || len(replies) != 0 {
			t.Fatalf("%s: %+v %q", item, replies, tagged)
		}
	}
}

// TestBinaryAppend: APPEND's literal8 form may carry binary content (RFC 3516
// §4.4). This store keeps none, so the command fails with NO [UNKNOWN-CTE]
// before a continuation invites the octets, and the session goes on.
//
// checklist: IMAP-BINARY-07
func TestBinaryAppend(t *testing.T) {
	box := &appendMailbox{}
	c := dialMailbox(t, box)
	c.line()
	c.login()
	if _, tagged := c.command("A1", "APPEND INBOX ~{5}"); !strings.HasPrefix(tagged, "A1 NO [UNKNOWN-CTE] ") {
		t.Fatalf("literal8 APPEND: %q", tagged)
	}
	if len(box.records) != 0 {
		t.Fatalf("appended: %+v", box.records)
	}
	if _, tagged := c.command("A2", "NOOP"); !strings.HasPrefix(tagged, "A2 OK") {
		t.Fatalf("after the refusal: %q", tagged)
	}
}
