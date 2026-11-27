package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/ianaindex"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// malformedParameterRow is one spelling of a Content-Type or Content-Disposition that its source
// issue found read differently somewhere, and the one reading every reader has of it now.
type malformedParameterRow struct {
	source, name string
	// top is the message's own Content-Type with its delimiter, when the spelling is in that field, and
	// fields are the second part's header lines.
	top, delimiter, fields string
	// What every reader agrees the second part is: its media type, whether it is an attachment, and its
	// name, which is "" for a part with none.
	mediaType string
	attached  bool
	filename  string
	// structure is that part in a legacy session's BODYSTRUCTURE, exactly.
	structure string
	// decision names the checklist rows whose RFC 2045 §5.2 default this row pins. Reading the spelling
	// some other way is an owner decision on those rows (#1897 item 2 and 5), not a fault.
	decision string
}

// malformedParameterCorpus is every malformed parameter spelling in #1886, #1890, #1893, #1894 and
// #1897, as the second of three parts, plus the Content-Disposition forms of the same faults. #1968's
// spellings are rows of TestFieldFaultsReadTheSameEverywhere.
func malformedParameterCorpus() []malformedParameterRow {
	const pdf = "Content-Type: application/pdf"
	const asPDF = `("APPLICATION" "PDF" `
	const textPart = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 6 1 NIL NIL NIL NIL)`
	const sevenBitDefault = "IMAP-BS-21, IMAP-BS-23"
	part := func(source, name, fields, mediaType string, attached bool, filename, structure, decision string) malformedParameterRow {
		return malformedParameterRow{source: source, name: name, fields: fields, mediaType: mediaType, attached: attached, filename: filename, structure: structure, decision: decision}
	}
	disposed := func(source, name, disposition string, attached bool, filename, structure, decision string) malformedParameterRow {
		return part(source, name, pdf+"\r\nContent-Disposition: "+disposition, "application/pdf", attached, filename, structure, decision)
	}
	topLevel := func(source, name, top, delimiter string) malformedParameterRow {
		row := part(source, name, pdf+"; name=a.pdf", "application/pdf", true, "a.pdf", asPDF+`("NAME" "a.pdf") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, "")
		row.top, row.delimiter = top, delimiter
		return row
	}
	return []malformedParameterRow{
		part("#1890", "an unquoted 8-bit name", pdf+"; name=Jörg.pdf", "application/pdf", true, "Jörg.pdf",
			asPDF+`("NAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1890", "an RFC 2231 name beside an unquoted 8-bit value", pdf+"; name*=windows-1252''x%F6.pdf; note=Grü", "application/pdf", true, "xö.pdf",
			asPDF+`("NAME" "=?windows-1252?Q?x=F6=2Epdf?=" "NOTE" "=?UTF-8?Q?Gr=C3=BC?=") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1886", "RFC 2231 names in Windows-1252 and Latin-2",
			"Content-Type: application/octet-stream; name*=windows-1252''J%F6rg.txt; other=keep\r\nContent-Disposition: attachment; filename*=iso-8859-2''%A3.txt",
			"application/octet-stream", true, "Ł.txt",
			`("APPLICATION" "OCTET-STREAM" ("NAME" "=?windows-1252?Q?J=F6rg=2Etxt?=" "OTHER" "keep") NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME" "=?iso-8859-2?Q?=A3=2Etxt?=")) NIL NIL)`, ""),
		part("#1894", "a comment in an 8-bit bare name", pdf+"; name=Jörg (1).pdf", "application/pdf", true, "Jörg (1).pdf",
			asPDF+`("NAME" "=?UTF-8?Q?J=C3=B6rg_=281=29=2Epdf?=") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1894", "an unbalanced quote in an 8-bit bare name", pdf+"; name=Jö\"rg.pdf", "application/pdf", true, "Jö\"rg.pdf",
			asPDF+`("NAME" "=?UTF-8?Q?J=C3=B6=22rg=2Epdf?=") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1894", "an 8-bit parameter name", pdf+"; näme=x", "application/pdf", false, "",
			asPDF+`("N%C3%A4ME" "x") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1897", "an unbalanced quote in a 7-bit bare name", pdf+"; name=a\"b; other=keep", "text/plain", false, "", textPart, sevenBitDefault),
		part("#1897", "a comment in a 7-bit bare name", pdf+"; name=Joe (1).pdf; other=keep", "text/plain", false, "", textPart, sevenBitDefault),
		part("#1897", "a space in a 7-bit bare name", pdf+"; name=Joe Smith.pdf; other=keep", "text/plain", false, "", textPart, sevenBitDefault),
		part("#1897", "a repeated name, which the grammar allows", pdf+"; a=1; a=2", "application/pdf", false, "",
			asPDF+`("A" "1") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1897", "an 8-bit continuation with no first segment", pdf+"; name*1=Jö; other=keep", "application/pdf", true, "Jö",
			asPDF+`("NAME" "=?UTF-8?Q?J=C3=B6?=" "OTHER" "keep") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1897", "a 7-bit continuation with no first segment", pdf+"; name*1=x", "application/pdf", true, "x",
			asPDF+`("NAME" "x") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1897", "an 8-bit media type", "Content-Type: applö/pdf; other=keep", "text/plain", false, "", textPart, "IMAP-BS-21"),
		part("#1897", "a gap between segments", pdf+"; name*0=a; name*2=c", "application/pdf", true, "ac",
			asPDF+`("NAME" "ac") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		part("#1897", "a segment that isn't a number", pdf+"; name*x=y; other=keep", "application/pdf", false, "",
			asPDF+`("NAME*X" "y" "OTHER" "keep") NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
		topLevel("#1893", "a quoted 8-bit boundary", "Content-Type: multipart/mixed; boundary=\"Jö\"", "Jö"),
		topLevel("#1893", "a bare 8-bit boundary", "Content-Type: multipart/mixed; boundary=Jö", "Jö"),
		// The same faults in a Content-Disposition. A 7-bit one that doesn't read is NIL (IMAP-CDP-03), so
		// the part is the type's and has no name anywhere.
		disposed("#1897", "an unbalanced quote in a 7-bit bare filename", "attachment; filename=a\"b; other=keep", false, "",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, "IMAP-CDP-03"),
		disposed("#1897", "a comment in a 7-bit bare filename", "attachment; filename=Joe (1).pdf; other=keep", false, "",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, "IMAP-CDP-03"),
		disposed("#1897", "a space in a 7-bit bare filename", "attachment; filename=Joe Smith.pdf; other=keep", false, "",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, "IMAP-CDP-03"),
		disposed("#1890", "an unquoted 8-bit filename", "attachment; filename=Jörg.pdf", true, "Jörg.pdf",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=")) NIL NIL)`, ""),
		disposed("#1894", "a comment in an 8-bit bare filename", "attachment; filename=Jörg (1).pdf", true, "Jörg (1).pdf",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6rg_=281=29=2Epdf?=")) NIL NIL)`, ""),
		disposed("#1894", "an unbalanced quote in an 8-bit bare filename", "attachment; filename=Jö\"rg.pdf", true, "Jö\"rg.pdf",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6=22rg=2Epdf?=")) NIL NIL)`, ""),
		disposed("#1894", "an 8-bit parameter name beside a filename", "attachment; fïle=x; filename=a.pdf", true, "a.pdf",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("F%C3%AFLE" "x" "FILENAME" "a.pdf")) NIL NIL)`, ""),
		disposed("#1897", "a filename continuation with no first segment", "attachment; filename*1=Jö; other=keep", true, "Jö",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6?=" "OTHER" "keep")) NIL NIL)`, ""),
		disposed("#1897", "a gap between filename segments", "attachment; filename*0=a; filename*2=c", true, "ac",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME" "ac")) NIL NIL)`, ""),
		disposed("#1897", "a filename segment that isn't a number", "attachment; filename*x=y; other=keep", true, "",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME*X" "y" "OTHER" "keep")) NIL NIL)`, ""),
		disposed("#1897", "a repeated filename", "attachment; filename=a.pdf; filename=b.pdf", true, "a.pdf",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL ("ATTACHMENT" ("FILENAME" "a.pdf")) NIL NIL)`, ""),
		disposed("#1897", "an 8-bit disposition type", "attachmént; filename=a.pdf", false, "",
			asPDF+`NIL NIL NIL "7BIT" 6 NIL NIL NIL NIL)`, ""),
	}
}

// message is the row's spelling as the second of three parts, the first and last plain text. content
// is the second part's body, which tells a row's stored part from another's.
func (row malformedParameterRow) message(content string) string {
	top, delimiter := row.top, row.delimiter
	if top == "" {
		top, delimiter = "Content-Type: multipart/mixed; boundary=a", "a"
	}
	return repeatedHead + top + "\r\n\r\n" +
		"--" + delimiter + "\r\nContent-Type: text/plain\r\n\r\nvisible text\r\n" +
		"--" + delimiter + "\r\n" + row.fields + "\r\n\r\n" + content + "\r\n" +
		"--" + delimiter + "\r\nContent-Type: text/plain\r\n\r\nafter the part\r\n--" + delimiter + "--\r\n"
}

// long is the row's message with the field that holds the fault repeated to about size: its
// parameters, each on a line of its own, again and again.
func (row malformedParameterRow) long(content string, size int) string {
	grow := func(field string) string {
		at := strings.Index(field, ";")
		head, parameters := field[:at], strings.TrimSpace(field[at+1:])
		var b strings.Builder
		b.WriteString(head)
		for b.Len() < size {
			b.WriteString(";\r\n ")
			b.WriteString(parameters)
		}
		return b.String()
	}
	if row.top != "" {
		row.top = grow(row.top)
		return row.message(content)
	}
	lines := strings.Split(row.fields, "\r\n")
	lines[len(lines)-1] = grow(lines[len(lines)-1])
	row.fields = strings.Join(lines, "\r\n")
	return row.message(content)
}

// TestMalformedParametersReadTheSameEverywhere is #2107 AC02's malformed-parameter corpus. Each
// spelling goes through the production delivery route into PostgreSQL, and the stored attachment row,
// the REST API's attachment list, what the filters are handed, and BODYSTRUCTURE in a legacy session and
// in one that enabled UTF8=ACCEPT all read the same media type and the same name. BODYSTRUCTURE is
// parsed and its words decoded here, with charsets from the IANA registry, so the comparison doesn't
// lean on this server's reader. Rows with a decision name pin RFC 2045 §5.2's default, which the
// checklist requires today; TestMalformedParametersAreBounded holds what each costs.
func TestMalformedParametersReadTheSameEverywhere(t *testing.T) {
	x := newRFC2231Fixture(t)
	utf8 := openIMAPSession(t, x.f, x.f.gatewayClient(x.srv.URL, imapCN))
	for _, command := range []string{"CLOSE", "ENABLE UTF8=ACCEPT", "SELECT INBOX"} {
		if answer := utf8.do(command); !strings.Contains(answer, " OK") {
			t.Fatalf("%s: %s", command, answer)
		}
	}
	for i, row := range malformedParameterCorpus() {
		t.Run(row.source+" "+row.name, func(t *testing.T) {
			// A stored part is shared by its content, and a shared one keeps its first name.
			content := fmt.Sprintf("part%02d", i)
			stored := x.deliver(t, row.message(content))
			if row.decision != "" {
				t.Logf("pins RFC 2045 §5.2's default, which %s require; reading it otherwise is an owner decision", row.decision)
			}

			want := ""
			if row.attached {
				want = fmt.Sprintf("%q(%s)", row.filename, row.mediaType)
			}
			var rows []models.Attachment
			if err := x.f.db.Where("message_id = ?", stored.ID).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			var stores []string
			for _, attachment := range rows {
				stores = append(stores, fmt.Sprintf("%q(%s)", attachment.Filename, attachment.ContentType))
			}
			if got := strings.Join(stores, " "); got != want {
				t.Errorf("attachment rows %s, want %s", got, want)
			}

			answer := x.f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/attachments", stored.ID), x.f.session, "")
			var listed struct {
				Data []struct {
					Filename    string `json:"filename"`
					ContentType string `json:"content_type"`
				} `json:"data"`
			}
			if err := json.Unmarshal(answer.Body.Bytes(), &listed); err != nil || answer.Code != http.StatusOK {
				t.Fatalf("listAttachments %d %.300s (%v)", answer.Code, answer.Body, err)
			}
			var list []string
			for _, attachment := range listed.Data {
				list = append(list, fmt.Sprintf("%q(%s)", attachment.Filename, attachment.ContentType))
			}
			if got := strings.Join(list, " "); got != want {
				t.Errorf("listAttachments %s, want %s", got, want)
			}

			// An attachment is handed to the filters on its own; any other part stays in the tree.
			tree := `tree=multipart/mixed[text/plain:"visible text" ` + fmt.Sprintf("%s:%q ", row.mediaType, content) + `text/plain:"after the part"]`
			if row.attached {
				tree = `tree=multipart/mixed[text/plain:"visible text" text/plain:"after the part"] attachment=` + want
			}
			if got := x.seen.describe(); got != tree {
				t.Errorf("filters were handed %s, want %s", got, tree)
			}

			x.imap.do("SELECT INBOX")
			utf8.do("SELECT INBOX")
			legacy := x.imap.do("FETCH 1 (BODYSTRUCTURE)")
			if !strings.Contains(legacy, row.structure) {
				t.Errorf("BODYSTRUCTURE %s\n  want %s in it", legacy, row.structure)
			}
			for session, response := range map[string]string{"legacy": legacy, "UTF8=ACCEPT": utf8.do("FETCH 1 (BODYSTRUCTURE)")} {
				second := secondPart(t, response)
				if got := second.mediaType(); got != row.mediaType {
					t.Errorf("%s BODYSTRUCTURE reads the part as %s, the row and filters as %s", session, got, row.mediaType)
				}
				if got := second.name(t); got != row.filename {
					t.Errorf("%s BODYSTRUCTURE names the part %q, the row and filters %q", session, got, row.filename)
				}
			}
			// The part's octets reach a client, whatever its fields say.
			for session, got := range map[string]string{"legacy": x.imap.do("FETCH 1 (BODY.PEEK[2])"), "UTF8=ACCEPT": utf8.do("FETCH 1 (BODY.PEEK[2])")} {
				if !strings.Contains(got, content) {
					t.Errorf("%s BODY[2] %q, want %q in it", session, got, content)
				}
			}
		})
	}
}

// boundedTimes is what the long form of a corpus row may cost, as a multiple of a well-formed field of
// the same size on the same route in the same run, so the ceiling doesn't depend on the platform's
// allocator (#2059). The rows under it measure 1.0x to 2.4x, and up to 3.6x under the race detector,
// whose sync.Pool keeps less.
const boundedTimes = 5

// malformedParametersStillOver are the corpus rows whose long form costs more than boundedTimes, each
// held to the multiple it was measured at, with and without the race detector, rounded up for what a
// run varies by. Each fails once it's clearly back under, at 3x, so that its ceiling goes. They are
// linear, and take well under a second at 1 MiB.
//
//   - An 8-bit boundary has to be respelled in the header and on every delimiter line. Since
//     headerWithBoundary writes the field one parameter at a time (#2330) it costs 3.9x to 6.0x,
//     the top of that under the race detector.
var malformedParametersStillOver = map[string]float64{
	"a quoted 8-bit boundary": 6,
	"a bare 8-bit boundary":   7,
}

// TestMalformedParametersAreBounded stores each spelling of the corpus repeated through one field of
// about 768 KiB, in a message of about 1 MiB, and holds what that costs to boundedTimes, or a row's own
// in malformedParametersStillOver, and to 10 seconds. These are not #1915's 8x of the message, which
// the handlers tests hold on the bare routes: this is the whole router, with the filters and the
// attachment store behind it, measured against itself.
func TestMalformedParametersAreBounded(t *testing.T) {
	const size = 768 << 10
	x := newRFC2231Fixture(t)
	deliver := func(t *testing.T, message string) (uint64, time.Duration) {
		t.Helper()
		if err := x.f.db.Where("mailbox_id = ?", x.f.mailboxID).Delete(&models.Message{}).Error; err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{"address": x.f.address, "sender": "sender@remote.test", "recipients_to": []string{x.f.address},
			"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
		if err != nil {
			t.Fatal(err)
		}
		body := string(payload)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		started := time.Now()
		answer := x.f.do(http.MethodPost, "/api/v1/messages/deliver", "", body)
		took := time.Since(started)
		runtime.ReadMemStats(&after)
		if answer.Code != http.StatusCreated {
			t.Fatalf("refused: %d %.300s", answer.Code, answer.Body)
		}
		lastStored(t, x.f, message)
		return after.TotalAlloc - before.TotalAlloc, took
	}
	// cheapest is the least of three tries: TotalAlloc counts the whole process, and a message that
	// really costs too much costs too much every time.
	cheapest := func(t *testing.T, message string) (float64, time.Duration) {
		best, quickest := uint64(1)<<63, time.Duration(1<<62)
		for range 3 {
			allocated, took := deliver(t, message)
			best, quickest = min(best, allocated), min(quickest, took)
		}
		return float64(best) / float64(len(message)), quickest
	}
	wellFormed := malformedParameterRow{fields: "Content-Type: application/pdf; name=a.pdf; other=keep"}
	controlRatio, controlTook := cheapest(t, wellFormed.long("control", size))
	t.Logf("a well-formed field: %.2fx, %v", controlRatio, controlTook)
	for i, row := range malformedParameterCorpus() {
		t.Run(row.source+" "+row.name, func(t *testing.T) {
			message := row.long(fmt.Sprintf("long%02d", i), size)
			ratio, took := cheapest(t, message)
			times := ratio / controlRatio
			t.Logf("%d bytes: %.2fx the message, %v; %.2fx a well-formed field's %.2fx, %v", len(message), ratio, took, times, controlRatio, controlTook)
			ceiling, over := malformedParametersStillOver[row.name]
			if !over {
				ceiling = boundedTimes
			}
			if times > ceiling {
				t.Errorf("allocated %.2fx what a well-formed field does, over the %gx this row is held to", times, ceiling)
			}
			if over && times <= 3 {
				t.Errorf("allocated %.2fx what a well-formed field does: this row belongs back under %dx", times, boundedTimes)
			}
			if took > 10*time.Second {
				t.Errorf("took %v, over this test's 10s", took)
			}
		})
	}
}

// bodyStructure is a parsed BODYSTRUCTURE, or a piece of one: NIL is nil, a string or number a string,
// and a parenthesized list a []any.
type bodyStructure []any

// secondPart is the second child of the multipart a FETCH response's BODYSTRUCTURE describes.
func secondPart(t *testing.T, response string) bodyStructure {
	t.Helper()
	at := strings.Index(response, "BODYSTRUCTURE ")
	if at < 0 {
		t.Fatalf("no BODYSTRUCTURE in %q", response)
	}
	p := &structureParser{s: response[at+len("BODYSTRUCTURE "):]}
	whole, ok := p.value().([]any)
	if !ok || p.err != nil || len(whole) < 3 {
		t.Fatalf("BODYSTRUCTURE %q: %v", response, p.err)
	}
	second, ok := whole[1].([]any)
	if !ok {
		t.Fatalf("no second part in %q", response)
	}
	return second
}

func (b bodyStructure) mediaType() string {
	kind, _ := b[0].(string)
	sub, _ := b[1].(string)
	return strings.ToLower(kind + "/" + sub)
}

// name is the part's name as a client reads it: the disposition's FILENAME, or else the type's NAME,
// with any encoded word in it decoded.
func (b bodyStructure) name(t *testing.T) string {
	t.Helper()
	parameter := func(list any, key string) (string, bool) {
		pairs, _ := list.([]any)
		for i := 0; i+1 < len(pairs); i += 2 {
			if name, _ := pairs[i].(string); strings.EqualFold(name, key) {
				value, _ := pairs[i+1].(string)
				return value, true
			}
		}
		return "", false
	}
	// A text part has its line count before the extension data, and only a basic part is used here.
	disposition := 8
	if strings.EqualFold(b[0].(string), "TEXT") {
		disposition = 9
	}
	if len(b) > disposition {
		if fields, ok := b[disposition].([]any); ok && len(fields) == 2 {
			if value, ok := parameter(fields[1], "FILENAME"); ok {
				return decodeWords(t, value)
			}
		}
	}
	if value, ok := parameter(b[2], "NAME"); ok {
		return decodeWords(t, value)
	}
	return ""
}

// decodeWords decodes the RFC 2047 words in a value as a client does, with the IANA registry's charsets.
func decodeWords(t *testing.T, value string) string {
	t.Helper()
	decoder := mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		encoding, err := ianaindex.MIME.Encoding(charset)
		if err != nil || encoding == nil {
			return nil, fmt.Errorf("charset %q: %v", charset, err)
		}
		return encoding.NewDecoder().Reader(input), nil
	}}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		t.Fatalf("decode %q: %v", value, err)
	}
	return decoded
}

// structureParser reads the IMAP values a BODYSTRUCTURE is made of (RFC 9051 §4): NIL, numbers,
// quoted strings, literals and parenthesized lists.
type structureParser struct {
	s   string
	err error
}

func (p *structureParser) value() any {
	p.s = strings.TrimLeft(p.s, " ")
	switch {
	case p.err != nil:
		return nil
	case p.s == "":
		p.err = fmt.Errorf("ran out")
		return nil
	case p.s[0] == '(':
		p.s = p.s[1:]
		list := []any{}
		for p.err == nil {
			p.s = strings.TrimLeft(p.s, " ")
			if strings.HasPrefix(p.s, ")") {
				p.s = p.s[1:]
				return list
			}
			list = append(list, p.value())
		}
		return nil
	case p.s[0] == '"':
		var b strings.Builder
		for i := 1; i < len(p.s); i++ {
			switch p.s[i] {
			case '\\':
				i++
				b.WriteByte(p.s[i])
			case '"':
				p.s = p.s[i+1:]
				return b.String()
			default:
				b.WriteByte(p.s[i])
			}
		}
		p.err = fmt.Errorf("unclosed string")
		return nil
	case p.s[0] == '{':
		end := strings.Index(p.s, "}\r\n")
		size, err := strconv.Atoi(strings.TrimSuffix(p.s[1:end], "+"))
		if end < 0 || err != nil || len(p.s) < end+3+size {
			p.err = fmt.Errorf("bad literal at %.20q", p.s)
			return nil
		}
		literal := p.s[end+3 : end+3+size]
		p.s = p.s[end+3+size:]
		return literal
	default:
		end := strings.IndexAny(p.s, " ()")
		if end < 0 {
			end = len(p.s)
		}
		atom := p.s[:end]
		p.s = p.s[end:]
		if atom == "NIL" {
			return nil
		}
		return atom
	}
}
