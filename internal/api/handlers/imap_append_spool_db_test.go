package handlers

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// TestAC01IMAPAppendStreamsToTheRealImport runs APPENDs through a real IMAP session, the real
// gateway and the real import route on PostgreSQL (#2107 AC01): the gateway spools the literal
// and streams it to the import, which spools the request, and the stored source is each
// literal with CRLF line endings and every other octet kept. Neither side leaves a file.
func TestAC01IMAPAppendStreamsToTheRealImport(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	client := newIMAPProofClient(t, imapProofAPI(t, db, account))
	client.ok("ENABLE UTF8=ACCEPT")

	large := "Message-ID: <large@example.test>\nSubject: large\n\n" + strings.Repeat("0123456789abcdefghijklmnopqrstu\n", 20000)
	for _, c := range []struct{ name, literal, want string }{
		{"CRLF", "Message-ID: <crlf@example.test>\r\nSubject: crlf\r\n\r\none\r\ntwo\r\n",
			"Message-ID: <crlf@example.test>\r\nSubject: crlf\r\n\r\none\r\ntwo\r\n"},
		{"bare LF and CR", "Message-ID: <lf@example.test>\nSubject: lf\n\nA\rB\r\r\nC\n",
			"Message-ID: <lf@example.test>\r\nSubject: lf\r\n\r\nA\rB\r\r\nC\r\n"},
		{"8-bit", "Message-ID: <eight@example.test>\r\nSubject: Gr\xc3\xbc\xc3\x9fe\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\ncaf\xe9 \xff\x80\r\n",
			"Message-ID: <eight@example.test>\r\nSubject: Gr\xc3\xbc\xc3\x9fe\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\ncaf\xe9 \xff\x80\r\n"},
		{"several chunks", large, strings.ReplaceAll(large, "\n", "\r\n")},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, uid := client.append("INBOX", "", c.literal)
			var stored models.Message
			if err := db.First(&stored, uid).Error; err != nil {
				t.Fatal(err)
			}
			if got := stored.Source(); !bytes.Equal(got, []byte(c.want)) {
				t.Fatalf("stored %d octets %.120q, want %d octets %.120q", len(got), got, len(c.want), c.want)
			}
			client.ok("NOOP")
			files, err := os.ReadDir(spool)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 0 {
				t.Fatalf("spool left %d file(s): %v", len(files), files)
			}
		})
	}

	// The UTF8 literal8 form of RFC 6855 §4 takes the same path.
	tag := client.nextTag()
	literal := "Message-ID: <utf8@example.test>\nSubject: \xe2\x9c\x93\n\nok\n"
	client.write(fmt.Sprintf("%s APPEND INBOX UTF8 (~{%d}\r\n", tag, len(literal)))
	if line := client.line(); !strings.HasPrefix(line, "+") {
		t.Fatalf("continuation: %q", line)
	}
	client.write(literal + ")\r\n")
	if _, tagged := client.until(tag); !strings.HasPrefix(tagged, tag+" OK") {
		t.Fatalf("UTF8 APPEND: %q", tagged)
	}
	var stored models.Message
	if err := db.Where("mailbox_id = ?", account.ID).Order("id DESC").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if got, want := stored.SourceText(), strings.ReplaceAll(literal, "\n", "\r\n"); got != want {
		t.Fatalf("stored %q, want %q", got, want)
	}
}
