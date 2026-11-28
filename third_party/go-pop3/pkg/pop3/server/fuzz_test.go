package server

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
	"github.com/rest-mail/go-pop3/pkg/pop3/internal/wire"
)

// Fuzz targets for the pure parsers that handle bytes from strangers: the
// command line (and its log redaction), the UIDL unique-id, and the RETR/TOP
// message framing with its octet count. Each target checks a property derived
// from RFC 1939 / RFC 2449 or from an independent formulation, never the
// implementation compared with itself. CI gives each target a fixed execution
// budget; plain `go test` runs the seeds below as regression cases.

// maxFuzzInput caps every fuzz input so a run stays bounded in memory and time.
const maxFuzzInput = 64 << 10

func skipOversized(t *testing.T, inputs ...string) {
	t.Helper()
	for _, in := range inputs {
		if len(in) > maxFuzzInput {
			t.Skip("input over the fuzz cap")
		}
	}
}

// commandSeeds are the RFC 1939 §4-§7 and RFC 2449 §5 example command lines, plus
// boundary, case, control, high-byte and malformed variants.
var commandSeeds = []string{
	"", " ", "  ", "USER mrose", "PASS secret", "PASS tanstaaf",
	"APOP mrose c4c9334bac560ecc979e58001b3e22fb", "STAT", "LIST", "LIST 2",
	"RETR 1", "DELE 1", "NOOP", "RSET", "QUIT", "TOP 1 10", "UIDL", "UIDL 2",
	"CAPA", "STLS", "AUTH", "AUTH PLAIN", "AUTH PLAIN dGVzdAB0ZXN0AHRlc3Q=",
	"pass Secret", "PaSs MixedCaseSecret", "PASS  leading-space-secret",
	"PASS ", "PASS", " PASS secret", "PASS\tsecret", "apop user digest",
	"auth login", "paſs unicode-fold", "USER \x00\x01\x7f", "\xff\xfe PASS x",
	"RETR 1\r", "RETR 1\r\n", "user\nPASS x", "PASS <redacted>",
	"lıst", "quıt", "apoP\xff x", "\xffPASS x", "PASS\x80 x",
}

// rfcKeywords are the command keywords of RFC 1939 §4-§7, RFC 2449 and RFC 2595.
var rfcKeywords = []string{
	"USER", "PASS", "APOP", "QUIT", "STAT", "LIST", "RETR", "DELE", "NOOP",
	"RSET", "TOP", "UIDL", "CAPA", "AUTH", "STLS",
}

// asciiUpper upper-cases only ASCII letters, byte by byte.
func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// swapASCIICase flips the case of every ASCII letter and leaves other bytes alone.
func swapASCIICase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case 'a' <= c && c <= 'z':
			b[i] = c - ('a' - 'A')
		case 'A' <= c && c <= 'Z':
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// splitKeyword recovers the keyword wire.ParseCommand saw from its argument: RFC 1939
// §3 separates the keyword from its argument with a single SPACE, so whenever the
// line has a space the line must be keyword + " " + arg with no space in keyword.
func splitKeyword(t *testing.T, line, arg string) string {
	t.Helper()
	if arg == "" && !strings.Contains(line, " ") {
		return line
	}
	if !strings.HasSuffix(line, " "+arg) {
		t.Fatalf("line %q does not end in SPACE + arg %q", line, arg)
	}
	keyword := line[:len(line)-len(arg)-1]
	if strings.Contains(keyword, " ") {
		t.Fatalf("keyword %q of line %q contains a SPACE: arg %q is not the text after the first SPACE", keyword, line, arg)
	}
	return keyword
}

// FuzzParseCommand checks the RFC 1939 §3 command grammar: the keyword is
// case-insensitive and is everything before the first SPACE; the argument is
// everything after it, byte for byte (a password is case-sensitive).
func FuzzParseCommand(f *testing.F) {
	for _, s := range commandSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		skipOversized(t, line)
		cmd, arg := wire.ParseCommand(line)
		if strings.Contains(cmd, " ") {
			t.Fatalf("parseCommand(%q) keyword %q contains a SPACE", line, cmd)
		}
		keyword := splitKeyword(t, line, arg)
		// Keywords are ASCII (RFC 1939 §3), so case-insensitive means ASCII case:
		// a-z are upper-cased and every other byte is kept, never Unicode-mapped.
		if cmd != asciiUpper(keyword) {
			t.Fatalf("parseCommand(%q) keyword %q, want ASCII upper case %q", line, cmd, asciiUpper(keyword))
		}
		if !isASCII(keyword) && slices.Contains(rfcKeywords, cmd) {
			t.Fatalf("parseCommand(%q) turned non-ASCII keyword %q into the command %q", line, keyword, cmd)
		}
		// Changing the case of the keyword changes nothing; the argument is
		// returned verbatim, never case-mapped.
		rest := line[len(keyword):]
		cmd2, arg2 := wire.ParseCommand(swapASCIICase(keyword) + rest)
		if cmd2 != cmd || arg2 != arg {
			t.Fatalf("case-swapped keyword of %q parsed as (%q, %q), want (%q, %q)", line, cmd2, arg2, cmd, arg)
		}
	})
}

// credentialKeyword reports, independently of credentialCommands, whether a
// keyword names a command whose argument is a secret: PASS (RFC 1939 §7), APOP
// (RFC 1939 §7, a digest of the shared secret) and AUTH (RFC 5034, SASL).
func credentialKeyword(keyword string) bool {
	for _, k := range []string{"PASS", "APOP", "AUTH"} {
		if asciiUpper(keyword) == k {
			return true
		}
	}
	return false
}

// FuzzRedactCommand checks the log redaction policy. A line whose keyword is a
// credential command (PASS, APOP, AUTH) logs the same text whatever its argument;
// one whose keyword is another RFC command logs verbatim; and every other line
// logs the same text whatever it is, so nothing of a malformed line, which may be
// a credential the client mistyped, reaches the log.
func FuzzRedactCommand(f *testing.F) {
	for _, s := range commandSeeds {
		f.Add(s, "hunter2")
	}
	f.Add("PASS a", "a")
	f.Add("AUTH PLAIN", "AUTH PLAIN")
	// Credentials in lines the parser rejects as unknown commands (#374).
	for _, s := range []string{
		" PASS hunter2", "\tPASS hunter2", "PASS\thunter2", "paſs hunter2",
		"PАSS hunter2", "PASS\x80 hunter2", "hunter2", "APOP\talice hunter2",
		" AUTH PLAIN aHVudGVyMg==", "AUTH\u0131 PLAIN aHVudGVyMg==", "aHVudGVyMg==",
		"\x00canary:hunter2", "<unknown command redacted>",
	} {
		f.Add(s, "hunter2")
	}
	f.Fuzz(func(t *testing.T, line, other string) {
		skipOversized(t, line, other)
		got := wire.RedactCommand(line)
		cmd, arg := wire.ParseCommand(line)
		keyword := splitKeyword(t, line, arg)
		dispatchedAsCredential := cmd == "PASS" || cmd == "APOP" || cmd == "AUTH"
		if credentialKeyword(keyword) != dispatchedAsCredential {
			t.Fatalf("keyword %q of %q: credential command %v, but parsed as %q", keyword, line, credentialKeyword(keyword), cmd)
		}
		// canary starts with a NUL, so no keyword built from it is a command.
		canary := "\x00canary:" + other
		switch {
		case dispatchedAsCredential && arg == "":
			if got != line {
				t.Fatalf("redactCommand(%q) = %q, want the argument-less line verbatim", line, got)
			}
		case dispatchedAsCredential:
			// Non-interference: swapping the secret for any other non-empty
			// argument leaves the logged text unchanged, so the log carries
			// nothing of it.
			if leaked := wire.RedactCommand(keyword + " " + canary); leaked != got {
				t.Fatalf("redactCommand depends on the secret: %q -> %q but %q -> %q", line, got, keyword+" "+canary, leaked)
			}
			if strings.Contains(got, canary) {
				t.Fatalf("redactCommand(%q) = %q reveals the argument", line, got)
			}
			if !strings.EqualFold(got[:min(len(got), len(cmd))], cmd) {
				t.Fatalf("redactCommand(%q) = %q drops the keyword %q", line, got, cmd)
			}
		case slices.Contains(rfcKeywords, asciiUpper(keyword)):
			if got != line {
				t.Fatalf("redactCommand(%q) = %q, want the %s line verbatim", line, got, cmd)
			}
		default:
			// An unknown or malformed line may be a credential sent with the
			// wrong syntax: the log must not depend on any byte of it.
			if leaked := wire.RedactCommand(canary); leaked != got {
				t.Fatalf("redactCommand depends on an unknown line: %q -> %q but %q -> %q", line, got, canary, leaked)
			}
			if strings.Contains(got, canary) {
				t.Fatalf("redactCommand(%q) = %q reveals the line", line, got)
			}
		}
	})
}

// uidGrammar is RFC 1939 §7's unique-id: "1 to 70 characters in the range 0x21
// to 0x7E". Go's regexp reads invalid UTF-8 as U+FFFD, outside the class.
var uidGrammar = regexp.MustCompile(`^[\x21-\x7E]{1,70}$`)

// FuzzValidUID checks wire.ValidUID against the RFC 1939 §7 grammar written as a
// regular expression, and that a valid UID survives a UIDL scan line intact.
func FuzzValidUID(f *testing.F) {
	for _, s := range []string{
		"whqtswO00WBw418f9t5JxYwZ", "QhdPYR:00WBw1Ph7x7", // RFC 1939 §7 examples
		"", "!", "~", " ", "a b", "a\r", "a\n", "a\r\nb", "\x00", "\x20", "\x7f",
		"\x80", "é", "\xff", "<1896.697170952@dbc.mtview.ca.us>",
		strings.Repeat("x", 69), strings.Repeat("x", 70), strings.Repeat("x", 71),
		strings.Repeat("~", 70) + "\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, uid string) {
		skipOversized(t, uid)
		got := wire.ValidUID(uid)
		if want := uidGrammar.MatchString(uid); got != want {
			t.Fatalf("validUID(%q) = %v, RFC 1939 §7 grammar says %v", uid, got, want)
		}
		if !got {
			return
		}
		// A unique-id-listing is "msg SP unique-id CRLF": the client must read back
		// exactly two fields, the second being the UID.
		fields := strings.Fields("1 " + uid)
		if len(fields) != 2 || fields[1] != uid || strings.ContainsAny(uid, "\r\n") {
			t.Fatalf("valid UID %q does not survive a UIDL line: %q", uid, fields)
		}
	})
}

// messageSeeds hold dot lines, terminator look-alikes, bare CR, bare LF and
// CRLF mixes, missing final newline and binary bytes.
var messageSeeds = []string{
	"", "\r\n", "\n", "\r", "\r\r\n", "\n\r", ".", "..", ".\r\n", "..\r\n",
	"\r\n.\r\n", "a\r\n.\r\nb\r\n", "a\n.\nb\n", "a\r.\r\nb", "\n.\n",
	"Subject: hi\r\n\r\nbody\r\n", "Subject: hi\r\n\r\nbody",
	"From: a\nSubject: b\n\n.hidden\n..double\n", "end\r", "x\x00\xff\r\n",
	"Subject: x\r\n\r\n.\r\n\r\n.\r\n", ". \r\n.\t\r\n", "\r\n\r\n",
	"\r\nbody with no header fields\r\n", "\r\n\r\nX\r\n", "\nbody\n", "H: v\r\n\r\n",
}

// FuzzCanonicalCRLF checks wire.CanonicalCRLF against the stdlib two-pass
// normalization (CRLF to LF, then LF to CRLF) and its own invariants: no bare
// LF survives, a bare CR is kept, other bytes are untouched, and it is
// idempotent.
func FuzzCanonicalCRLF(f *testing.F) {
	for _, s := range messageSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		skipOversized(t, in)
		out := wire.CanonicalCRLF(in)
		if want := strings.ReplaceAll(strings.ReplaceAll(in, "\r\n", "\n"), "\n", "\r\n"); out != want {
			t.Fatalf("canonicalCRLF(%q) = %q, two-pass normalization gives %q", in, out, want)
		}
		for i := 0; i < len(out); i++ {
			if out[i] == '\n' && (i == 0 || out[i-1] != '\r') {
				t.Fatalf("canonicalCRLF(%q) = %q has a bare LF at %d", in, out, i)
			}
		}
		if again := wire.CanonicalCRLF(out); again != out {
			t.Fatalf("canonicalCRLF is not idempotent on %q: %q then %q", in, out, again)
		}
		if bareCRs(out) != bareCRs(in) {
			t.Fatalf("canonicalCRLF(%q) = %q changed the bare CR count", in, out)
		}
		if strings.ReplaceAll(out, "\r", "") != strings.ReplaceAll(in, "\r", "") {
			t.Fatalf("canonicalCRLF(%q) = %q changed bytes other than CR", in, out)
		}
		if len(out) != len(in)+bareLFs(in) {
			t.Fatalf("canonicalCRLF(%q) = %q: length %d, want %d", in, out, len(out), len(in)+bareLFs(in))
		}
	})
}

func bareCRs(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' && (i+1 == len(s) || s[i+1] != '\n') {
			n++
		}
	}
	return n
}

func bareLFs(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && (i == 0 || s[i-1] != '\r') {
			n++
		}
	}
	return n
}

// frameRetr builds the RETR multi-line body exactly as the session sends it:
// canonical CRLF, one wire line per message line, byte-stuffed, then ".".
func frameRetr(raw string) string {
	var b strings.Builder
	for _, line := range wire.SplitMessageLines(wire.CanonicalCRLF(raw)) {
		b.WriteString(wire.DotStuff(line))
		b.WriteString("\r\n")
	}
	b.WriteString(".\r\n")
	return b.String()
}

// readMultiLine is an independent RFC 1939 §3 client: it reads CRLF lines up to
// the line ".", removes one leading "." from any other line beginning with one,
// and returns the decoded message and whatever follows the terminator.
func readMultiLine(wire string) (msg, rest string, err error) {
	var b strings.Builder
	for {
		i := strings.Index(wire, "\r\n")
		if i < 0 {
			return "", "", errors.New("no terminating line")
		}
		line := wire[:i]
		wire = wire[i+2:]
		if strings.Contains(line, "\n") {
			return "", "", errors.New("bare LF inside a wire line")
		}
		if line == "." {
			return b.String(), wire, nil
		}
		b.WriteString(strings.TrimPrefix(line, "."))
		b.WriteString("\r\n")
	}
}

// TestRetrWireMatchesFrameRetr ties frameRetr to the engine: for every message
// seed, the bytes a real Session writes for RETR (over net.Pipe) are exactly
// frameRetr's, and the "+OK n octets" it advertises is OctetCount.
func TestRetrWireMatchesFrameRetr(t *testing.T) {
	m := newMockBackend()
	for i, raw := range messageSeeds {
		uid := fmt.Sprintf("u%d", i+1)
		m.seed(uid, pop3.OctetCount([]byte(raw)), raw)
		m.mbox.raws[uid] = []byte(raw) // seed skips "", an empty message is still one
	}
	h := newPOP3Harness(t, m)
	h.login()
	for i, raw := range messageSeeds {
		status := h.cmd("RETR %d", i+1)
		if want := fmt.Sprintf("+OK %d octets", pop3.OctetCount([]byte(raw))); status != want {
			t.Fatalf("RETR of %q: status %q, want %q", raw, status, want)
		}
		var wire strings.Builder
		for {
			_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			line, err := h.cr.ReadString('\n')
			if err != nil {
				t.Fatalf("RETR of %q: %v after %q", raw, err, wire.String())
			}
			wire.WriteString(line)
			if line == ".\r\n" {
				break
			}
		}
		if want := frameRetr(raw); wire.String() != want {
			t.Fatalf("RETR of %q sent %q, frameRetr gives %q", raw, wire.String(), want)
		}
	}
}

// FuzzMessageFraming checks RETR framing end to end: an RFC 1939 client reading
// the wire meets the terminator only at the end, gets the message back with
// every line, dot line and bare CR intact, and the decoded size matches the
// advertised OctetCount (RFC 1939 §11).
func FuzzMessageFraming(f *testing.F) {
	for _, s := range messageSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		skipOversized(t, string(raw))
		wire := frameRetr(string(raw))
		got, rest, err := readMultiLine(wire)
		if err != nil {
			t.Fatalf("framing %q: %v (wire %q)", raw, err, wire)
		}
		if rest != "" {
			t.Fatalf("framing %q terminated early, %q left after the terminator", raw, rest)
		}
		// The client sees the message with every end-of-line as CRLF, and a last
		// line stored without one ends in the CRLF that ends every line of a
		// multi-line response (RFC 1939 §3).
		want := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", "\r\n")
		if want != "" && !strings.HasSuffix(want, "\r\n") {
			want += "\r\n"
		}
		if got != want {
			t.Fatalf("framing %q decoded to %q, want %q", raw, got, want)
		}
		// The advertised size is what the client decoded: no stuffed dots, no
		// terminator (RFC 1939 §11).
		if n := pop3.OctetCount(raw); n != len(got) {
			t.Fatalf("OctetCount(%q) = %d, client received %d octets", raw, n, len(got))
		}
	})
}

// FuzzOctetCount checks OctetCount against a direct count of RFC 1939 §11
// octets (every octet once, a lone LF end-of-line as two, and two for the CRLF
// that ends an unterminated last line on the wire), and that counting is
// additive over concatenation except where a's last line runs into b.
func FuzzOctetCount(f *testing.F) {
	for _, s := range messageSeeds {
		f.Add([]byte(s), []byte("\n"))
	}
	f.Add([]byte("a\r"), []byte("\nb"))
	f.Add([]byte("a"), []byte("b\r\n"))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		skipOversized(t, string(a), string(b))
		if got, want := pop3.OctetCount(a), directOctets(a); got != want {
			t.Fatalf("OctetCount(%q) = %d, want %d", a, got, want)
		}
		joined := append(append([]byte{}, a...), b...)
		sum := pop3.OctetCount(a) + pop3.OctetCount(b)
		if len(a) > 0 && len(b) > 0 {
			if a[len(a)-1] != '\n' {
				sum -= 2 // a's last line continues into b, so it gains no CRLF of its own
			}
			if a[len(a)-1] == '\r' && b[0] == '\n' {
				sum-- // bare CR (1) + bare LF (2) become one CRLF (2)
			}
		}
		if got := pop3.OctetCount(joined); got != sum {
			t.Fatalf("OctetCount(%q) = %d, want %d from its parts", joined, got, sum)
		}
	})
}

// directOctets counts §11 octets without building the wire form: each stored
// octet once, one more for each LF not preceded by CR, and two for the CRLF a
// non-empty message not ending in LF is sent with.
func directOctets(raw []byte) int {
	n := len(raw) + bareLFs(string(raw))
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		n += 2
	}
	return n
}

// cutLines reads a canonical message line by line; a final line without CRLF
// is still a line.
func cutLines(msg string) []string {
	var lines []string
	for msg != "" {
		line, rest, _ := strings.Cut(msg, "\r\n")
		lines = append(lines, line)
		msg = rest
	}
	return lines
}

// frameLines sends lines as the session does: byte-stuffed, CRLF, then ".".
func frameLines(lines []string) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(wire.DotStuff(line))
		b.WriteString("\r\n")
	}
	b.WriteString(".\r\n")
	return b.String()
}

// FuzzTopFraming checks TOP (RFC 1939 §7) against a line model of RFC 5322
// §2.1: the header section runs to the first empty line, and TOP sends the
// headers, that empty line, then the first n body lines. A client reads it back
// with no early terminator; more lines only extend the reply; asking for every
// body line of a message that has a body separator yields exactly what RETR sends.
func FuzzTopFraming(f *testing.F) {
	for _, s := range messageSeeds {
		f.Add([]byte(s), uint16(0))
		f.Add([]byte(s), uint16(1))
		f.Add([]byte(s), uint16(1000))
	}
	f.Fuzz(func(t *testing.T, raw []byte, n16 uint16) {
		skipOversized(t, string(raw))
		n := int(n16)
		canon := wire.CanonicalCRLF(string(raw))
		got, rest, err := readMultiLine(frameLines(wire.TopLines(canon, n)))
		if err != nil || rest != "" {
			t.Fatalf("TOP %d of %q: err %v, %q after the terminator", n, raw, err, rest)
		}

		lines := cutLines(canon)
		header, body := lines, []string(nil)
		sep := -1
		for i, l := range lines {
			if l == "" {
				sep = i
				header, body = lines[:i], lines[i+1:]
				break
			}
		}
		want := append(append(append([]string{}, header...), ""), body[:min(n, len(body))]...)
		if wantWire := strings.Join(want, "\r\n") + "\r\n"; got != wantWire {
			t.Fatalf("TOP %d of %q decoded to %q, want %q", n, raw, got, wantWire)
		}

		more, _, _ := readMultiLine(frameLines(wire.TopLines(canon, n+1)))
		if !strings.HasPrefix(more, got) {
			t.Fatalf("TOP %d of %q (%q) is not a prefix of TOP %d (%q)", n, raw, got, n+1, more)
		}
		if sep >= 0 && n >= len(body) {
			if whole, _, _ := readMultiLine(frameRetr(string(raw))); got != whole {
				t.Fatalf("TOP %d of %q gave %q, RETR gives %q", n, raw, got, whole)
			}
		}
	})
}

// TestPOP3_TopEmptyHeaderSection is the regression for a message whose header
// section is empty (it begins with CRLF, RFC 5322 §2.1): TOP used to send its
// body as header lines, so "TOP 1 0" leaked the body and added a stray blank
// line. Found by FuzzTopFraming's model.
func TestPOP3_TopEmptyHeaderSection(t *testing.T) {
	const raw = "\r\nfirst body line\r\nsecond body line\r\n"
	m := newMockBackend()
	m.seed("1", len(raw), raw)
	h := newPOP3Harness(t, m)
	h.login()

	for _, tc := range []struct {
		n    int
		want []string
	}{
		{0, []string{""}},
		{1, []string{"", "first body line"}},
		{5, []string{"", "first body line", "second body line"}},
	} {
		if got := h.cmd("TOP 1 %d", tc.n); !strings.HasPrefix(got, "+OK") {
			t.Fatalf("TOP 1 %d status = %q", tc.n, got)
		}
		if got := h.readDotBody(); strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("TOP 1 %d = %q, want %q", tc.n, got, tc.want)
		}
	}
}
