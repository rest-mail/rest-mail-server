package rfctest

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// checklist: IMAP2-RESP-191
func TestESearchDuplicateResultOptions(t *testing.T) {
	c := sharedClient(t, esearchMailbox())
	for _, tc := range []struct{ command, response string }{
		{"SEARCH RETURN (COUNT MIN COUNT ALL ALL MIN) FLAGGED", `* ESEARCH (TAG "R1") MIN 2 ALL 2,10:11 COUNT 3`},
		{"UID SEARCH RETURN (ALL ALL COUNT COUNT) FLAGGED", `* ESEARCH (TAG "R1") UID ALL 8,70,3800 COUNT 3`},
		{"SEARCH RETURN (COUNT COUNT MIN MIN) KEYWORD missing", `* ESEARCH (TAG "R1") COUNT 0`},
	} {
		runExactly(t, c, tc.command, tc.response)
	}
}

// TestNoExpungeWhileLiteralPending changes the backend after a synchronizing
// continuation was requested, but before the client supplied the literal. The
// change is not pushed into that negotiation or SEARCH; NOOP reports it later.
//
// checklist: IMAP2-RESP-199
func TestNoExpungeWhileLiteralPending(t *testing.T) {
	box := &sharedMailbox{messages: []imap.Message{{UID: 1}, {UID: 2}}}
	c := sharedClient(t, box)
	if _, err := fmt.Fprint(c.conn, "Q1 SEARCH TEXT {1}\r\n"); err != nil {
		t.Fatal(err)
	}
	if continuation := c.line(); !strings.HasPrefix(continuation, "+") {
		t.Fatalf("not a continuation: %q", continuation)
	}
	box.remove(1)
	if err := c.conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if octet, err := c.r.ReadByte(); err == nil {
		t.Fatalf("unsolicited byte during literal negotiation: %q", octet)
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("expected an idle transport, got %v", err)
	}
	if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(c.conn, "x\r\n"); err != nil {
		t.Fatal(err)
	}
	for {
		line := c.line()
		if strings.Contains(line, " EXPUNGE") {
			t.Fatalf("EXPUNGE during SEARCH: %q", line)
		}
		if strings.HasPrefix(line, "Q1 ") {
			requireOK(t, line, "Q1")
			break
		}
	}
	lines, completion := c.command("N1", "NOOP")
	requireOK(t, completion, "N1")
	requireExactly(t, lines, "* 1 EXPUNGE")
}

// TestBodyStructureContinuedParameters checks ordered RFC2231 segments and
// extended UTF-8 segments, in both media-type and disposition parameters.
// Literal byte counts are checked by structureResponse's wire reader.
//
// checklist: IMAP2-RESP-210 IMAP2-RESP-211
func TestBodyStructureContinuedParameters(t *testing.T) {
	for _, tc := range []struct {
		segments, value string
		literals        []string
	}{
		{`*0="quarter"; name*1="ly report"`, `"quarterly report"`, nil},
		{`*0*=utf-8''caf; name*1*=%C3%A9`, `{5}`, []string{"café", "café"}},
	} {
		media := "Content-Type: application/octet-stream; name" + tc.segments + "\r\n\r\nx"
		base := `("APPLICATION" "OCTET-STREAM" ("NAME" ` + tc.value + `) NIL NIL "7BIT" 1`
		structureResponse(t, media, base+")", base+" NIL NIL NIL NIL)", tc.literals...)
		segments := strings.ReplaceAll(tc.segments, "name", "filename")
		disposition := "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename" + segments + "\r\n\r\nx"
		base = `("APPLICATION" "OCTET-STREAM" NIL NIL NIL "7BIT" 1`
		var literals []string
		if len(tc.literals) > 0 {
			literals = []string{"café"}
		}
		structureResponse(t, disposition, base+")", base+` NIL ("ATTACHMENT" ("FILENAME" `+tc.value+`)) NIL NIL)`, literals...)
	}
}
