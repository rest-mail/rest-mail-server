package imap

import (
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// ── FETCH literal helpers (extend the transcript harness) ──────────────────

// fetchSizeAndBodyLen issues FETCH <seq> (RFC822.SIZE BODY.PEEK[]) and returns
// the reported RFC822.SIZE and the actual octet length of the returned BODY[]
// literal, consuming the whole response through the tagged completion line. It
// is how a test proves RFC822.SIZE equals len(BODY[]) end-to-end over the wire.
func (h *transcript) fetchSizeAndBodyLen(tag string, seq int) (size, bodyLen int) {
	h.t.Helper()
	h.send("%s FETCH %d (RFC822.SIZE BODY.PEEK[])", tag, seq)

	// The untagged FETCH line ends with the BODY[] literal marker "{N}"; the N
	// literal octets follow on the next line(s).
	line := h.readLine()
	size = intAfter(h.t, line, "RFC822.SIZE ")
	bodyLen = braceCount(h.t, line)

	// Consume exactly bodyLen literal octets, then drain to the tagged line.
	h.readN(bodyLen)
	for {
		if l := h.readLine(); strings.HasPrefix(l, tag+" ") {
			break
		}
	}
	return size, bodyLen
}

// readN consumes exactly n raw octets from the connection (an IMAP literal body).
func (h *transcript) readN(n int) {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(h.cr, make([]byte, n)); err != nil {
		h.t.Fatalf("readN(%d): %v", n, err)
	}
}

// intAfter parses the run of decimal digits that immediately follows key in line.
func intAfter(t *testing.T, line, key string) int {
	t.Helper()
	i := strings.Index(line, key)
	if i < 0 {
		t.Fatalf("%q not found in %q", key, line)
	}
	rest := line[i+len(key):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		t.Fatalf("bad integer after %q in %q: %v", key, line, err)
	}
	return n
}

// braceCount parses the octet count from the trailing IMAP literal marker "{N}".
func braceCount(t *testing.T, line string) int {
	t.Helper()
	i := strings.LastIndex(line, "{")
	if i < 0 {
		t.Fatalf("no literal marker in %q", line)
	}
	j := strings.Index(line[i:], "}")
	if j < 0 {
		t.Fatalf("unterminated literal marker in %q", line)
	}
	n, err := strconv.Atoi(line[i+1 : i+j])
	if err != nil {
		t.Fatalf("bad literal count in %q: %v", line, err)
	}
	return n
}

// ── ENVELOPE / BODYSTRUCTURE / date wiring (go-imap v0.4.0) ────────────────

// envelopeRaw is a CRLF message whose recipient/reference headers and Date all
// differ from the neutral summary the fake API returns, so a green assertion can
// only mean the engine parsed the RAW the gateway serves (via Fetch), not the
// neutral Message model. Its Date header (2030) is deliberately distinct from
// the arrival time the fake stamps (fakeReceivedAt, 2025).
const envelopeRaw = "Message-ID: <root@example.com>\r\n" +
	"In-Reply-To: <parent@example.com>\r\n" +
	"Date: Wed, 01 Jan 2030 12:30:00 +0000\r\n" +
	"From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.net>, carol@example.org\r\n" +
	"Cc: Dave <dave@example.com>\r\n" +
	"Subject: Envelope test\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello, this is the body.\r\n"

// TestServer_Fetch_EnvelopeBodyStructureFromRaw proves the go-imap v0.4.0 bump
// populates ENVELOPE (To/Cc/In-Reply-To/Message-ID and the Date-from-header) and
// BODYSTRUCTURE straight from the raw RFC822 the gateway already serves for
// BODY[] — no gateway change needed, because Fetch already returns the stored
// raw. It also proves ENVELOPE's date is the message's Date: header while
// INTERNALDATE stays the arrival time (the two are distinct, RFC 3501 §7.4.2 vs
// §2.3.3). Closes the ENVELOPE/BODYSTRUCTURE items of rest-mail-server #200.
func TestServer_Fetch_EnvelopeBodyStructureFromRaw(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", envelopeRaw, "Envelope test")

	h := newBackendTranscript(t, api)
	h.login("a1")
	if _, st := h.command("a2", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}

	unt, st := h.command("a3", "FETCH 1 (ENVELOPE BODYSTRUCTURE INTERNALDATE)")
	if !strings.Contains(st, "OK") {
		t.Fatalf("FETCH status = %q", st)
	}
	resp := strings.Join(unt, "\n")

	// The engine split the ENVELOPE at the "ENVELOPE (" opener; assert against the
	// remainder so an address that happens to appear in a header line elsewhere
	// cannot give a false positive.
	env := resp
	if i := strings.Index(resp, "ENVELOPE ("); i >= 0 {
		env = resp[i:]
	}

	// ENVELOPE date comes from the Date: header (2030), not the arrival time.
	if !strings.Contains(env, "2030") {
		t.Errorf("ENVELOPE date not from Date: header (want 2030): %q", env)
	}
	// To is populated (both recipients), Cc is populated — previously always NIL.
	for _, want := range []string{"bob", "example.net", "carol", "example.org"} {
		if !strings.Contains(env, want) {
			t.Errorf("ENVELOPE To missing %q: %q", want, env)
		}
	}
	if !strings.Contains(env, "dave") {
		t.Errorf("ENVELOPE Cc not populated (want dave): %q", env)
	}
	// In-Reply-To and Message-ID references are populated.
	if !strings.Contains(env, "<parent@example.com>") {
		t.Errorf("ENVELOPE In-Reply-To not populated: %q", env)
	}
	if !strings.Contains(env, "<root@example.com>") {
		t.Errorf("ENVELOPE Message-ID not populated: %q", env)
	}

	// BODYSTRUCTURE is emitted and describes the text/plain part — not a blanket NIL.
	if !strings.Contains(resp, `"TEXT" "PLAIN"`) {
		t.Errorf("BODYSTRUCTURE not populated from raw (want TEXT/PLAIN): %q", resp)
	}

	// INTERNALDATE is the arrival time the fake stamped (fakeReceivedAt, 2025),
	// distinct from the ENVELOPE date, confirming the two are not conflated.
	if !strings.Contains(resp, "INTERNALDATE \"15-Mar-2025") {
		t.Errorf("INTERNALDATE not the arrival time (want 15-Mar-2025): %q", resp)
	}
}

// TestServer_Fetch_RFC822SizeMatchesBody proves FETCH RFC822.SIZE equals the
// actual octet length of BODY[] for a stored message: the gateway reports the
// exact stored-raw size (WireSize) and serves those same bytes verbatim
// (rest-mail-server #200 RFC822.SIZE item).
func TestServer_Fetch_RFC822SizeMatchesBody(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", envelopeRaw, "Envelope test")

	h := newBackendTranscript(t, api)
	h.login("a1")
	if _, st := h.command("a2", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}

	size, bodyLen := h.fetchSizeAndBodyLen("a3", 1)
	if size != bodyLen {
		t.Errorf("RFC822.SIZE = %d, len(BODY[]) = %d; they must be equal", size, bodyLen)
	}
	if size != len(envelopeRaw) {
		t.Errorf("RFC822.SIZE = %d, want %d (exact stored-raw octet count)", size, len(envelopeRaw))
	}
}

// TestServer_Fetch_RFC822SizeMatchesBody_AppendBareLF proves the size invariant
// survives APPEND's LF→CRLF normalization: a message APPENDed with bare-LF line
// endings is stored CRLF-normalized, and FETCH RFC822.SIZE must equal
// len(BODY[]) of that normalized form — not the shorter pre-normalization size.
func TestServer_Fetch_RFC822SizeMatchesBody_AppendBareLF(t *testing.T) {
	api := newFakeAPI()

	h := newBackendTranscript(t, api)
	h.login("a1")

	// Bare-LF raw: the gateway normalizes each LF to CRLF at ingest, so the stored
	// (and served) message is longer than what we APPEND here.
	bareLF := "Subject: Bare LF\nFrom: a@b.test\nTo: c@d.test\n\nline one\nline two\n"
	if _, st := h.appendMsg("a2", "INBOX", bareLF); !strings.Contains(st, "OK") {
		t.Fatalf("APPEND status = %q", st)
	}

	// Re-SELECT so the newly APPENDed message (id 100) is in the engine's cached
	// message list as sequence 1.
	if _, st := h.command("a3", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}

	size, bodyLen := h.fetchSizeAndBodyLen("a4", 1)
	if size != bodyLen {
		t.Errorf("APPEND bare-LF: RFC822.SIZE = %d, len(BODY[]) = %d; they must be equal", size, bodyLen)
	}
	wantNormalized := len(normalizeToCRLF([]byte(bareLF)))
	if size != wantNormalized {
		t.Errorf("RFC822.SIZE = %d, want %d (CRLF-normalized stored size)", size, wantNormalized)
	}
	if size == len(bareLF) {
		t.Errorf("RFC822.SIZE = %d equals the pre-normalization length; normalization was not reflected in the size", size)
	}
}

// newBackendTranscript wires a transcript straight to a Backend over the fakeAPI,
// the common setup the ENVELOPE/SIZE tests share.
func newBackendTranscript(t *testing.T, api *fakeAPI) *transcript {
	t.Helper()
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	return newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
}
