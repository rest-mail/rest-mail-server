package filters

// This file implements a tokenizer and recursive-descent parser for a
// practical subset of the Sieve mail-filtering language (RFC 5228), plus a
// few widely used extensions (envelope, body, imap4flags, vacation, notify)
// and a non-standard :regex match type retained for backwards compatibility.
//
// The parser produces an AST (sieveScript -> commands -> tests) that the
// evaluator in sieve.go walks at execution time. Parsing is deliberately
// strict for the constructs it understands (so ValidateSieve can catch real
// mistakes) but lenient about unknown commands, tests and tagged arguments so
// that scripts relying on extensions we do not implement still load and run
// their recognised parts.

import (
	"fmt"
	"strconv"
	"strings"
)

// ── AST ──────────────────────────────────────────────────────────────

// sieveScript is a fully parsed Sieve script.
type sieveScript struct {
	requires []string
	commands []sieveCmd
}

// sieveCmd is a single command (action or control structure).
type sieveCmd interface{ isCmd() }

// ifCmd represents an if / elsif* / else? chain. Branches are evaluated in
// order; the first whose test passes (or the trailing else, whose test is nil)
// has its block executed.
type ifCmd struct {
	branches []sieveBranch
}

type sieveBranch struct {
	test  sieveTest // nil for the trailing "else"
	block []sieveCmd
}

type stopCmd struct{}
type keepCmd struct{}
type discardCmd struct{}

type fileintoCmd struct {
	folder string
	create bool
}

type redirectCmd struct{ addr string }
type rejectCmd struct{ reason string }

// flagCmd covers setflag / addflag / removeflag from the imap4flags extension.
type flagCmd struct {
	op    string // "setflag", "addflag", "removeflag"
	flags []string
}

type vacationCmd struct {
	days    int
	subject string
	body    string
}

type notifyCmd struct {
	method  string
	message string
}

func (*ifCmd) isCmd()       {}
func (*stopCmd) isCmd()     {}
func (*keepCmd) isCmd()     {}
func (*discardCmd) isCmd()  {}
func (*fileintoCmd) isCmd() {}
func (*redirectCmd) isCmd() {}
func (*rejectCmd) isCmd()   {}
func (*flagCmd) isCmd()     {}
func (*vacationCmd) isCmd() {}
func (*notifyCmd) isCmd()   {}

// sieveTest is a boolean test used inside a control command.
type sieveTest interface{ isTest() }

type allofTest struct{ tests []sieveTest }
type anyofTest struct{ tests []sieveTest }
type notTest struct{ inner sieveTest }
type boolTest struct{ val bool }
type existsTest struct{ headers []string }

type sizeTest struct {
	over  bool
	limit int64
}

type headerTest struct {
	comparator string
	matchType  string
	headers    []string
	keys       []string
}

type addressTest struct {
	comparator  string
	matchType   string
	addressPart string
	headers     []string
	keys        []string
}

type envelopeTest struct {
	comparator  string
	matchType   string
	addressPart string
	parts       []string
	keys        []string
}

type bodyTest struct {
	comparator string
	matchType  string
	keys       []string
}

func (*allofTest) isTest()    {}
func (*anyofTest) isTest()    {}
func (*notTest) isTest()      {}
func (*boolTest) isTest()     {}
func (*existsTest) isTest()   {}
func (*sizeTest) isTest()     {}
func (*headerTest) isTest()   {}
func (*addressTest) isTest()  {}
func (*envelopeTest) isTest() {}
func (*bodyTest) isTest()     {}

// Defaults per RFC 5228.
const (
	defaultMatchType   = ":is"
	defaultComparator  = "i;ascii-casemap"
	defaultAddressPart = ":all"
)

// ── Tokenizer ────────────────────────────────────────────────────────

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tTag
	tString
	tNumber
	tLBrace
	tRBrace
	tLParen
	tRParen
	tLBracket
	tRBracket
	tComma
	tSemicolon
)

type token struct {
	kind tokKind
	str  string // ident/tag name (lowercased) or string value
	num  int64  // for tNumber
	line int
}

type lexer struct {
	src  string
	pos  int
	line int
}

func newLexer(src string) *lexer { return &lexer{src: src, line: 1} }

func (l *lexer) errf(format string, args ...interface{}) error {
	return fmt.Errorf("sieve: line %d: %s", l.line, fmt.Sprintf(format, args...))
}

// tokenize scans the whole source into a token slice terminated by tEOF.
func (l *lexer) tokenize() ([]token, error) {
	var toks []token
	for {
		if err := l.skipTrivia(); err != nil {
			return nil, err
		}
		if l.pos >= len(l.src) {
			toks = append(toks, token{kind: tEOF, line: l.line})
			return toks, nil
		}

		// Multi-line string: "text:" at a token boundary, followed only by
		// optional whitespace / comment until end of line.
		if l.atMultiLineString() {
			s, err := l.readMultiLineString()
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tString, str: s, line: l.line})
			continue
		}

		c := l.src[l.pos]
		switch {
		case c == '{':
			toks = append(toks, token{kind: tLBrace, line: l.line})
			l.pos++
		case c == '}':
			toks = append(toks, token{kind: tRBrace, line: l.line})
			l.pos++
		case c == '(':
			toks = append(toks, token{kind: tLParen, line: l.line})
			l.pos++
		case c == ')':
			toks = append(toks, token{kind: tRParen, line: l.line})
			l.pos++
		case c == '[':
			toks = append(toks, token{kind: tLBracket, line: l.line})
			l.pos++
		case c == ']':
			toks = append(toks, token{kind: tRBracket, line: l.line})
			l.pos++
		case c == ',':
			toks = append(toks, token{kind: tComma, line: l.line})
			l.pos++
		case c == ';':
			toks = append(toks, token{kind: tSemicolon, line: l.line})
			l.pos++
		case c == '"':
			s, err := l.readQuotedString()
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tString, str: s, line: l.line})
		case c == ':':
			tag := l.readTag()
			toks = append(toks, token{kind: tTag, str: tag, line: l.line})
		case c >= '0' && c <= '9':
			n, err := l.readNumber()
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tNumber, num: n, line: l.line})
		case isIdentStart(c):
			toks = append(toks, token{kind: tIdent, str: strings.ToLower(l.readIdent()), line: l.line})
		default:
			return nil, l.errf("unexpected character %q", string(c))
		}
	}
}

// skipTrivia advances over whitespace, hash comments and bracket comments.
func (l *lexer) skipTrivia() error {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.line++
			l.pos++
		case c == ' ' || c == '\t' || c == '\r':
			l.pos++
		case c == '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*':
			l.pos += 2
			for {
				if l.pos+1 >= len(l.src) {
					return l.errf("unterminated bracket comment")
				}
				if l.src[l.pos] == '*' && l.src[l.pos+1] == '/' {
					l.pos += 2
					break
				}
				if l.src[l.pos] == '\n' {
					l.line++
				}
				l.pos++
			}
		default:
			return nil
		}
	}
	return nil
}

func (l *lexer) readQuotedString() (string, error) {
	l.pos++ // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.pos++
			return b.String(), nil
		case '\\':
			// A backslash escapes the following character (RFC 5228 §2.4.2);
			// an escaped char is that char verbatim.
			if l.pos+1 < len(l.src) {
				l.pos++
				b.WriteByte(l.src[l.pos])
				l.pos++
				continue
			}
			l.pos++
		case '\n':
			return "", l.errf("unterminated string")
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	return "", l.errf("unterminated string")
}

func (l *lexer) readTag() string {
	start := l.pos
	l.pos++ // colon
	for l.pos < len(l.src) && isIdentChar(l.src[l.pos]) {
		l.pos++
	}
	return strings.ToLower(l.src[start:l.pos])
}

func (l *lexer) readIdent() string {
	start := l.pos
	for l.pos < len(l.src) && isIdentChar(l.src[l.pos]) {
		l.pos++
	}
	return l.src[start:l.pos]
}

// readNumber reads a non-negative integer with an optional K/M/G quantifier
// (RFC 5228 §2.4.1). K=1024, M=1024^2, G=1024^3.
func (l *lexer) readNumber() (int64, error) {
	start := l.pos
	for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
		l.pos++
	}
	digits := l.src[start:l.pos]
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, l.errf("invalid number %q", digits)
	}
	if l.pos < len(l.src) {
		switch l.src[l.pos] {
		case 'K', 'k':
			n *= 1024
			l.pos++
		case 'M', 'm':
			n *= 1024 * 1024
			l.pos++
		case 'G', 'g':
			n *= 1024 * 1024 * 1024
			l.pos++
		}
	}
	return n, nil
}

// atMultiLineString reports whether the cursor sits on a "text:" multi-line
// string token: the literal "text:" followed by only whitespace and an
// optional hash comment up to the end of the line.
func (l *lexer) atMultiLineString() bool {
	const marker = "text:"
	if l.pos+len(marker) > len(l.src) {
		return false
	}
	if !strings.EqualFold(l.src[l.pos:l.pos+len(marker)], marker) {
		return false
	}
	i := l.pos + len(marker)
	for i < len(l.src) && (l.src[i] == ' ' || l.src[i] == '\t' || l.src[i] == '\r') {
		i++
	}
	if i < len(l.src) && l.src[i] == '#' {
		for i < len(l.src) && l.src[i] != '\n' {
			i++
		}
	}
	return i >= len(l.src) || l.src[i] == '\n'
}

// readMultiLineString consumes a "text:" block up to a line containing only a
// single period, applying dot-unstuffing (a leading ".." becomes ".").
func (l *lexer) readMultiLineString() (string, error) {
	// Skip "text:" and the remainder of the introducing line.
	l.pos += len("text:")
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.pos++
	}
	if l.pos < len(l.src) {
		l.pos++ // consume newline
		l.line++
	}

	var b strings.Builder
	for {
		if l.pos >= len(l.src) {
			return "", l.errf("unterminated multi-line string")
		}
		lineStart := l.pos
		for l.pos < len(l.src) && l.src[l.pos] != '\n' {
			l.pos++
		}
		line := strings.TrimSuffix(l.src[lineStart:l.pos], "\r")
		if l.pos < len(l.src) {
			l.pos++ // newline
			l.line++
		}
		if line == "." {
			return b.String(), nil
		}
		line = strings.TrimPrefix(line, ".") // dot-unstuffing
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_'
}

// ── Parser ───────────────────────────────────────────────────────────

type parser struct {
	toks     []token
	pos      int
	requires []string
}

func parseSieveScript(src string) (*sieveScript, error) {
	toks, err := newLexer(src).tokenize()
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	cmds, err := p.parseCommands(false)
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, p.errf("unexpected %s", p.describe(p.peek()))
	}
	return &sieveScript{requires: p.requires, commands: cmds}, nil
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *parser) errf(format string, args ...interface{}) error {
	return fmt.Errorf("sieve: line %d: %s", p.peek().line, fmt.Sprintf(format, args...))
}

func (p *parser) describe(t token) string {
	switch t.kind {
	case tEOF:
		return "end of script"
	case tIdent:
		return fmt.Sprintf("identifier %q", t.str)
	case tTag:
		return fmt.Sprintf("tag %q", t.str)
	case tString:
		return "string"
	case tNumber:
		return "number"
	default:
		return "token"
	}
}

func (p *parser) expect(k tokKind, what string) (token, error) {
	if p.peek().kind != k {
		return token{}, p.errf("expected %s, got %s", what, p.describe(p.peek()))
	}
	return p.next(), nil
}

// parseCommands parses a sequence of commands. When inBlock is true it stops at
// (and requires) a closing brace; otherwise it stops at EOF.
func (p *parser) parseCommands(inBlock bool) ([]sieveCmd, error) {
	var cmds []sieveCmd
	for {
		t := p.peek()
		if t.kind == tEOF {
			if inBlock {
				return nil, p.errf("expected } but reached end of script")
			}
			return cmds, nil
		}
		if t.kind == tRBrace {
			if !inBlock {
				return nil, p.errf("unexpected }")
			}
			return cmds, nil
		}
		cmd, err := p.parseCommand()
		if err != nil {
			return nil, err
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
}

func (p *parser) parseCommand() (sieveCmd, error) {
	t := p.peek()
	if t.kind != tIdent {
		return nil, p.errf("expected a command, got %s", p.describe(t))
	}
	name := p.next().str

	switch name {
	case "require":
		return nil, p.parseRequire()
	case "if":
		return p.parseIf()
	case "elsif", "else":
		return nil, p.errf("%q without a matching if", name)
	case "stop":
		return &stopCmd{}, p.finishStatement()
	case "keep":
		p.skipArguments()
		return &keepCmd{}, p.finishStatement()
	case "discard":
		return &discardCmd{}, p.finishStatement()
	case "fileinto":
		return p.parseFileinto()
	case "redirect":
		return p.parseRedirect()
	case "reject", "ereject":
		return p.parseReject()
	case "setflag", "addflag", "removeflag":
		return p.parseFlag(name)
	case "vacation":
		return p.parseVacation()
	case "notify":
		return p.parseNotify()
	default:
		// Unknown command from an unimplemented extension: skip it so the rest
		// of the script still loads.
		return nil, p.skipUnknownCommand()
	}
}

func (p *parser) parseRequire() error {
	names, err := p.parseStringList()
	if err != nil {
		return err
	}
	p.requires = append(p.requires, names...)
	return p.finishStatement()
}

func (p *parser) parseIf() (sieveCmd, error) {
	test, err := p.parseTest()
	if err != nil {
		return nil, err
	}
	block, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	branches := []sieveBranch{{test: test, block: block}}

	for p.peek().kind == tIdent && p.peek().str == "elsif" {
		p.next()
		t, err := p.parseTest()
		if err != nil {
			return nil, err
		}
		b, err := p.parseBlock()
		if err != nil {
			return nil, err
		}
		branches = append(branches, sieveBranch{test: t, block: b})
	}

	if p.peek().kind == tIdent && p.peek().str == "else" {
		p.next()
		b, err := p.parseBlock()
		if err != nil {
			return nil, err
		}
		branches = append(branches, sieveBranch{test: nil, block: b})
	}

	return &ifCmd{branches: branches}, nil
}

func (p *parser) parseBlock() ([]sieveCmd, error) {
	if _, err := p.expect(tLBrace, "{"); err != nil {
		return nil, err
	}
	cmds, err := p.parseCommands(true)
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRBrace, "}"); err != nil {
		return nil, err
	}
	return cmds, nil
}

// finishStatement consumes the terminating semicolon of a command.
func (p *parser) finishStatement() error {
	_, err := p.expect(tSemicolon, ";")
	return err
}

func (p *parser) parseFileinto() (sieveCmd, error) {
	cmd := &fileintoCmd{}
	for p.peek().kind == tTag {
		switch p.next().str {
		case ":create":
			cmd.create = true
		case ":flags":
			if _, err := p.parseStringList(); err != nil {
				return nil, err
			}
		case ":copy":
			// no argument
		}
	}
	folder, err := p.expect(tString, "a folder name")
	if err != nil {
		return nil, err
	}
	cmd.folder = folder.str
	return cmd, p.finishStatement()
}

func (p *parser) parseRedirect() (sieveCmd, error) {
	for p.peek().kind == tTag {
		p.next() // :copy / :list / :notify etc. — no argument we act on
	}
	addr, err := p.expect(tString, "a redirect address")
	if err != nil {
		return nil, err
	}
	return &redirectCmd{addr: addr.str}, p.finishStatement()
}

func (p *parser) parseReject() (sieveCmd, error) {
	cmd := &rejectCmd{}
	if p.peek().kind == tString {
		cmd.reason = p.next().str
	}
	return cmd, p.finishStatement()
}

func (p *parser) parseFlag(op string) (sieveCmd, error) {
	// imap4flags setflag/addflag/removeflag may take an optional variable name
	// (which we do not support) followed by the flag list. We only accept the
	// flag string / string-list form.
	flags, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	return &flagCmd{op: op, flags: flags}, p.finishStatement()
}

func (p *parser) parseVacation() (sieveCmd, error) {
	cmd := &vacationCmd{days: 7} // RFC 5230 default
	for p.peek().kind == tTag {
		switch p.next().str {
		case ":days":
			n, err := p.expect(tNumber, "a day count")
			if err != nil {
				return nil, err
			}
			cmd.days = int(n.num)
		case ":seconds":
			n, err := p.expect(tNumber, "a seconds count")
			if err != nil {
				return nil, err
			}
			cmd.days = int(n.num / 86400)
		case ":subject":
			s, err := p.expect(tString, "a subject")
			if err != nil {
				return nil, err
			}
			cmd.subject = s.str
		case ":from", ":handle":
			if _, err := p.expect(tString, "a string"); err != nil {
				return nil, err
			}
		case ":addresses":
			if _, err := p.parseStringList(); err != nil {
				return nil, err
			}
		case ":mime":
			// no argument
		}
	}
	if p.peek().kind == tString {
		cmd.body = p.next().str
	}
	return cmd, p.finishStatement()
}

func (p *parser) parseNotify() (sieveCmd, error) {
	cmd := &notifyCmd{}
	for p.peek().kind == tTag {
		switch p.next().str {
		case ":method":
			s, err := p.expect(tString, "a notification method")
			if err != nil {
				return nil, err
			}
			cmd.method = s.str
		case ":message":
			s, err := p.expect(tString, "a message")
			if err != nil {
				return nil, err
			}
			cmd.message = s.str
		case ":options":
			if _, err := p.parseStringList(); err != nil {
				return nil, err
			}
		case ":from", ":importance", ":id":
			if _, err := p.expect(tString, "a string"); err != nil {
				return nil, err
			}
		}
	}
	// RFC 5435 puts the method in a trailing positional string.
	if cmd.method == "" && p.peek().kind == tString {
		cmd.method = p.next().str
	}
	return cmd, p.finishStatement()
}

// skipUnknownCommand discards an unrecognised command, including a trailing
// block if present, up to and including its terminating semicolon.
func (p *parser) skipUnknownCommand() error {
	depth := 0
	for {
		t := p.peek()
		switch t.kind {
		case tEOF:
			return nil
		case tLBrace:
			depth++
			p.next()
		case tRBrace:
			if depth == 0 {
				return nil
			}
			depth--
			p.next()
			if depth == 0 {
				return nil
			}
		case tSemicolon:
			p.next()
			if depth == 0 {
				return nil
			}
		default:
			p.next()
		}
	}
}

// ── Test parsing ─────────────────────────────────────────────────────

func (p *parser) parseTest() (sieveTest, error) {
	t := p.peek()
	if t.kind != tIdent {
		return nil, p.errf("expected a test, got %s", p.describe(t))
	}
	name := p.next().str

	switch name {
	case "allof":
		tests, err := p.parseTestList()
		if err != nil {
			return nil, err
		}
		return &allofTest{tests: tests}, nil
	case "anyof":
		tests, err := p.parseTestList()
		if err != nil {
			return nil, err
		}
		return &anyofTest{tests: tests}, nil
	case "not":
		inner, err := p.parseTest()
		if err != nil {
			return nil, err
		}
		return &notTest{inner: inner}, nil
	case "true":
		return &boolTest{val: true}, nil
	case "false":
		return &boolTest{val: false}, nil
	case "exists":
		headers, err := p.parseStringList()
		if err != nil {
			return nil, err
		}
		return &existsTest{headers: headers}, nil
	case "size":
		return p.parseSizeTest()
	case "header":
		return p.parseHeaderTest()
	case "address":
		return p.parseAddressTest()
	case "envelope":
		return p.parseEnvelopeTest()
	case "body":
		return p.parseBodyTest()
	default:
		// Unknown test from an unimplemented extension: consume its arguments
		// and treat it as always-false so surrounding logic still works.
		p.skipTestArguments()
		return &boolTest{val: false}, nil
	}
}

func (p *parser) parseTestList() ([]sieveTest, error) {
	if _, err := p.expect(tLParen, "("); err != nil {
		return nil, err
	}
	var tests []sieveTest
	for {
		t, err := p.parseTest()
		if err != nil {
			return nil, err
		}
		tests = append(tests, t)
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		break
	}
	if _, err := p.expect(tRParen, ")"); err != nil {
		return nil, err
	}
	return tests, nil
}

func (p *parser) parseSizeTest() (sieveTest, error) {
	st := &sizeTest{}
	gotOp := false
	for p.peek().kind == tTag {
		switch p.next().str {
		case ":over":
			st.over = true
			gotOp = true
		case ":under":
			st.over = false
			gotOp = true
		}
	}
	if !gotOp {
		return nil, p.errf("size test requires :over or :under")
	}
	n, err := p.expect(tNumber, "a size limit")
	if err != nil {
		return nil, err
	}
	st.limit = n.num
	return st, nil
}

// matchOptions collects the tagged arguments shared by string-matching tests.
type matchOptions struct {
	comparator  string
	matchType   string
	addressPart string
}

// parseMatchOptions consumes comparator, match-type and (for address/envelope)
// address-part tags in any order. allowAddressPart selects whether
// :localpart/:domain/:all are recognised; allowBody selects the body-transform
// tags :raw/:text/:content.
func (p *parser) parseMatchOptions(allowAddressPart, allowBody bool) (matchOptions, error) {
	opts := matchOptions{
		comparator:  defaultComparator,
		matchType:   defaultMatchType,
		addressPart: defaultAddressPart,
	}
	for p.peek().kind == tTag {
		tag := p.next().str
		switch tag {
		case ":is", ":contains", ":matches", ":regex":
			opts.matchType = tag
		case ":comparator":
			s, err := p.expect(tString, "a comparator name")
			if err != nil {
				return opts, err
			}
			opts.comparator = strings.ToLower(s.str)
		case ":localpart", ":domain", ":all":
			if allowAddressPart {
				opts.addressPart = tag
			}
		case ":content":
			if allowBody {
				if _, err := p.expect(tString, "a content type"); err != nil {
					return opts, err
				}
			}
		case ":raw", ":text":
			// body transforms we treat uniformly as extracted text
		}
	}
	return opts, nil
}

func (p *parser) parseHeaderTest() (sieveTest, error) {
	opts, err := p.parseMatchOptions(false, false)
	if err != nil {
		return nil, err
	}
	headers, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	keys, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	return &headerTest{
		comparator: opts.comparator,
		matchType:  opts.matchType,
		headers:    headers,
		keys:       keys,
	}, nil
}

func (p *parser) parseAddressTest() (sieveTest, error) {
	opts, err := p.parseMatchOptions(true, false)
	if err != nil {
		return nil, err
	}
	headers, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	keys, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	return &addressTest{
		comparator:  opts.comparator,
		matchType:   opts.matchType,
		addressPart: opts.addressPart,
		headers:     headers,
		keys:        keys,
	}, nil
}

func (p *parser) parseEnvelopeTest() (sieveTest, error) {
	opts, err := p.parseMatchOptions(true, false)
	if err != nil {
		return nil, err
	}
	parts, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	keys, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	return &envelopeTest{
		comparator:  opts.comparator,
		matchType:   opts.matchType,
		addressPart: opts.addressPart,
		parts:       parts,
		keys:        keys,
	}, nil
}

func (p *parser) parseBodyTest() (sieveTest, error) {
	opts, err := p.parseMatchOptions(false, true)
	if err != nil {
		return nil, err
	}
	keys, err := p.parseStringList()
	if err != nil {
		return nil, err
	}
	return &bodyTest{
		comparator: opts.comparator,
		matchType:  opts.matchType,
		keys:       keys,
	}, nil
}

// parseStringList parses either a single quoted string or a bracketed,
// comma-separated list of strings.
func (p *parser) parseStringList() ([]string, error) {
	if p.peek().kind == tString {
		return []string{p.next().str}, nil
	}
	if _, err := p.expect(tLBracket, "a string or string list"); err != nil {
		return nil, err
	}
	var list []string
	for p.peek().kind != tRBracket {
		s, err := p.expect(tString, "a string")
		if err != nil {
			return nil, err
		}
		list = append(list, s.str)
		if p.peek().kind == tComma {
			p.next()
			continue
		}
		break
	}
	if _, err := p.expect(tRBracket, "]"); err != nil {
		return nil, err
	}
	return list, nil
}

// skipArguments discards optional tagged/positional arguments of a command we
// accept but whose arguments we ignore (e.g. keep :flags ...).
func (p *parser) skipArguments() {
	for {
		switch p.peek().kind {
		case tTag, tNumber, tString:
			p.next()
		case tLBracket:
			p.skipBracketed()
		default:
			return
		}
	}
}

// skipTestArguments discards the arguments of an unknown test.
func (p *parser) skipTestArguments() {
	for {
		switch p.peek().kind {
		case tTag, tNumber, tString:
			p.next()
		case tLBracket:
			p.skipBracketed()
		case tLParen:
			p.skipParen()
		default:
			return
		}
	}
}

func (p *parser) skipBracketed() {
	p.next() // [
	for p.peek().kind != tRBracket && p.peek().kind != tEOF {
		p.next()
	}
	if p.peek().kind == tRBracket {
		p.next()
	}
}

func (p *parser) skipParen() {
	depth := 0
	for {
		switch p.peek().kind {
		case tEOF:
			return
		case tLParen:
			depth++
			p.next()
		case tRParen:
			depth--
			p.next()
			if depth == 0 {
				return
			}
		default:
			p.next()
		}
	}
}
