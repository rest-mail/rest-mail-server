package smtp

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

// DeliverMessageFrom reads a streamed delivery back into a request, so a test asserts on the
// fields as the API would decode them.
func (m *mockBackend) DeliverMessageFrom(req *apiclient.DeliverRequest, content apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	return m.DeliverMessage(wholeDelivery(req, content))
}

// wholeDelivery is the request a streamed delivery amounts to.
func wholeDelivery(req *apiclient.DeliverRequest, content apiclient.DeliverContent) *apiclient.DeliverRequest {
	whole := *req
	read := func(open func() io.Reader) []byte {
		if open == nil {
			return nil
		}
		data, err := io.ReadAll(open())
		if err != nil {
			panic(fmt.Sprintf("read a delivery's content: %v", err))
		}
		return data
	}
	whole.RawMessage = read(content.RawMessage)
	whole.BodyText = string(read(content.BodyText))
	whole.BodyHTML = string(read(content.BodyHTML))
	return &whole
}

// parseRawMessage parses data as a session parses the message it has spooled.
func parseRawMessage(data []byte) (subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references string, toList, ccList []string, fromAddr string) {
	parsed, limited := parseForTest(data)
	if limited != nil || parsed == nil {
		return
	}
	defer parsed.remove()
	bodyText, _ = parsed.text.whole()
	bodyHTML, _ = parsed.html.whole()
	return parsed.subject, bodyText, bodyHTML, parsed.messageID, parsed.senderName, parsed.inReplyTo, parsed.references, parsed.toList, parsed.ccList, parsed.fromAddr
}

func parseForTest(data []byte) (*parsedMessage, error) {
	file, err := spool(bytes.NewReader(data))
	if err != nil {
		panic(err)
	}
	defer file.remove()
	return parseSpooled(file)
}

// parseWhole is how the gateway parsed a message it held whole before it spooled DATA (#1437):
// what the file-based parse must keep answering.
func parseWhole(data []byte) (subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references string, toList, ccList []string, fromAddr string, limitErr error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return
	}
	input := data
	if !bytes.Contains(data, []byte("\r\n\r\n")) && !bytes.Contains(data, []byte("\n\n")) {
		input = append(append([]byte{}, data...), "\r\n\r\n"...)
	}
	email, err := rmime.Parse(input)
	if errors.Is(err, rmime.ErrStructureLimit) {
		limitErr = err
		return
	}
	if err != nil || email == nil {
		for _, sep := range []string{"\r\n\r\n", "\n\n"} {
			if _, body, found := strings.Cut(string(data), sep); found {
				bodyText = body
				return
			}
		}
		return
	}
	h := email.Headers
	subject = h.Subject
	messageID = strings.Trim(h.MessageID, "<>")
	inReplyTo = strings.Trim(h.InReplyTo, "<>")
	references = strings.Join(h.References, " ")
	if from, ok := h.From.First(); ok {
		senderName = from.Name
		fromAddr = from.Address
	}
	toList = bareAddresses(h.To.All())
	ccList = bareAddresses(h.Cc.All())
	bodyText, bodyHTML = rmime.TextAndHTML(email.Body)
	return
}

// deeplyNested is a message nested past the MIME limit on depth.
func deeplyNested() string {
	var message strings.Builder
	message.WriteString("From: s@remote.test\r\nContent-Type: multipart/mixed; boundary=b0\r\n\r\n")
	for depth := 0; depth < rmime.DefaultMaxNestingDepth+5; depth++ {
		fmt.Fprintf(&message, "--b%d\r\nContent-Type: multipart/mixed; boundary=b%d\r\n\r\n", depth, depth+1)
	}
	return message.String()
}

// messageCorpus holds messages for the file-based steps to answer as the in-memory ones did.
func messageCorpus() map[string]string {
	nested := deeplyNested()
	manyParts := "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=p\r\n\r\n"
	manyParts += strings.Repeat("--p\r\nContent-Type: text/plain\r\n\r\nx\r\n", 1100) + "--p--\r\n"
	alt := func(plain, html string) string {
		return "--a\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + plain + "\r\n--a\r\nContent-Type: text/html\r\n\r\n" + html + "\r\n--a--\r\n"
	}
	return map[string]string{
		"empty":             "",
		"spaces":            " \r\n\t ",
		"unicode spaces":    "  \u0085",
		"headers only":      "From: A <a@x.test>\r\nSubject: only",
		"headers only LF":   "From: A <a@x.test>\nSubject: only\n",
		"plain":             "From: A <a@x.test>\r\nTo: b@x.test, C <c@x.test>\r\nCc: d@x.test\r\nSubject: hi\r\nMessage-ID: <m@x.test>\r\nIn-Reply-To: <p@x.test>\r\nReferences: <q@x.test> <p@x.test>\r\n\r\nbody\r\n",
		"plain LF":          "From: a@x.test\nSubject: lf\n\nbody\nmore\n",
		"crlf after lf":     "From: a@x.test\n\nfirst\r\n\r\nsecond",
		"no header at all":  "\r\nbody after a blank first line\r\n",
		"encoded subject":   "From: =?UTF-8?B?w6k=?= <a@x.test>\r\nSubject: =?UTF-8?Q?caf=C3=A9?=\r\n\r\nb",
		"latin1":            "From: a@x.test\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\ncaf\xe9",
		"unknown charset":   "From: a@x.test\r\nContent-Type: text/plain; charset=x-nope\r\n\r\nraw \xff bytes",
		"non utf8 body":     "From: a@x.test\r\n\r\nA\xe9B\x80\xff\xfe body\r\n",
		"no content type":   "From: a@x.test\r\nSubject: s\r\n\r\ntext",
		"html only":         "From: a@x.test\r\nContent-Type: text/html\r\n\r\n<p>x</p>",
		"bad content type":  "From: a@x.test\r\nContent-Type: text/plain; charset\r\n\r\nbody",
		"upper case type":   "From: a@x.test\r\nContent-Type: TEXT/HTML; charset=utf-8\r\n\r\n<b>x</b>",
		"bad base64 single": "From: a@x.test\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD!!!not base64",
		"bad qp single":     "From: a@x.test\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nabc=ZZdef",
		"unknown encoding":  "From: a@x.test\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: x-nope\r\n\r\nbody",
		"alternative":       "From: a@x.test\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" + alt("plain", "<i>html</i>"),
		"nested": "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" + alt("inner", "<i>inner</i>") +
			"\r\n--m\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD\r\n--m--\r\n",
		"attachment text first":  "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=t.txt\r\n\r\nnot the body\r\n--m\r\nContent-Type: text/plain\r\n\r\nthe body\r\n--m--\r\n",
		"empty first text":       "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nsecond\r\n--m--\r\n",
		"second text ignored":    "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\none\r\n--m\r\nContent-Type: text/plain\r\n\r\ntwo\r\n--m--\r\n",
		"undecodable first text": "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD!!!\r\n--m\r\nContent-Type: text/plain\r\n\r\nsecond\r\n--m--\r\n",
		"inline with id":         "From: a@x.test\r\nContent-Type: multipart/related; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\nContent-Disposition: inline\r\nContent-Id: <i@x>\r\n\r\nnot body\r\n--m\r\nContent-Type: text/plain\r\n\r\nbody\r\n--m--\r\n",
		"calendar":               "From: a@x.test\r\nContent-Type: text/calendar\r\n\r\nBEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
		"truncated multipart":    "From: a@x.test\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nno end",
		"missing boundary":       "From: a@x.test\r\nContent-Type: multipart/mixed\r\n\r\nbody without a boundary",
		"too deep":               nested,
		"too many parts":         manyParts,
		"over-long header":       "Subject: " + strings.Repeat("x", 2<<20) + "\r\n\r\nrecovered body",
		"malformed header":       "not a header line\r\n\r\nrecovered",
		"separator at chunk end": "From: a@x.test\r\nX-Pad: " + strings.Repeat("y", 64<<10-len("From: a@x.test\r\nX-Pad: ")-2) + "\r\n\r\nbody",
		"long body":              "From: a@x.test\r\n\r\n" + strings.Repeat("0123456789", 20000),
		"received":               "Received: from a\r\n  by b\r\nreceived : from c\r\nRECEIVED:d\r\nReceived\r\nReceivedX: e\r\nX: Received: f\r\n\r\nReceived: body\r\n",
		"received no blank":      "Received: a\r\nReceived: b",
		"received cr line":       "Received: a\r\n\r\r\nReceived: b\r\n",
		"received cr in name":    "Received\r: a\r\n\rReceived: b\r\n",
		"received padded":        "Received" + strings.Repeat(" ", 70000) + ": a\r\n\tReceived: b\r\n\r\n",
		"non-ascii header":       "Subject: café\r\n\r\nb",
		"replacement char":       "Subject: �\r\n\r\nb",
		"invalid header":         "Subject: caf\xe9\r\n\r\nb",
		"invalid and non-ascii":  "Subject: café\r\nX: \xff\r\n\r\nb",
		"invalid at end":         "Subject: x\xe2\x82",
		"invalid in body":        "Subject: x\r\n\r\n\xff",
		"blank then invalid LF":  "Subject: x\n\n\xff",
		"bare CR line":           "Subject: x\r\r\n\xff\r\n\r\n",
		"lone CR at end":         "Subject: \xc3\xa9\r",
	}
}

// oldValidateUTF8Headers is validateUTF8Headers as it read a message held whole.
func oldValidateUTF8Headers(data []byte, enabled bool) error {
	headers := data
	for start := 0; start < len(data); {
		nl := bytes.IndexByte(data[start:], '\n')
		if nl < 0 {
			break
		}
		end := start + nl
		if end == start || (end == start+1 && data[start] == '\r') {
			headers = data[:start]
			break
		}
		start = end + 1
	}
	if !utf8.Valid(headers) {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 6, 0}, Message: "Invalid UTF-8 in message headers"}
	}
	if !enabled && containsNonASCII(headers) {
		return &gosmtp.SMTPError{Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 6, 9}, Message: "Internationalized message headers require SMTPUTF8"}
	}
	return nil
}

// TestSpooledStepsAnswerAsTheyDidOnWholeBytes holds every step that now reads the file to what it
// answered when it read the whole message from memory.
func TestSpooledStepsAnswerAsTheyDidOnWholeBytes(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for name, message := range messageCorpus() {
		t.Run(name, func(t *testing.T) {
			data := []byte(message)
			for _, enabled := range []bool{false, true} {
				want, got := oldValidateUTF8Headers(data, enabled), validateUTF8Headers(strings.NewReader(message), enabled)
				if fmt.Sprint(want) != fmt.Sprint(got) {
					t.Errorf("validateUTF8Headers(enabled=%t) = %v, want %v", enabled, got, want)
				}
			}

			count, err := rmime.CountReceived(strings.NewReader(message))
			if err != nil || count != rmime.ReceivedCount(data) {
				t.Errorf("CountReceived = %d, %v, want %d", count, err, rmime.ReceivedCount(data))
			}

			subject, text, html, id, sender, inReplyTo, references, to, cc, from, limit := parseWhole(data)
			parsed, gotLimit := parseForTest(data)
			if (limit != nil) != errors.Is(gotLimit, rmime.ErrStructureLimit) || (limit == nil && gotLimit != nil) {
				t.Fatalf("parse error = %v, want structure limit %v", gotLimit, limit)
			}
			if limit != nil {
				return
			}
			defer parsed.remove()
			gotText, _ := parsed.text.whole()
			gotHTML, _ := parsed.html.whole()
			if gotText != text || gotHTML != html {
				t.Errorf("text, html = %q, %q, want %q, %q", clip(gotText), clip(gotHTML), clip(text), clip(html))
			}
			if got, want := fmt.Sprint(parsed.subject, "|", parsed.messageID, "|", parsed.senderName, "|", parsed.inReplyTo, "|", parsed.references, "|", parsed.fromAddr, "|", parsed.toList, "|", parsed.ccList),
				fmt.Sprint(subject, "|", id, "|", sender, "|", inReplyTo, "|", references, "|", from, "|", to, "|", cc); got != want {
				t.Errorf("headers = %s, want %s", got, want)
			}
		})
	}
	assertSpoolEmpty(t)
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// spoolEntries lists the spool files in the test's temporary directory.
func spoolEntries(t *testing.T) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(os.Getenv("TMPDIR"))
	if err != nil {
		t.Fatalf("read the temporary directory: %v", err)
	}
	var files []os.DirEntry
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "restmail-smtp-") {
			files = append(files, entry)
		}
	}
	return files
}

func assertSpoolEmpty(t *testing.T) {
	t.Helper()
	waitFor(t, "the spool files to be removed", func() bool { return len(spoolEntries(t)) == 0 })
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// observingBackend records whether the message was on disk when it was delivered.
type observingBackend struct {
	*mockBackend
	t          *testing.T
	sawOnDisk  int
	deliveries int
}

func (b *observingBackend) DeliverMessageFrom(req *apiclient.DeliverRequest, content apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	b.deliveries++
	b.sawOnDisk = len(spoolEntries(b.t))
	return b.mockBackend.DeliverMessageFrom(req, content)
}

// transaction starts a delivery to recipient, up to the 354 answer to DATA.
func startData(t *testing.T, h *smtpHarness, from, recipient string) {
	t.Helper()
	h.ehlo()
	if r := h.cmd("MAIL FROM:<%s>", from); !strings.HasPrefix(r, "250") {
		t.Fatalf("MAIL = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s>", recipient); !strings.HasPrefix(r, "250") {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.cmd("DATA"); !strings.HasPrefix(r, "354") {
		t.Fatalf("DATA = %q", r)
	}
}

// sendMessage writes the message, dot-terminated, and answers the reply to it. It does not use
// dataBody, whose lines would all pass through a format string.
func sendMessage(t *testing.T, h *smtpHarness, message string) string {
	t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_ = h.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.WriteString(h.cw, strings.TrimSuffix(message, "\r\n")+"\r\n.\r\n"); err != nil {
		t.Fatalf("write message: %v", err)
	}
	if err := h.cw.Flush(); err != nil {
		t.Fatalf("flush message: %v", err)
	}
	_, final := h.readReply()
	return final
}

// TestDataRemovesItsFilesOnEveryOutcome walks a message to each answer DATA has, and finds nothing
// of it left on disk after any of them.
func TestDataRemovesItsFilesOnEveryOutcome(t *testing.T) {
	const good = "From: s@remote.test\r\nSubject: hi\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
		"--a\r\nContent-Type: text/plain\r\n\r\nplain\r\n--a\r\nContent-Type: text/html\r\n\r\n<b>x</b>\r\n--a--\r\n"
	deep := deeplyNested()
	loop := strings.Repeat("Received: from a by b\r\n", 60) + "From: s@remote.test\r\n\r\nb\r\n"

	for _, tc := range []struct {
		name, message, want string
		arrange             func(*mockBackend, *mockStore)
	}{
		{name: "accepted", message: good, want: "250"},
		{name: "API refuses", message: good, want: "550", arrange: func(b *mockBackend, _ *mockStore) { b.deliverStatus["bob@example.com"] = 404 }},
		{name: "API fails", message: good, want: "451", arrange: func(b *mockBackend, _ *mockStore) { b.deliverFail["bob@example.com"] = true }},
		{name: "invalid header", message: "Subject: \xff\r\n\r\nb\r\n", want: "550"},
		{name: "routing loop", message: loop, want: "554"},
		{name: "structure over limit", message: deep, want: "554"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			back, store := newMockBackend(), newMockStore()
			back.local["bob@example.com"] = true
			if tc.arrange != nil {
				tc.arrange(back, store)
			}
			observed := &observingBackend{mockBackend: back, t: t}
			h := newSMTPHarness(t, back, store, false, func(s *Server) { s.api = observed })
			startData(t, h, "s@remote.test", "bob@example.com")
			if reply := sendMessage(t, h, tc.message); !strings.HasPrefix(reply, tc.want) {
				t.Fatalf("reply = %q, want %s", reply, tc.want)
			}
			assertSpoolEmpty(t)
			if observed.deliveries > 0 && observed.sawOnDisk != 3 {
				t.Errorf("%d files on disk during delivery, want the message and its two text bodies", observed.sawOnDisk)
			}
		})
	}

	t.Run("dropped connection", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		back := newMockBackend()
		back.local["bob@example.com"] = true
		h := newSMTPHarness(t, back, newMockStore(), false)
		startData(t, h, "s@remote.test", "bob@example.com")
		_, _ = io.WriteString(h.cw, "From: s@remote.test\r\n\r\npartial\r\n")
		_ = h.cw.Flush()
		waitFor(t, "the message to reach the disk", func() bool { return len(spoolEntries(t)) == 1 })
		_ = h.conn.Close()
		assertSpoolEmpty(t)
		if got := back.deliveredTo(); len(got) != 0 {
			t.Errorf("delivered %v for a connection dropped during DATA", got)
		}
	})

	t.Run("signed copy of a submission", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		back, store := newMockBackend(), newMockStore()
		back.local["bob@example.com"] = true
		store.authorized["alice@example.com"] = true
		store.identities = map[string]uint{"alice@example.com": back.accountID}
		observed := &observingBackend{mockBackend: back, t: t}
		h := newSMTPHarness(t, back, store, true, func(s *Server) { s.api = observed })
		h.ehlo()
		if r := h.authPlain(back.user, back.pass); !strings.HasPrefix(r, "235") {
			t.Fatalf("AUTH = %q", r)
		}
		startData(t, h, "alice@example.com", "bob@example.com")
		if reply := sendMessage(t, h, "From: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n"); !strings.HasPrefix(reply, "250") {
			t.Fatalf("reply = %q", reply)
		}
		assertSpoolEmpty(t)
		if observed.sawOnDisk != 3 {
			t.Errorf("%d files on disk during delivery, want the message, the signed copy and the text body", observed.sawOnDisk)
		}
		if raw := string(back.lastDeliverReq().RawMessage); !strings.HasPrefix(raw, mockSignature) {
			t.Errorf("delivered raw message does not start with the signature: %q", clip(raw))
		}
	})

	t.Run("refused submission", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		back, store := newMockBackend(), newMockStore()
		back.local["bob@example.com"] = true
		store.prepareErr = errors.New("no key")
		store.identities = map[string]uint{"alice@example.com": back.accountID}
		h := newSMTPHarness(t, back, store, true)
		h.ehlo()
		if r := h.authPlain(back.user, back.pass); !strings.HasPrefix(r, "235") {
			t.Fatalf("AUTH = %q", r)
		}
		startData(t, h, "alice@example.com", "bob@example.com")
		if reply := sendMessage(t, h, "From: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n"); !strings.HasPrefix(reply, "451") {
			t.Fatalf("reply = %q, want 451", reply)
		}
		assertSpoolEmpty(t)
	})

	t.Run("queued for a remote recipient", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		back, store := newMockBackend(), newMockStore()
		store.identities = map[string]uint{"alice@example.com": back.accountID}
		h := newSMTPHarness(t, back, store, true)
		h.ehlo()
		if r := h.authPlain(back.user, back.pass); !strings.HasPrefix(r, "235") {
			t.Fatalf("AUTH = %q", r)
		}
		startData(t, h, "alice@example.com", "remote@other.test")
		if reply := sendMessage(t, h, "From: alice@example.com\r\nSubject: hi\r\n\r\nbody\r\n"); !strings.HasPrefix(reply, "250") {
			t.Fatalf("reply = %q", reply)
		}
		assertSpoolEmpty(t)
		queued := store.queued()
		if len(queued) != 1 || !strings.HasPrefix(queued[0].RawMessage, "Received: ") || !strings.HasSuffix(queued[0].RawMessage, "body\r\n") {
			t.Fatalf("queued = %+v, want the message with its Received field", queued)
		}
		persisted := store.persistedSubmissions()
		if len(persisted) != 1 || persisted[0].RawMessage != queued[0].RawMessage || !strings.HasPrefix(persisted[0].BodyText, "body") {
			t.Fatalf("persisted = %+v", persisted)
		}
	})
}

// TestDataHoldsAnOversizedMessageToTheCapOnDisk sends past the configured cap with the file open,
// and finds the file never held more than the cap, the sender answered as it always was, and
// nothing delivered or left behind.
func TestDataHoldsAnOversizedMessageToTheCapOnDisk(t *testing.T) {
	const limit = 1 << 20
	t.Setenv("TMPDIR", t.TempDir())
	back := newMockBackend()
	back.local["bob@example.com"] = true
	h := newSMTPHarness(t, back, newMockStore(), false, func(s *Server) { s.SetMaxMessageSize(limit) })
	startData(t, h, "s@remote.test", "bob@example.com")
	_ = h.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_ = h.conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	line := strings.Repeat("x", 998) + "\r\n"

	// Half the cap first: the file exists and is being written before anything is refused.
	for written := 0; written < limit/2; written += len(line) {
		if _, err := io.WriteString(h.cw, line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := h.cw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	waitFor(t, "the message to reach the disk", func() bool {
		files := spoolEntries(t)
		if len(files) != 1 {
			return false
		}
		info, err := files[0].Info()
		return err == nil && info.Size() > 0
	})
	name := spoolEntries(t)[0].Name()

	var largest int64
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			if info, err := os.Stat(os.Getenv("TMPDIR") + "/" + name); err == nil && info.Size() > largest {
				largest = info.Size()
			}
			select {
			case <-stop:
				return
			default:
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()
	// The rest from another goroutine: past what it will drain, the server answers and stops
	// reading, and on this synchronous pipe that needs the client free to read the answer.
	go func() {
		_ = h.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		for written := 0; written < 3*limit; written += len(line) {
			if _, err := io.WriteString(h.cw, line); err != nil {
				return
			}
		}
		_, _ = io.WriteString(h.cw, ".\r\n")
		_ = h.cw.Flush()
	}()
	_, final := h.readReply()
	close(stop)
	<-stopped

	if !strings.HasPrefix(final, "552 5.3.4") {
		t.Fatalf("reply = %q, want 552 5.3.4", final)
	}
	if largest > limit {
		t.Errorf("the file held %d bytes, past the cap of %d", largest, limit)
	}
	assertSpoolEmpty(t)
	if got := back.deliveredTo(); len(got) != 0 {
		t.Errorf("delivered %v for an oversized message", got)
	}
}

// failingReader delivers its data and then fails, as a dropped connection does.
type failingReader struct {
	data io.Reader
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	n, err := f.data.Read(p)
	if err == io.EOF {
		return n, f.err
	}
	return n, err
}

// TestSpoolLeavesNoFileWhenTheSourceFails covers the step every outcome starts with.
func TestSpoolLeavesNoFileWhenTheSourceFails(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	boom := errors.New("connection reset")
	if _, err := spool(&failingReader{data: strings.NewReader("partial"), err: boom}); !errors.Is(err, boom) {
		t.Fatalf("spool error = %v, want %v", err, boom)
	}
	assertSpoolEmpty(t)
}

// TestDeliveredFieldsMatchTheMessage proves a local delivery carries the raw octets, with the trace
// field in front, and no decoded text beside them: the server reads that from the source (#1915).
func TestDeliveredFieldsMatchTheMessage(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	back := newMockBackend()
	back.local["bob@example.com"] = true
	h := newSMTPHarness(t, back, newMockStore(), false)
	startData(t, h, "s@remote.test", "bob@example.com")
	raw := "From: S <s@remote.test>\r\nSubject: hi\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte("caf\xe9")) + "\r\n"
	if reply := sendMessage(t, h, raw); !strings.HasPrefix(reply, "250") {
		t.Fatalf("reply = %q", reply)
	}
	req := back.lastDeliverReq()
	if req == nil {
		t.Fatal("nothing delivered")
	}
	scanner := bufio.NewScanner(bytes.NewReader(req.RawMessage))
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "Received: ") {
		t.Fatalf("raw message does not start with the trace field: %q", clip(string(req.RawMessage)))
	}
	if !bytes.HasSuffix(req.RawMessage, []byte(raw)) {
		t.Fatalf("raw message does not end with what was sent: %q", clip(string(req.RawMessage)))
	}
	if req.BodyText != "" || req.BodyHTML != "" {
		t.Errorf("text sent beside the source: %q / %q", req.BodyText, req.BodyHTML)
	}
	if req.Subject != "hi" || req.SenderName != "S" {
		t.Errorf("Subject, SenderName = %q, %q", req.Subject, req.SenderName)
	}
}
