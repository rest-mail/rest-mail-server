package rfctest

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This file is a strict reader of the responses RFC 3501 §9 defines. It reads
// the wire as a client must, taking each literal by its octet count, and
// refuses what the grammar refuses. The tests that use it give a mapped row an
// independent check of the framing and shape of a response. The values come
// back normalized, so a test compares what a string holds and not how it was
// framed. Extension responses are not read: a test using this reader sends only
// RFC 3501 commands.

// wireString is an nstring value: a string, or NIL.
type wireString struct {
	value string
	isNil bool
}

func (s wireString) String() string {
	if s.isNil {
		return "NIL"
	}
	return strconv.Quote(s.value)
}

type wireAddress struct {
	name, route, mailbox, host wireString
}

func (a wireAddress) String() string {
	return fmt.Sprintf("(%s %s %s %s)", a.name, a.route, a.mailbox, a.host)
}

// wireAddresses is an envelope address field; nil is NIL.
type wireAddresses []wireAddress

func (l wireAddresses) String() string {
	if l == nil {
		return "NIL"
	}
	var b strings.Builder
	b.WriteByte('(')
	for _, a := range l {
		b.WriteString(a.String())
	}
	b.WriteByte(')')
	return b.String()
}

type wireEnvelope struct {
	date, subject         wireString
	from, sender, replyTo wireAddresses
	to, cc, bcc           wireAddresses
	inReplyTo, messageID  wireString
}

func (e wireEnvelope) String() string {
	return fmt.Sprintf("(%s %s %s %s %s %s %s %s %s %s)", e.date, e.subject, e.from, e.sender, e.replyTo, e.to, e.cc, e.bcc, e.inReplyTo, e.messageID)
}

// wireParams is a body-fld-param; nil is NIL.
type wireParams [][2]string

func (p wireParams) String() string {
	if p == nil {
		return "NIL"
	}
	parts := make([]string, 0, len(p))
	for _, pair := range p {
		parts = append(parts, strconv.Quote(pair[0])+" "+strconv.Quote(pair[1]))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// wireExtension holds the optional fields after a body, in their order. The
// grammar lets a server stop after any of them, so count says how many came.
type wireExtension struct {
	count       int
	first       string // MD5 of one part, or the parameters of a multipart
	disposition string
	language    string
	location    wireString
}

func (e *wireExtension) String() string {
	if e == nil {
		return ""
	}
	fields := []string{e.first, e.disposition, e.language, e.location.String()}
	return " " + strings.Join(fields[:e.count], " ")
}

// wireBody is one body production. kind names the alternative the reader took:
// "basic", "text" or "message" for body-type-1part, "multipart" for
// body-type-mpart.
type wireBody struct {
	kind          string
	mediaType     string
	subtype       string
	params        wireParams
	id, desc      wireString
	encoding      string
	octets, lines uint32
	envelope      *wireEnvelope
	inner         *wireBody
	parts         []*wireBody
	extension     *wireExtension
}

func (b *wireBody) String() string {
	var out strings.Builder
	out.WriteByte('(')
	if b.kind == "multipart" {
		for _, part := range b.parts {
			out.WriteString(part.String())
		}
		fmt.Fprintf(&out, " %q%s)", b.subtype, b.extension)
		return out.String()
	}
	fmt.Fprintf(&out, "%q %q %s %s %s %q %d", b.mediaType, b.subtype, b.params, b.id, b.desc, b.encoding, b.octets)
	switch b.kind {
	case "message":
		fmt.Fprintf(&out, " %s %s %d", b.envelope, b.inner, b.lines)
	case "text":
		fmt.Fprintf(&out, " %d", b.lines)
	}
	fmt.Fprintf(&out, "%s)", b.extension)
	return out.String()
}

// wireFetch is one msg-att. items keeps the item names in the order they came.
type wireFetch struct {
	items         []string
	flags         []string
	uid           uint32
	size          uint32
	internalDate  string
	envelope      *wireEnvelope
	body          *wireBody
	bodyStructure *wireBody
	sections      map[string]wireString
}

func (f *wireFetch) has(item string) bool {
	return slices.Contains(f.items, item)
}

type wireListRow struct {
	attributes []string
	delimiter  wireString
	name       string
}

// wireResponse is one response. tag is "*" for response-data, "+" for
// continue-req, and otherwise the command's tag. kind is the keyword: OK, NO,
// BAD, BYE, PREAUTH, CAPABILITY, FLAGS, LIST, LSUB, SEARCH, STATUS, EXISTS,
// RECENT, EXPUNGE or FETCH, and empty for continue-req.
type wireResponse struct {
	tag, kind string
	number    uint32
	code      string
	text      string
	words     []string // CAPABILITY names or FLAGS
	numbers   []uint32 // SEARCH results
	list      *wireListRow
	name      string            // STATUS mailbox
	status    map[string]uint32 // STATUS attributes
	fetch     *wireFetch
}

// grammarFailure carries a refusal up to the reader's entry point.
type grammarFailure string

type grammarReader struct {
	r    *bufio.Reader
	seen strings.Builder
}

func (g *grammarReader) fail(format string, args ...any) {
	panic(grammarFailure(fmt.Sprintf(format, args...) + fmt.Sprintf(" after %q", g.seen.String())))
}

func (g *grammarReader) peek() byte {
	b, err := g.r.Peek(1)
	if err != nil {
		g.fail("read: %v", err)
	}
	return b[0]
}

func (g *grammarReader) next() byte {
	c, err := g.r.ReadByte()
	if err != nil {
		g.fail("read: %v", err)
	}
	g.seen.WriteByte(c)
	return c
}

// word consumes exactly want.
func (g *grammarReader) word(want string) {
	for i := range len(want) {
		if c := g.next(); c != want[i] {
			g.fail("got %q in place of %q", c, want)
		}
	}
}

// accept consumes want when it comes next. A partial match fails, as no
// production here shares a prefix with its alternative.
func (g *grammarReader) accept(want string) bool {
	if g.peek() != want[0] {
		return false
	}
	g.word(want)
	return true
}

func (g *grammarReader) sp()   { g.word(" ") }
func (g *grammarReader) crlf() { g.word("\r\n") }

// number reads RFC 3501 §9 number, an unsigned 32-bit integer.
func (g *grammarReader) number() uint32 {
	var digits strings.Builder
	for {
		c := g.peek()
		if c < '0' || c > '9' {
			break
		}
		digits.WriteByte(g.next())
	}
	value, err := strconv.ParseUint(digits.String(), 10, 32)
	if err != nil {
		g.fail("number %q: %v", digits.String(), err)
	}
	return uint32(value)
}

// nzNumber reads nz-number, which cannot be zero or start with a zero.
func (g *grammarReader) nzNumber() uint32 {
	if c := g.peek(); c < '1' || c > '9' {
		g.fail("nz-number starts with %q", c)
	}
	return g.number()
}

func atomChar(c byte) bool {
	return c > 0x20 && c < 0x7f && !strings.ContainsRune(`(){%*"\]`, rune(c))
}

func (g *grammarReader) atom() string {
	var b strings.Builder
	for atomChar(g.peek()) {
		b.WriteByte(g.next())
	}
	if b.Len() == 0 {
		g.fail("atom starts with %q", g.peek())
	}
	return b.String()
}

// str reads string: a quoted-string or a synchronizing-form literal. A server
// never sends the non-synchronizing form.
func (g *grammarReader) str() string {
	switch g.peek() {
	case '"':
		return g.quoted()
	case '{':
		g.next()
		count := g.number()
		g.word("}\r\n")
		octets := make([]byte, count)
		if _, err := io.ReadFull(g.r, octets); err != nil {
			g.fail("literal of %d octets: %v", count, err)
		}
		g.seen.Write(octets)
		if strings.IndexByte(string(octets), 0) >= 0 {
			g.fail("literal holds NUL")
		}
		return string(octets)
	}
	g.fail("string starts with %q", g.peek())
	return ""
}

func (g *grammarReader) quoted() string {
	g.word(`"`)
	var b strings.Builder
	for {
		c := g.next()
		switch {
		case c == '"':
			return b.String()
		case c == '\\':
			escaped := g.next()
			if escaped != '"' && escaped != '\\' {
				g.fail("quoted-string escapes %q", escaped)
			}
			b.WriteByte(escaped)
		case c == 0 || c == '\r' || c == '\n' || c >= 0x80:
			g.fail("quoted-string holds %q", c)
		default:
			b.WriteByte(c)
		}
	}
}

func (g *grammarReader) nstring() wireString {
	if g.accept("NIL") {
		return wireString{isNil: true}
	}
	return wireString{value: g.str()}
}

// astring reads an atom of ASTRING-CHAR, which also takes "]", or a string.
func (g *grammarReader) astring() string {
	if c := g.peek(); c == '"' || c == '{' {
		return g.str()
	}
	var b strings.Builder
	for atomChar(g.peek()) || g.peek() == ']' {
		b.WriteByte(g.next())
	}
	if b.Len() == 0 {
		g.fail("astring starts with %q", g.peek())
	}
	return b.String()
}

// flag reads flag, flag-fetch or flag-perm: a keyword atom, or "\" and an atom,
// and "\*" only when star is set.
func (g *grammarReader) flag(star bool) string {
	if g.peek() != '\\' {
		return g.atom()
	}
	g.next()
	if star && g.accept("*") {
		return `\*`
	}
	return `\` + g.atom()
}

// flagList reads "(" [flag *(SP flag)] ")".
func (g *grammarReader) flagList(star bool) []string {
	g.word("(")
	flags := []string{}
	if g.peek() == ')' {
		g.next()
		return flags
	}
	for {
		flags = append(flags, g.flag(star))
		if g.accept(")") {
			return flags
		}
		g.sp()
	}
}

// address reads "(" addr-name SP addr-adl SP addr-mailbox SP addr-host ")".
func (g *grammarReader) address() wireAddress {
	g.word("(")
	var a wireAddress
	a.name = g.nstring()
	g.sp()
	a.route = g.nstring()
	g.sp()
	a.mailbox = g.nstring()
	g.sp()
	a.host = g.nstring()
	g.word(")")
	return a
}

// addresses reads "(" 1*address ")" or NIL.
func (g *grammarReader) addresses() wireAddresses {
	if g.accept("NIL") {
		return nil
	}
	g.word("(")
	list := wireAddresses{g.address()}
	for g.peek() == '(' {
		list = append(list, g.address())
	}
	g.word(")")
	return list
}

// envelope reads the ten fields of RFC 3501 §9 envelope, in their order.
func (g *grammarReader) envelope() *wireEnvelope {
	g.word("(")
	var e wireEnvelope
	e.date = g.nstring()
	g.sp()
	e.subject = g.nstring()
	for _, field := range []*wireAddresses{&e.from, &e.sender, &e.replyTo, &e.to, &e.cc, &e.bcc} {
		g.sp()
		*field = g.addresses()
	}
	g.sp()
	e.inReplyTo = g.nstring()
	g.sp()
	e.messageID = g.nstring()
	g.word(")")
	return &e
}

// params reads body-fld-param: "(" string SP string *(SP string SP string) ")"
// or NIL. The grammar has no empty list.
func (g *grammarReader) params() wireParams {
	if g.accept("NIL") {
		return nil
	}
	g.word("(")
	var list wireParams
	for {
		name := g.str()
		g.sp()
		list = append(list, [2]string{name, g.str()})
		if g.accept(")") {
			return list
		}
		g.sp()
	}
}

// disposition reads body-fld-dsp: "(" string SP body-fld-param ")" or NIL.
func (g *grammarReader) disposition() string {
	if g.accept("NIL") {
		return "NIL"
	}
	g.word("(")
	kind := g.str()
	g.sp()
	parameters := g.params()
	g.word(")")
	return fmt.Sprintf("(%q %s)", kind, parameters)
}

// language reads body-fld-lang: an nstring, or "(" string *(SP string) ")".
func (g *grammarReader) language() string {
	if g.peek() != '(' {
		return g.nstring().String()
	}
	g.next()
	tags := []string{strconv.Quote(g.str())}
	for !g.accept(")") {
		g.sp()
		tags = append(tags, strconv.Quote(g.str()))
	}
	return "(" + strings.Join(tags, " ") + ")"
}

// bodyExtension reads the optional suffix of a body: MD5 or parameters by
// first, then disposition, language and location. Any further field would be a
// body-extension, which RFC 3501 defines no value for, so it is refused along
// with any suffix on a body that takes none.
func (g *grammarReader) bodyExtension(extended bool, first func() string) *wireExtension {
	if g.peek() != ' ' {
		g.word(")")
		return nil
	}
	if !extended {
		g.fail("BODY carries extension data")
	}
	g.sp()
	e := &wireExtension{count: 1, first: first()}
	if g.accept(" ") {
		e.disposition, e.count = g.disposition(), 2
		if g.accept(" ") {
			e.language, e.count = g.language(), 3
			if g.accept(" ") {
				e.location, e.count = g.nstring(), 4
			}
		}
	}
	g.word(")")
	return e
}

// body reads body. extended says whether this is BODYSTRUCTURE, the only form
// that may carry extension data. A MESSAGE/RFC822 or TEXT type takes its own
// alternative: body-type-basic's "MESSAGE subtype MUST NOT be RFC822".
func (g *grammarReader) body(extended bool) *wireBody {
	g.word("(")
	if g.peek() == '(' {
		b := &wireBody{kind: "multipart"}
		for g.peek() == '(' {
			b.parts = append(b.parts, g.body(extended))
		}
		g.sp()
		b.subtype = g.str()
		b.extension = g.bodyExtension(extended, func() string { return g.params().String() })
		return b
	}
	b := &wireBody{kind: "basic"}
	b.mediaType = g.str()
	g.sp()
	b.subtype = g.str()
	g.sp()
	b.params = g.params()
	g.sp()
	b.id = g.nstring()
	g.sp()
	b.desc = g.nstring()
	g.sp()
	b.encoding = g.str()
	g.sp()
	b.octets = g.number()
	switch {
	case strings.EqualFold(b.mediaType, "MESSAGE") && strings.EqualFold(b.subtype, "RFC822"):
		b.kind = "message"
		g.sp()
		b.envelope = g.envelope()
		g.sp()
		b.inner = g.body(extended)
		g.sp()
		b.lines = g.number()
	case strings.EqualFold(b.mediaType, "TEXT"):
		b.kind = "text"
		g.sp()
		b.lines = g.number()
	}
	b.extension = g.bodyExtension(extended, func() string { return g.nstring().String() })
	return b
}

var wireDateTime = regexp.MustCompile(`^( \d|\d\d)-(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)-\d{4} \d\d:\d\d:\d\d [+-]\d{4}$`)

// msgAtt reads msg-att: a parenthesized list that holds at least one item.
func (g *grammarReader) msgAtt() *wireFetch {
	g.word("(")
	f := &wireFetch{sections: map[string]wireString{}}
	for {
		var name strings.Builder
		for {
			c := g.peek()
			if c == ' ' || c == '[' || c == ')' {
				break
			}
			name.WriteByte(g.next())
		}
		item := name.String()
		switch {
		case item == "FLAGS":
			g.sp()
			f.flags = g.flagList(false)
		case item == "ENVELOPE":
			g.sp()
			f.envelope = g.envelope()
		case item == "INTERNALDATE":
			g.sp()
			f.internalDate = g.quoted()
			if !wireDateTime.MatchString(f.internalDate) {
				g.fail("date-time %q", f.internalDate)
			}
		case item == "RFC822" || item == "RFC822.HEADER" || item == "RFC822.TEXT":
			g.sp()
			f.sections[item] = g.nstring()
		case item == "RFC822.SIZE":
			g.sp()
			f.size = g.number()
		case item == "UID":
			g.sp()
			f.uid = g.nzNumber()
		case item == "BODYSTRUCTURE":
			g.sp()
			f.bodyStructure = g.body(true)
		case item == "BODY" && g.peek() == ' ':
			g.sp()
			f.body = g.body(false)
		case item == "BODY":
			var section strings.Builder
			section.WriteString("BODY")
			for {
				c := g.next()
				section.WriteByte(c)
				if c == ']' {
					break
				}
			}
			if g.accept("<") {
				fmt.Fprintf(&section, "<%d>", g.number())
				g.word(">")
			}
			item = section.String()
			g.sp()
			f.sections[item] = g.nstring()
		default:
			g.fail("msg-att item %q", item)
		}
		f.items = append(f.items, item)
		if g.accept(")") {
			return f
		}
		g.sp()
	}
}

// respText reads resp-text: an optional code, then text, which is not empty.
func (g *grammarReader) respText(r *wireResponse) {
	if g.accept("[") {
		r.code = g.atom()
		g.respTextCode(r)
		g.word("]")
		g.sp()
	}
	var text strings.Builder
	for g.peek() != '\r' {
		c := g.next()
		if c == 0 || c == '\n' || c >= 0x80 {
			g.fail("text holds %q", c)
		}
		text.WriteByte(c)
	}
	if text.Len() == 0 {
		g.fail("resp-text has no text")
	}
	r.text = text.String()
}

// respTextCode reads what follows the name of a resp-text-code.
func (g *grammarReader) respTextCode(r *wireResponse) {
	switch r.code {
	case "ALERT", "PARSE", "READ-ONLY", "READ-WRITE", "TRYCREATE":
	case "BADCHARSET":
		if g.accept(" ") {
			g.word("(")
			g.astring()
			for !g.accept(")") {
				g.sp()
				g.astring()
			}
		}
	case "CAPABILITY":
		r.words = g.capabilities()
	case "PERMANENTFLAGS":
		g.sp()
		r.words = g.flagList(true)
	case "UIDNEXT", "UIDVALIDITY", "UNSEEN":
		g.sp()
		r.number = g.nzNumber()
	default:
		// atom [SP 1*<any TEXT-CHAR except "]">]
		for g.peek() != ']' {
			if c := g.next(); c == 0 || c == '\r' || c == '\n' || c >= 0x80 {
				g.fail("response code holds %q", c)
			}
		}
	}
}

// capabilities reads *(SP capability) after the CAPABILITY keyword.
func (g *grammarReader) capabilities() []string {
	var names []string
	for g.accept(" ") {
		names = append(names, g.atom())
	}
	return names
}

// response reads one response, including its CRLF.
func (g *grammarReader) response() wireResponse {
	var r wireResponse
	switch g.peek() {
	case '+':
		g.next()
		g.sp()
		r.tag = "+"
		// continue-req = "+" SP (resp-text / base64). Empty base64 is legal.
		if g.peek() != '\r' {
			g.respText(&r)
		}
	case '*':
		g.next()
		g.sp()
		r.tag = "*"
		g.responseData(&r)
	default:
		var tag strings.Builder
		for g.peek() != ' ' {
			c := g.next()
			if !atomChar(c) || c == '+' {
				g.fail("tag holds %q", c)
			}
			tag.WriteByte(c)
		}
		g.sp()
		r.tag = tag.String()
		r.kind = g.atom()
		if r.kind != "OK" && r.kind != "NO" && r.kind != "BAD" {
			g.fail("tagged response %q", r.kind)
		}
		g.sp()
		g.respText(&r)
	}
	g.crlf()
	return r
}

// responseData reads what follows "* ", up to the CRLF.
func (g *grammarReader) responseData(r *wireResponse) {
	if c := g.peek(); c >= '0' && c <= '9' {
		r.number = g.number()
		g.sp()
		r.kind = g.atom()
		switch r.kind {
		case "EXISTS", "RECENT":
		case "EXPUNGE", "FETCH":
			if r.number == 0 {
				g.fail("%s of message 0", r.kind)
			}
			if r.kind == "FETCH" {
				g.sp()
				r.fetch = g.msgAtt()
			}
		default:
			g.fail("numbered response %q", r.kind)
		}
		return
	}
	r.kind = g.atom()
	switch r.kind {
	case "OK", "NO", "BAD", "BYE", "PREAUTH":
		g.sp()
		g.respText(r)
	case "CAPABILITY":
		r.words = g.capabilities()
	case "FLAGS":
		g.sp()
		r.words = g.flagList(false)
	case "LIST", "LSUB":
		g.sp()
		r.list = g.listRow()
	case "SEARCH":
		for g.accept(" ") {
			r.numbers = append(r.numbers, g.nzNumber())
		}
	case "STATUS":
		g.sp()
		r.name = g.astring()
		g.sp()
		r.status = g.statusAttributes()
	default:
		g.fail("response %q", r.kind)
	}
}

// listRow reads mailbox-list: its flags, one quoted delimiter character or NIL,
// and a mailbox.
func (g *grammarReader) listRow() *wireListRow {
	row := &wireListRow{attributes: g.flagList(false)}
	g.sp()
	if g.accept("NIL") {
		row.delimiter = wireString{isNil: true}
	} else {
		row.delimiter = wireString{value: g.quoted()}
		if len(row.delimiter.value) != 1 {
			g.fail("delimiter %q is not one character", row.delimiter.value)
		}
	}
	g.sp()
	row.name = g.astring()
	return row
}

// statusAttributes reads "(" [status-att-list] ")": attribute and number pairs.
func (g *grammarReader) statusAttributes() map[string]uint32 {
	g.word("(")
	values := map[string]uint32{}
	if g.accept(")") {
		return values
	}
	for {
		name := g.atom()
		g.sp()
		values[name] = g.number()
		if g.accept(")") {
			return values
		}
		g.sp()
	}
}

// readResponse reads one response from r, reporting a grammar refusal as an
// error.
func readResponse(r *bufio.Reader) (response wireResponse, err error) {
	g := &grammarReader{r: r}
	defer func() {
		if failure := recover(); failure != nil {
			message, isGrammar := failure.(grammarFailure)
			if !isGrammar {
				panic(failure)
			}
			err = fmt.Errorf("%s", message)
		}
	}()
	return g.response(), nil
}

// response reads one response from the session, failing the test on anything
// the grammar refuses.
func (c *client) response() wireResponse {
	c.t.Helper()
	response, err := readResponse(c.r)
	if err != nil {
		c.t.Fatal(err)
	}
	return response
}

// exchange sends one command line and reads through the response tagged tag,
// which comes last in the result.
func (c *client) exchange(tag, format string, args ...any) []wireResponse {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, tag+" "+format+"\r\n", args...); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	return c.responsesThrough(tag)
}

// responsesThrough reads through the response tagged tag.
func (c *client) responsesThrough(tag string) []wireResponse {
	c.t.Helper()
	var responses []wireResponse
	for {
		response := c.response()
		responses = append(responses, response)
		if response.tag == tag {
			return responses
		}
		if response.tag != "*" {
			c.t.Fatalf("%s: response tagged %q", tag, response.tag)
		}
	}
}

// fetched reads the only FETCH of a command that must succeed.
func fetched(t *testing.T, c *client, command string) *wireFetch {
	t.Helper()
	responses := c.exchange("G1", "%s", command)
	if last := responses[len(responses)-1]; last.kind != "OK" {
		t.Fatalf("%s: %+v", command, last)
	}
	var found *wireFetch
	for _, response := range responses {
		if response.kind == "FETCH" {
			if found != nil {
				t.Fatalf("%s: more than one FETCH: %+v", command, responses)
			}
			found = response.fetch
		}
	}
	if found == nil {
		t.Fatalf("%s: no FETCH: %+v", command, responses)
	}
	return found
}

// TestResponseGrammarReaderRefuses keeps the reader honest: the tests that rely
// on it prove nothing if it takes what RFC 3501 §9 refuses.
//
// checklist: IMAP-INV-ABNF-11
func TestResponseGrammarReaderRefuses(t *testing.T) {
	for _, tc := range []struct{ name, response string }{
		{"no text", "A1 OK \r\n"},
		{"code without text", "A1 OK [READ-ONLY]\r\n"},
		{"bare LF", "* OK ready\n"},
		{"unknown tagged status", "A1 BYE gone\r\n"},
		{"8-bit text", "* OK caf\xc3\xa9\r\n"},
		{"8-bit quoted", "* 1 FETCH (RFC822 \"caf\xc3\xa9\")\r\n"},
		{"bad escape", "* 1 FETCH (RFC822 \"a\\b\")\r\n"},
		{"NUL literal", "* 1 FETCH (RFC822 {1}\r\n\x00)\r\n"},
		{"short literal", "* 1 FETCH (RFC822 {9}\r\nabc"},
		{"empty msg-att", "* 1 FETCH ()\r\n"},
		{"message zero", "* 0 FETCH (UID 1)\r\n"},
		{"expunge zero", "* 0 EXPUNGE\r\n"},
		{"UID zero", "* 1 FETCH (UID 0)\r\n"},
		{"number too wide", "* 4294967296 EXISTS\r\n"},
		{"bad date", "* 1 FETCH (INTERNALDATE \"1-Jan-2020 00:00:00 +0000\")\r\n"},
		{"nine envelope fields", "* 1 FETCH (ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL))\r\n"},
		{"empty address list", "* 1 FETCH (ENVELOPE (NIL NIL () NIL NIL NIL NIL NIL NIL NIL))\r\n"},
		{"three address fields", "* 1 FETCH (ENVELOPE (NIL NIL ((NIL NIL NIL)) NIL NIL NIL NIL NIL NIL NIL))\r\n"},
		{"BODY with extension", "* 1 FETCH (BODY (\"TEXT\" \"PLAIN\" NIL NIL NIL \"7BIT\" 1 1 NIL))\r\n"},
		{"text without lines", "* 1 FETCH (BODY (\"TEXT\" \"PLAIN\" NIL NIL NIL \"7BIT\" 1))\r\n"},
		{"message as basic", "* 1 FETCH (BODY (\"MESSAGE\" \"RFC822\" NIL NIL NIL \"7BIT\" 1))\r\n"},
		{"multipart without subtype", "* 1 FETCH (BODY ((\"TEXT\" \"PLAIN\" NIL NIL NIL \"7BIT\" 1 1)))\r\n"},
		{"empty parameters", "* 1 FETCH (BODY (\"APPLICATION\" \"X\" () NIL NIL \"7BIT\" 1))\r\n"},
		{"undefined extension", "* 1 FETCH (BODYSTRUCTURE (\"APPLICATION\" \"X\" NIL NIL NIL \"7BIT\" 1 NIL NIL NIL NIL NIL))\r\n"},
		{"long delimiter", "* LIST () \"//\" a\r\n"},
		{"status without value", "* STATUS a (MESSAGES)\r\n"},
		{"search zero", "* SEARCH 0\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if response, err := readResponse(bufio.NewReader(strings.NewReader(tc.response))); err == nil {
				t.Fatalf("%q read as %+v", tc.response, response)
			}
		})
	}
}

// TestResponseGrammarReaderTakes checks the reader accepts each alternative the
// tests rely on, and returns values whatever their framing.
//
// checklist: IMAP-INV-ABNF-11
func TestResponseGrammarReaderTakes(t *testing.T) {
	for _, tc := range []struct{ response, field, want string }{
		{"* 1 FETCH (BODY[TEXT]<2> {3}\r\nabc)\r\n", "BODY[TEXT]<2>", `"abc"`},
		{"* 1 FETCH (RFC822 NIL)\r\n", "RFC822", "NIL"},
		{"* 1 FETCH (RFC822 \"a\\\"b\\\\c\")\r\n", "RFC822", `"a\"b\\c"`},
	} {
		response, err := readResponse(bufio.NewReader(strings.NewReader(tc.response)))
		if err != nil {
			t.Fatalf("%q: %v", tc.response, err)
		}
		if got := response.fetch.sections[tc.field].String(); got != tc.want {
			t.Fatalf("%q: %s, want %s", tc.response, got, tc.want)
		}
	}
	const parts = "((\"TEXT\" \"PLAIN\" (\"A\" \"\") {1}\r\nx NIL \"7BIT\" 1 1 NIL NIL NIL NIL)(\"APPLICATION\" \"X\" NIL NIL NIL \"BASE64\" 4 NIL (\"INLINE\" NIL) (\"en\" \"de\") NIL) \"MIXED\" (\"BOUNDARY\" \"b\") NIL \"en\" NIL)"
	response, err := readResponse(bufio.NewReader(strings.NewReader("* 1 FETCH (BODYSTRUCTURE " + parts + ")\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := `(("TEXT" "PLAIN" ("A" "") "x" NIL "7BIT" 1 1 NIL NIL NIL NIL)("APPLICATION" "X" NIL NIL NIL "BASE64" 4 NIL ("INLINE" NIL) ("en" "de") NIL) "MIXED" ("BOUNDARY" "b") NIL "en" NIL)`
	if got := response.fetch.bodyStructure.String(); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}
