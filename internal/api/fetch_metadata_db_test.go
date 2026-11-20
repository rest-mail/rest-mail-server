package api

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	pop3gw "github.com/rest-mail/rest-mail-server/internal/gateway/pop3"
)

// metadataShapes are messages whose FETCH and RETR forms differ from what is stored, in nested
// entities and around malformed boundaries, so every size, line count and offset a client is told has
// to be measured on what it is sent. Each arrives through a real SMTP session.
// parts are the sections BODYSTRUCTURE must show, so a walk that stops early fails.
type metadataShape struct {
	name, message string
	parts         []string
}

func metadataShapes() []metadataShape {
	head := func(id, extra string) string {
		return "From: =?utf-8?q?J=C3=B6rg?= <sender@remote.test>\r\nTo: chris@example.test\r\nSubject: Grüße\r\n" +
			"Date: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <meta-" + id + "-" + strconv.FormatInt(time.Now().UnixNano(), 36) +
			"@remote.test>\r\nMIME-Version: 1.0\r\n" + extra + "\r\n"
	}
	inner := "From: caf\xe9 <inner@remote.test>\r\nSubject: R\xe9union über\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"Iß\"\r\n\r\npreamble\r\n" +
		"--Iß\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\ncaf\xe9\r\nline two\r\n" +
		"--Iß\r\nContent-Type: application/octet-stream; name=\"bé.bin\"\r\nContent-Transfer-Encoding: binary\r\n\r\n\xff\xfe\x00\x01\r\n\x80\r\n" +
		"--Iß--\r\nepilogue\r\n"
	return []metadataShape{
		{"nested message, 8-bit heads and a binary part", head("nested", "Content-Type: multipart/mixed; boundary=\"B\"\r\n") +
			"--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nGrüße aus Köln\r\n" +
			"--B\r\nContent-Type: message/rfc822\r\n\r\n" + inner + "\r\n--B--\r\n", []string{"1", "2", "2.1", "2.2"}},
		{"a message inside a message", head("deep", "Content-Type: message/rfc822\r\n") +
			"Subject: outer \xe9\r\nContent-Type: message/rfc822\r\n\r\n" + inner, []string{"1", "1.1", "1.1.1", "1.1.2"}},
		{"an 8-bit boundary", head("boundary", "Content-Type: multipart/mixed; boundary=\"Jörg\"\r\n") +
			"--Jörg\r\nContent-Type: text/plain\r\n\r\nx\r\n--Jörg\r\nContent-Type: application/pdf; name=a\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD\r\n--Jörg--\r\n", []string{"1", "2"}},
		{"a multipart never closed, a delimiter with space after it", head("open", "Content-Type: multipart/mixed; boundary=B\r\n") +
			"--B  \r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\nn\xe4\r\n--B\r\nContent-Type: text/html\r\n\r\n<p>\xc3\xa4</p>\r\n", []string{"1", "2"}},
		// RFC 2046 section 5.1.1: what comes before the first delimiter is preamble, so this is a
		// multipart of one empty part.
		{"a boundary the body never uses", head("unused", "Content-Type: multipart/mixed; boundary=\"é\"\r\n") + "only text \xe9\r\n.\r\n..\r\n", []string{"1"}},
	}
}

// Every size, line count and offset FETCH tells a client is the octets it then sends, in a session
// with UTF-8 enabled and one without, where headers and parts are converted on the way out (#2107
// AC06, #1882, #1893, #1897, #1968, #1921). RFC822.SIZE is BODY[]; BODY[HEADER] and BODY[TEXT] make up
// BODY[]; each part's BODYSTRUCTURE size and line count are what BODY[part] sends, and that part's MIME
// header and octets sit in BODY[] as sent; an encapsulated message is its own HEADER and TEXT; and a
// partial fetch is that slice of the whole. POP3's LIST and STAT are the octets RETR sends, and TOP 0
// sends RETR's head.
func TestFetchAndPOP3MetadataMatchTheOctetsSent(t *testing.T) {
	useRealGrants(t)
	s := newEightBitServer(t)
	s.pastGreylist("sender@remote.test")
	password := s.createAppPassword("reader").Password
	for _, shape := range metadataShapes() {
		t.Run(shape.name, func(t *testing.T) {
			s.t = t
			if reply, status, body := s.deliver("sender@remote.test", s.address, shape.message); reply != "250" || len(status) != 1 || status[0] != http.StatusCreated {
				t.Fatalf("SMTP %s, API %v %s", reply, status, body)
			}
			converted := 0
			for _, utf8 := range []bool{false, true} {
				t.Run(fmt.Sprintf("UTF8=%v", utf8), func(t *testing.T) {
					c := newIMAPReader(t, s, password, utf8)
					whole := c.fetchBody("BODY.PEEK[]")
					if !bytes.HasSuffix(s.newest().Source(), []byte(shape.message)) {
						t.Fatal("the stored source isn't the message")
					}
					if string(whole) != string(s.newest().Source()) {
						converted++
					}
					if size := c.fetchNumber("RFC822.SIZE"); size != len(whole) {
						t.Fatalf("RFC822.SIZE %d, BODY[] sent %d octets", size, len(whole))
					}
					header, text := c.fetchBody("BODY.PEEK[HEADER]"), c.fetchBody("BODY.PEEK[TEXT]")
					if string(header)+string(text) != string(whole) {
						t.Fatalf("BODY[HEADER] and BODY[TEXT] aren't BODY[]:\n%q\n%q\n%q", header, text, whole)
					}
					for _, at := range []int{0, len(header) - 3, len(whole) / 2, len(whole) - 9} {
						if got := c.fetchBody(fmt.Sprintf("BODY.PEEK[]<%d.7>", at)); string(got) != string(whole[at:at+7]) {
							t.Fatalf("BODY[]<%d.7> = %q, BODY[] has %q there", at, got, whole[at:at+7])
						}
					}
					structure := c.fetch("BODYSTRUCTURE")
					var parts []string
					walkStructure(t, structure, "", func(path string, part sexp, kind partKind) {
						parts = append(parts, path)
						sent := c.fetchBody("BODY.PEEK[" + path + "]")
						if kind == multipartPart {
							return
						}
						if size := part.number(t, 6); size != len(sent) {
							t.Errorf("part %s: BODYSTRUCTURE size %d, BODY[%s] sent %d octets: %q", path, size, path, len(sent), sent)
						}
						if kind != otherPart {
							if lines, want := part.number(t, linesAt(kind)), countLines(sent); lines != want {
								t.Errorf("part %s: BODYSTRUCTURE lines %d, BODY[%s] has %d: %q", path, lines, path, want, sent)
							}
						}
						if mime := c.fetchBody("BODY.PEEK[" + path + ".MIME]"); !bytes.Contains(whole, append(mime, sent...)) {
							t.Errorf("part %s: its MIME header and octets aren't in BODY[] as sent:\n%q\n%q", path, mime, sent)
						}
						if len(sent) > 6 {
							if got := c.fetchBody("BODY.PEEK[" + path + "]<1.5>"); string(got) != string(sent[1:6]) {
								t.Errorf("BODY[%s]<1.5> = %q, BODY[%s] has %q there", path, got, path, sent[1:6])
							}
						}
						if kind == messagePart {
							inner := string(c.fetchBody("BODY.PEEK["+path+".HEADER]")) + string(c.fetchBody("BODY.PEEK["+path+".TEXT]"))
							if inner != string(sent) {
								t.Errorf("part %s: its HEADER and TEXT aren't the message sent:\n%q\n%q", path, inner, sent)
							}
						}
					})
					if !slices.Equal(parts, shape.parts) {
						t.Fatalf("BODYSTRUCTURE has parts %v, want %v: %+v", parts, shape.parts, structure)
					}
				})
			}
			if converted == 0 {
				t.Fatal("neither session changed the message, so this proves nothing about conversion")
			}

			listed, retrieved, top := s.pop3Measure(password)
			if listed != len(retrieved) {
				t.Fatalf("POP3 LIST %d, RETR sent %d octets", listed, len(retrieved))
			}
			end := bytes.Index(retrieved, []byte("\r\n\r\n"))
			if end < 0 || string(top) != string(retrieved[:end+4]) {
				t.Fatalf("POP3 TOP 0 = %q, RETR's head is %q", top, retrieved[:max(end+4, 0)])
			}
		})
	}
}

// countLines is the number of lines in a body as IMAP counts them for BODYSTRUCTURE: each CRLF ends
// one, and text after the last makes one more.
func countLines(body []byte) int {
	lines := bytes.Count(body, []byte("\r\n"))
	if len(body) > 0 && !bytes.HasSuffix(body, []byte("\r\n")) {
		lines++
	}
	return lines
}

// imapReader is a real IMAP session over the gateway's backend, signed in with INBOX selected and
// its newest message the one each FETCH asks for.
type imapReader struct {
	t      *testing.T
	conn   net.Conn
	r      *bufio.Reader
	n      int
	tagged int
}

func newIMAPReader(t *testing.T, s *eightBitServer, password string, utf8 bool) *imapReader {
	t.Helper()
	client, server := net.Pipe()
	session := imapserver.NewSession(clientAtAddress{Conn: server}, imapgw.NewBackend(s.imapAPI, nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	_ = client.SetDeadline(time.Now().Add(60 * time.Second))
	c := &imapReader{t: t, conn: client, r: bufio.NewReader(client)}
	if _, err := c.r.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	c.command(fmt.Sprintf("LOGIN %q %q", s.address, password))
	if utf8 {
		c.command("ENABLE UTF8=ACCEPT")
	}
	selected := c.command("SELECT INBOX")
	m := regexp.MustCompile(`\* (\d+) EXISTS`).FindStringSubmatch(selected)
	if m == nil {
		t.Fatalf("SELECT said no EXISTS: %q", selected)
	}
	c.n, _ = strconv.Atoi(m[1])
	return c
}

var literalAtEnd = regexp.MustCompile(`\{(\d+)\}\r\n$`)

// command sends line and answers every untagged octet before the tagged OK, literals included.
func (c *imapReader) command(line string) string {
	c.t.Helper()
	c.tagged++
	tag := fmt.Sprintf("m%d", c.tagged)
	if _, err := fmt.Fprintf(c.conn, "%s %s\r\n", tag, line); err != nil {
		c.t.Fatal(err)
	}
	var got strings.Builder
	for {
		text, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("IMAP %s: %v", line, err)
		}
		if strings.HasPrefix(text, tag+" ") {
			if !strings.HasPrefix(text, tag+" OK") {
				c.t.Fatalf("IMAP %s: %s", line, text)
			}
			return got.String()
		}
		got.WriteString(text)
		if m := literalAtEnd.FindStringSubmatch(text); m != nil {
			n, _ := strconv.Atoi(m[1])
			literal := make([]byte, n)
			if _, err := io.ReadFull(c.r, literal); err != nil {
				c.t.Fatal(err)
			}
			got.Write(literal)
		}
	}
}

// fetch answers the value FETCH gives the one item asked for, read from the response.
func (c *imapReader) fetch(item string) sexp {
	c.t.Helper()
	got := c.command(fmt.Sprintf("FETCH %d (%s)", c.n, item))
	prefix := fmt.Sprintf("* %d FETCH ", c.n)
	if !strings.HasPrefix(got, prefix) {
		c.t.Fatalf("FETCH %s: %q", item, got)
	}
	response, _ := parseSexp(c.t, got, len(prefix))
	// The name a body item comes back under drops PEEK, and a partial one keeps only its origin.
	name := strings.Replace(item, ".PEEK", "", 1)
	if i := strings.LastIndex(name, "<"); i >= 0 {
		name = name[:i] + "<" + strings.SplitN(name[i+1:], ".", 2)[0] + ">"
	}
	for i := 0; i+1 < len(response.list); i += 2 {
		if strings.EqualFold(response.list[i].text, name) {
			return response.list[i+1]
		}
	}
	c.t.Fatalf("FETCH %s answered no %s: %q", item, name, got)
	return sexp{}
}

func (c *imapReader) fetchBody(item string) []byte {
	c.t.Helper()
	value := c.fetch(item)
	if value.nil {
		return nil
	}
	return []byte(value.text)
}

func (c *imapReader) fetchNumber(item string) int {
	c.t.Helper()
	n, err := strconv.Atoi(c.fetch(item).text)
	if err != nil {
		c.t.Fatalf("%s: %v", item, err)
	}
	return n
}

// sexp is one value of an IMAP response: a list, or an atom, a quoted string or a literal, whose
// text is what it says.
type sexp struct {
	list   []sexp
	isList bool
	text   string
	nil    bool
}

// parseSexp reads one value of s from i, answering it and where it ends.
func parseSexp(t *testing.T, s string, i int) (sexp, int) {
	t.Helper()
	for i < len(s) && s[i] == ' ' {
		i++
	}
	if i >= len(s) {
		t.Fatalf("response ends early: %q", s)
	}
	switch s[i] {
	case '(':
		value := sexp{isList: true}
		i++
		for {
			for i < len(s) && s[i] == ' ' {
				i++
			}
			if i < len(s) && s[i] == ')' {
				return value, i + 1
			}
			var item sexp
			item, i = parseSexp(t, s, i)
			value.list = append(value.list, item)
		}
	case '"':
		var b strings.Builder
		for i++; i < len(s) && s[i] != '"'; i++ {
			if s[i] == '\\' {
				i++
			}
			b.WriteByte(s[i])
		}
		return sexp{text: b.String()}, i + 1
	case '{', '~':
		if s[i] == '~' {
			i++
		}
		end := strings.Index(s[i:], "}\r\n")
		n, err := strconv.Atoi(s[i+1 : i+end])
		if err != nil {
			t.Fatalf("literal at %d of %q", i, s)
		}
		start := i + end + 3
		return sexp{text: s[start : start+n]}, start + n
	}
	start := i
	depth := 0
	for i < len(s) && (depth > 0 || s[i] != ' ' && s[i] != ')' && s[i] != '(' && s[i] != '\r') {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		}
		i++
	}
	atom := s[start:i]
	return sexp{text: atom, nil: strings.EqualFold(atom, "NIL")}, i
}

func (x sexp) number(t *testing.T, at int) int {
	t.Helper()
	if at >= len(x.list) {
		t.Fatalf("no field %d in %+v", at, x)
	}
	n, err := strconv.Atoi(x.list[at].text)
	if err != nil {
		t.Fatalf("field %d is %q: %v", at, x.list[at].text, err)
	}
	return n
}

type partKind int

const (
	otherPart partKind = iota
	textPart
	messagePart
	multipartPart
)

// linesAt is where BODYSTRUCTURE gives a part's line count (RFC 3501 section 7.4.2).
func linesAt(kind partKind) int {
	if kind == messagePart {
		return 9
	}
	return 7
}

// walkStructure visits each part of a BODYSTRUCTURE by the section number FETCH reads it under, the
// parts of an encapsulated message included.
func walkStructure(t *testing.T, body sexp, path string, visit func(path string, part sexp, kind partKind)) {
	t.Helper()
	if !body.isList || len(body.list) == 0 {
		t.Fatalf("not a body: %+v", body)
	}
	at := func(n int) string {
		if path == "" {
			return strconv.Itoa(n)
		}
		return path + "." + strconv.Itoa(n)
	}
	if body.list[0].isList {
		if path != "" {
			visit(path, body, multipartPart)
		}
		for i, child := range body.list {
			if !child.isList {
				break
			}
			walkStructure(t, child, at(i+1), visit)
		}
		return
	}
	if path == "" {
		path = "1"
	}
	kind := otherPart
	switch media := strings.ToUpper(body.list[0].text + "/" + body.list[1].text); {
	case media == "MESSAGE/RFC822" || media == "MESSAGE/GLOBAL":
		kind = messagePart
	case strings.HasPrefix(media, "TEXT/"):
		kind = textPart
	}
	visit(path, body, kind)
	if kind == messagePart {
		inner := body.list[8]
		if inner.list[0].isList {
			for i, child := range inner.list {
				if !child.isList {
					break
				}
				walkStructure(t, child, path+"."+strconv.Itoa(i+1), visit)
			}
		} else {
			walkStructure(t, inner, path+".1", visit)
		}
	}
}

// pop3Measure signs in over a real POP3 session and answers, for the newest message, the size LIST
// gives it, what RETR sends with its byte-stuffing undone, and what TOP 0 sends. STAT must count what
// LIST does.
func (s *eightBitServer) pop3Measure(password string) (listed int, retrieved, top []byte) {
	s.t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	session := pop3server.NewSession(server, pop3gw.NewBackend(s.pop3API, nil).ForClient(testClientIP), nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	defer func() {
		_ = client.Close()
		<-done
	}()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(client)
	if _, err := reader.ReadString('\n'); err != nil {
		s.t.Fatal(err)
	}
	command := func(line string) string {
		s.t.Helper()
		if _, err := fmt.Fprintf(client, "%s\r\n", line); err != nil {
			s.t.Fatal(err)
		}
		text, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(text, "+OK") {
			s.t.Fatalf("POP3 %s: %q %v", line, text, err)
		}
		return text
	}
	multiline := func() []byte {
		var out []byte
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				s.t.Fatalf("POP3: %v", err)
			}
			if string(line) == ".\r\n" {
				return out
			}
			out = append(out, bytes.TrimPrefix(line, []byte("."))...)
		}
	}
	command("USER " + s.address)
	command("PASS " + password)
	fields := strings.Fields(command("STAT"))
	count, _ := strconv.Atoi(fields[1])
	octets, _ := strconv.Atoi(fields[2])
	command("LIST")
	sum := 0
	for _, line := range strings.Split(strings.TrimSuffix(string(multiline()), "\r\n"), "\r\n") {
		var n, size int
		if _, err := fmt.Sscanf(line, "%d %d", &n, &size); err != nil {
			s.t.Fatalf("LIST line %q: %v", line, err)
		}
		sum += size
		if n == count {
			listed = size
		}
	}
	if sum != octets {
		s.t.Fatalf("STAT counts %d octets, LIST %d", octets, sum)
	}
	command(fmt.Sprintf("RETR %d", count))
	retrieved = multiline()
	command(fmt.Sprintf("TOP %d 0", count))
	top = multiline()
	return listed, retrieved, top
}
