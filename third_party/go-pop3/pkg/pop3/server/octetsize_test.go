package server

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
	pop3wire "github.com/rest-mail/go-pop3/pkg/pop3/internal/wire"
)

// These tests pin the RFC 1939 §11 octet-count contract: the size STAT and LIST
// report for a message MUST be the exact number of octets RETR transmits for it,
// measured on the canonical CRLF wire form. RFC 1939 §11 defines that count by
// normalizing the stored end-of-line convention to CRLF ("counts each occurrence
// of this character ... as two octets"); the §3 byte-stuffing of leading-dot
// lines and the CRLF.CRLF terminator are transport framing and are NOT part of
// the message's octet count. [pop3.OctetCount] is the single source of truth backends
// use so LIST/STAT and RETR agree.

// mixedRaw exercises every way the stored bytes and the wire octet count can
// diverge from a naive len(): a bare-LF line ending (CRLF normalization adds an
// octet) and a leading-dot line (byte-stuffed on the wire, but counted once in
// the size).
const mixedRaw = "Subject: t\r\n" + // CRLF header (12)
	"\r\n" + // CRLF separator (2)
	"line1\n" + // BARE LF: +1 octet once normalized to CRLF
	".hidden\r\n" + // leading dot: byte-stuffed on the wire, counted once here
	"end\r\n" // (5)

// unstuff reverses RFC 1939 §3 byte-stuffing: a wire line beginning with "."
// carried a prepended termination octet the client strips.
func unstuff(line string) string {
	if strings.HasPrefix(line, ".") {
		return line[1:]
	}
	return line
}

// reconstructWire rebuilds the message octets a client receives from a RETR dot
// body: every line was CRLF-terminated and any leading-dot line was stuffed.
func reconstructWire(body []string) string {
	var b strings.Builder
	for _, l := range body {
		b.WriteString(unstuff(l))
		b.WriteString("\r\n")
	}
	return b.String()
}

// retrOctets sends RETR n and returns the advertised octet count from the
// "+OK <n> octets" status line together with the reconstructed message octets.
func retrOctets(t *testing.T, h *pop3Harness, n int) (advertised int, wire string) {
	t.Helper()
	status := h.cmd("RETR %d", n)
	fields := strings.Fields(status)
	if len(fields) < 2 || fields[0] != "+OK" {
		t.Fatalf("RETR %d status = %q, want \"+OK <n> octets\"", n, status)
	}
	adv, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("RETR %d advertised count %q not an integer: %v", n, fields[1], err)
	}
	return adv, reconstructWire(h.readDotBody())
}

// TestOctetCount_IsCanonicalWireLength pins OctetCount's semantics: it must
// return the CRLF-normalized message length (RFC 1939 §11), NOT the raw stored
// length. For mixedRaw the two differ by the bare LF, so an implementation that
// merely returned len(raw) would fail here.
func TestOctetCount_IsCanonicalWireLength(t *testing.T) {
	got := pop3.OctetCount([]byte(mixedRaw))
	want := len(pop3wire.CanonicalCRLF(mixedRaw))
	if got != want {
		t.Fatalf("OctetCount = %d, want canonical CRLF length %d", got, want)
	}
	if got == len(mixedRaw) {
		t.Fatalf("OctetCount = %d equals raw len; bare LF was not normalized to CRLF", got)
	}
}

// TestListStatAgreeWithRetr proves that when a backend sizes each message with
// OctetCount (the documented contract), LIST's per-message size, STAT's total,
// and RETR's advertised count all equal the octets RETR actually transmits.
func TestListStatAgreeWithRetr(t *testing.T) {
	const second = "Only headers\r\n" // headers-only, CRLF throughout

	m := newMockBackend()
	m.seed("1", pop3.OctetCount([]byte(mixedRaw)), mixedRaw)
	m.seed("2", pop3.OctetCount([]byte(second)), second)

	h := newPOP3Harness(t, m)
	h.login()

	// STAT total must equal the sum of the true octet counts.
	stat := h.cmd("STAT")
	wantTotal := pop3.OctetCount([]byte(mixedRaw)) + pop3.OctetCount([]byte(second))
	if want := "+OK 2 " + strconv.Itoa(wantTotal); stat != want {
		t.Fatalf("STAT = %q, want %q", stat, want)
	}

	// LIST per-message sizes.
	if r := h.cmd("LIST"); !strings.HasPrefix(r, "+OK") {
		t.Fatalf("LIST status = %q", r)
	}
	listSizes := map[int]int{}
	for _, l := range h.readDotBody() {
		f := strings.Fields(l)
		n, _ := strconv.Atoi(f[0])
		sz, _ := strconv.Atoi(f[1])
		listSizes[n] = sz
	}

	// For each message: LIST size == RETR advertised == transmitted message octets.
	for n, raw := range map[int]string{1: mixedRaw, 2: second} {
		adv, wire := retrOctets(t, h, n)
		want := pop3.OctetCount([]byte(raw))
		if listSizes[n] != want {
			t.Errorf("msg %d: LIST size = %d, want %d", n, listSizes[n], want)
		}
		if adv != want {
			t.Errorf("msg %d: RETR advertised %d, want %d", n, adv, want)
		}
		if len(wire) != want {
			t.Errorf("msg %d: RETR transmitted %d message octets, want %d (wire=%q)", n, len(wire), want, wire)
		}
		if wire != pop3wire.CanonicalCRLF(raw) {
			t.Errorf("msg %d: reconstructed wire %q != canonical form %q", n, wire, pop3wire.CanonicalCRLF(raw))
		}
	}
}

// TestNaiveStoredSizeUnderReports characterizes the hazard the tightened
// contract forbids: a backend that reports the raw stored length of bare-LF
// content under-reports relative to what RETR transmits, so LIST/STAT would
// disagree with RETR. OctetCount is the fix — it counts what the wire carries.
func TestNaiveStoredSizeUnderReports(t *testing.T) {
	naive := len(mixedRaw)                       // what a backend using raw len would report
	correct := pop3.OctetCount([]byte(mixedRaw)) // what RETR advertises and transmits
	if naive >= correct {
		t.Fatalf("expected raw len %d < canonical octet count %d", naive, correct)
	}

	m := newMockBackend()
	m.seed("1", naive, mixedRaw) // deliberately mis-sized backend
	h := newPOP3Harness(t, m)
	h.login()

	list := h.cmd("LIST 1")
	adv, _ := retrOctets(t, h, 1)
	// The mis-sized backend makes LIST disagree with RETR; sizing via OctetCount
	// (as the contract now requires) is what makes them agree.
	if got := "+OK 1 " + strconv.Itoa(naive); list != got {
		t.Fatalf("LIST 1 = %q, want %q", list, got)
	}
	if adv == naive {
		t.Fatalf("RETR advertised the naive size %d; expected the canonical %d", naive, correct)
	}
}

// octetCases are stored messages whose advertised size and decoded RETR length
// have to agree: each end-of-line convention, a missing final end-of-line, bare
// CR, dot-leading and dot-only lines, and binary and non-ASCII bytes. decoded is
// the message an RFC 1939 client gets back, written out by hand: every
// end-of-line is CRLF, and a last line stored without one ends in CRLF.
var octetCases = []struct{ name, raw, decoded string }{
	{"empty", "", ""},
	{"crlf", "Subject: a\r\n\r\nbody\r\n", "Subject: a\r\n\r\nbody\r\n"},
	{"unterminated", "Subject: a\r\n\r\nbody", "Subject: a\r\n\r\nbody\r\n"},
	{"unterminated header only", "Subject: a", "Subject: a\r\n"},
	{"lf only", "Subject: a\n\nbody\n", "Subject: a\r\n\r\nbody\r\n"},
	{"lf unterminated", "Subject: a\n\nbody", "Subject: a\r\n\r\nbody\r\n"},
	{"mixed", "Subject: a\r\n\nline\nend\r\n", "Subject: a\r\n\r\nline\r\nend\r\n"},
	{"bare cr inside", "Subject: a\r\n\r\nx\ry\r\n", "Subject: a\r\n\r\nx\ry\r\n"},
	{"bare cr at end", "Subject: a\r\n\r\nend\r", "Subject: a\r\n\r\nend\r\r\n"},
	{"lone cr", "\r", "\r\r\n"},
	{"dot lines", "Subject: a\r\n\r\n.\r\n..\r\n.x\r\n", "Subject: a\r\n\r\n.\r\n..\r\n.x\r\n"},
	{"dot only unterminated", ".", ".\r\n"},
	{"dot after lf", "a\n.\nb", "a\r\n.\r\nb\r\n"},
	{"binary", "Subject: a\r\n\r\n\x00\x01\xff\xfe\r\n", "Subject: a\r\n\r\n\x00\x01\xff\xfe\r\n"},
	{"non-ascii unterminated", "Subject: caf\xc3\xa9\r\n\r\nna\xc3\xafve \xe2\x9c\x93", "Subject: caf\xc3\xa9\r\n\r\nna\xc3\xafve \xe2\x9c\x93\r\n"},
}

// decodeRetr reads one RETR body off the wire as an RFC 1939 §3 client does,
// without the harness's line helpers: every wire line must end in CRLF, the
// line "." ends the body, and one leading "." is removed from any other line.
// It returns the message octets the client ends up with.
func decodeRetr(t *testing.T, h *pop3Harness) string {
	t.Helper()
	var msg strings.Builder
	for {
		_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := h.cr.ReadString('\n')
		if err != nil {
			t.Fatalf("RETR body: %v after %q", err, msg.String())
		}
		body, ok := strings.CutSuffix(line, "\r\n")
		if !ok || strings.Contains(body, "\n") {
			t.Fatalf("RETR body: wire line %q does not end in CRLF", line)
		}
		if body == "." {
			return msg.String()
		}
		msg.WriteString(strings.TrimPrefix(body, "."))
		msg.WriteString("\r\n")
	}
}

// TestAdvertisedOctetsMatchDecodedRetr checks RFC 1939 §5 and §11 over a real
// session: for every stored message sized with OctetCount, RETR decodes to the
// expected message; LIST n, the LIST scan listing and RETR's "+OK n octets" all
// equal the octets the client decoded; STAT's total is their sum; and the
// command after each RETR gets its own reply (the body and terminator were
// exactly where the client looked).
// Byte-stuffed dots and the terminating ".\r\n" are not counted.
func TestAdvertisedOctetsMatchDecodedRetr(t *testing.T) {
	m := newMockBackend()
	for i, c := range octetCases {
		uid := fmt.Sprintf("u%d", i+1)
		m.seed(uid, pop3.OctetCount([]byte(c.raw)), c.raw)
		m.mbox.raws[uid] = []byte(c.raw) // seed skips "", an empty message is still one
	}
	h := newPOP3Harness(t, m)
	h.login()

	decoded := make([]string, len(octetCases))
	total := 0
	for i, c := range octetCases {
		n := i + 1
		status := h.cmd("RETR %d", n)
		decoded[i] = decodeRetr(t, h)
		total += len(decoded[i])
		if decoded[i] != c.decoded {
			t.Errorf("%s: RETR decoded to %q, want %q", c.name, decoded[i], c.decoded)
		}
		if want := fmt.Sprintf("+OK %d octets", len(decoded[i])); status != want {
			t.Errorf("%s: RETR status %q, client decoded %d octets %q", c.name, status, len(decoded[i]), decoded[i])
		}
		if r := h.cmd("NOOP"); r != "+OK" {
			t.Fatalf("%s: NOOP after RETR got %q, the RETR framing is off", c.name, r)
		}
		if want := fmt.Sprintf("+OK %d %d", n, len(decoded[i])); h.cmd("LIST %d", n) != want {
			t.Errorf("%s: LIST %d disagrees with RETR, want %q", c.name, n, want)
		}
	}

	if want := fmt.Sprintf("+OK %d %d", len(octetCases), total); h.cmd("STAT") != want {
		t.Errorf("STAT disagrees with the RETR total, want %q", want)
	}
	if r := h.cmd("LIST"); !strings.HasPrefix(r, "+OK") {
		t.Fatalf("LIST status = %q", r)
	}
	scan := h.readDotBody()
	if len(scan) != len(octetCases) {
		t.Fatalf("LIST listed %d messages, want %d", len(scan), len(octetCases))
	}
	for i, l := range scan {
		if want := fmt.Sprintf("%d %d", i+1, len(decoded[i])); l != want {
			t.Errorf("%s: LIST line %q, want %q", octetCases[i].name, l, want)
		}
	}
	if r := h.cmd("NOOP"); r != "+OK" {
		t.Fatalf("NOOP after LIST got %q", r)
	}
}
