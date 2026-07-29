package e2e

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// smtpConn wraps a plain TCP connection to an SMTP server.
type smtpConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialSMTP(t *testing.T, addr string) *smtpConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("dial SMTP %s: %v", addr, err)
	}
	sc := &smtpConn{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
	// Read greeting
	greeting := sc.readLine(t)
	if !strings.HasPrefix(greeting, "220") {
		t.Fatalf("expected 220 greeting from %s, got: %s", addr, greeting)
	}
	return sc
}

func (sc *smtpConn) close() {
	sc.conn.Close()
}

func (sc *smtpConn) send(t *testing.T, cmd string) {
	t.Helper()
	_ = sc.conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := fmt.Fprintf(sc.conn, "%s\r\n", cmd)
	if err != nil {
		t.Fatalf("send SMTP command %q: %v", cmd, err)
	}
}

func (sc *smtpConn) readLine(t *testing.T) string {
	t.Helper()
	_ = sc.conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := sc.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SMTP line: %v", err)
	}
	return strings.TrimSpace(line)
}

// readMultiLine reads a multi-line SMTP response (e.g. EHLO).
func (sc *smtpConn) readMultiLine(t *testing.T) []string {
	t.Helper()
	var lines []string
	for {
		line := sc.readLine(t)
		lines = append(lines, line)
		// Multi-line responses have a dash after the code (e.g. "250-SIZE")
		// The last line has a space (e.g. "250 OK")
		if len(line) >= 4 && line[3] == ' ' {
			break
		}
	}
	return lines
}

// sendExpect sends a command and expects a specific response code prefix.
func (sc *smtpConn) sendExpect(t *testing.T, cmd string, expectedCode string) string {
	t.Helper()
	sc.send(t, cmd)
	resp := sc.readLine(t)
	if !strings.HasPrefix(resp, expectedCode) {
		t.Fatalf("SMTP %q: expected %s, got: %s", cmd, expectedCode, resp)
	}
	return resp
}

// ehlo sends EHLO and returns the capability lines.
func (sc *smtpConn) ehlo(t *testing.T, domain string) []string {
	t.Helper()
	sc.send(t, "EHLO "+domain)
	return sc.readMultiLine(t)
}

// hasCapability checks if an EHLO response includes a specific capability.
func hasCapability(lines []string, cap string) bool {
	cap = strings.ToUpper(cap)
	for _, line := range lines {
		upper := strings.ToUpper(line)
		// Line is like "250-PIPELINING" or "250 PIPELINING"
		if len(upper) > 4 {
			if strings.HasPrefix(upper[4:], cap) {
				return true
			}
		}
	}
	return false
}

// sendMail sends a complete email via SMTP.
func sendMailViaSMTP(t *testing.T, smtpAddr, from, to, subject, body string) {
	t.Helper()
	// Greylisting defers the first attempt for a new (sender, recipient, ip)
	// triplet with 451 — exactly like the real internet. The e2e pipeline runs
	// with a zero-length greylist window (see ensureE2EPipeline), so an
	// immediate retry passes; retry until the deadline for robustness.
	deadline := time.Now().Add(30 * time.Second)
	for {
		sc := dialSMTP(t, smtpAddr)
		// Upgrade whenever the peer offers it. rest-mail refuses a transaction on a
		// cleartext session, and the reference MTAs accept the upgrade happily — which is
		// also what a real sending MTA does.
		sc.starttlsIfOffered(t, "test.local")
		sc.sendExpect(t, "MAIL FROM:<"+from+">", "250")
		sc.sendExpect(t, "RCPT TO:<"+to+">", "250")
		sc.sendExpect(t, "DATA", "354")

		msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <test-%d@test.local>\r\n\r\n%s",
			from, to, subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano(), body)

		sc.send(t, msg)
		sc.send(t, ".")
		resp := sc.readLine(t)
		sc.send(t, "QUIT")
		sc.close()

		switch {
		case strings.HasPrefix(resp, "250"):
			return
		case strings.HasPrefix(resp, "451") && time.Now().Before(deadline):
			t.Logf("greylisted for %s, retrying: %s", to, resp)
			time.Sleep(1 * time.Second)
		default:
			t.Fatalf("SMTP DATA for %s not accepted: %s", to, resp)
		}
	}
}

// starttls upgrades the SMTP connection to TLS.
func (sc *smtpConn) starttls(t *testing.T) {
	t.Helper()
	sc.sendExpect(t, "STARTTLS", "220")
	tlsConn := tls.Client(sc.conn, &tls.Config{InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("SMTP TLS handshake failed: %v", err)
	}
	sc.conn = tlsConn
	sc.reader = bufio.NewReader(tlsConn)
}

// starttlsIfOffered EHLOs and upgrades when the peer advertises STARTTLS, then EHLOs
// again because the extension list changes after an upgrade.
//
// Opportunistic on purpose: it is used against both rest-mail, which refuses a cleartext
// transaction, and the reference MTAs, where the upgrade is optional. A sending MTA on
// the real internet behaves the same way.
func (sc *smtpConn) starttlsIfOffered(t *testing.T, domain string) {
	t.Helper()
	caps := sc.ehlo(t, domain)
	if !hasCapability(caps, "STARTTLS") {
		return
	}
	sc.starttls(t)
	sc.ehlo(t, domain)
}

// authPlain sends AUTH PLAIN with base64-encoded credentials.
func (sc *smtpConn) authPlain(t *testing.T, user, pass string) {
	t.Helper()
	cred := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
	sc.sendExpect(t, "AUTH PLAIN "+cred, "235")
}

// sendMailViaSubmission sends an email via a STARTTLS submission port (587) with AUTH
// PLAIN. This is for the REFERENCE servers, which still run cleartext-then-upgrade
// submission — rest-mail's submission is implicit TLS on 465, reached with dialSMTPTLS.
func sendMailViaSubmission(t *testing.T, submitAddr, from, to, user, pass, subject, body string) {
	t.Helper()
	sc := dialSMTP(t, submitAddr)
	defer sc.close()

	caps := sc.ehlo(t, "test.local")
	if !hasCapability(caps, "STARTTLS") {
		t.Fatalf("submission port does not advertise STARTTLS")
	}

	sc.starttls(t)
	caps = sc.ehlo(t, "test.local")
	if !hasCapability(caps, "AUTH") {
		t.Fatalf("submission port does not advertise AUTH after STARTTLS")
	}

	sc.authPlain(t, user, pass)
	sc.sendExpect(t, "MAIL FROM:<"+from+">", "250")
	sc.sendExpect(t, "RCPT TO:<"+to+">", "250")
	sc.sendExpect(t, "DATA", "354")

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <submit-%d@test.local>\r\n\r\n%s",
		from, to, subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano(), body)

	sc.send(t, msg)
	sc.sendExpect(t, ".", "250")
	sc.sendExpect(t, "QUIT", "221")
}

// sendRawMailViaSMTP sends a pre-built raw MIME message via SMTP.
// The rawMsg should contain all headers and body (From, To, Subject, MIME parts, etc.).
func sendRawMailViaSMTP(t *testing.T, smtpAddr, from, to, rawMsg string) {
	t.Helper()
	sc := dialSMTP(t, smtpAddr)
	defer sc.close()
	// starttlsIfOffered has already sent EHLO, and again after the upgrade.
	sc.starttlsIfOffered(t, "test.local")
	sc.sendExpect(t, "MAIL FROM:<"+from+">", "250")
	sc.sendExpect(t, "RCPT TO:<"+to+">", "250")
	sc.sendExpect(t, "DATA", "354")

	sc.send(t, rawMsg)
	sc.sendExpect(t, ".", "250")
	sc.sendExpect(t, "QUIT", "221")
}

// ── IMAP helper ──────────────────────────────────────────────────────

type imapConn struct {
	conn   net.Conn
	reader *bufio.Reader
	tag    int
}

func dialIMAP(t *testing.T, addr string) *imapConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("dial IMAP %s: %v", addr, err)
	}
	ic := &imapConn{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
	// Read greeting
	greeting := ic.readLine(t)
	if !strings.HasPrefix(greeting, "* OK") {
		t.Fatalf("expected IMAP greeting from %s, got: %s", addr, greeting)
	}
	return ic
}

func (ic *imapConn) close() {
	ic.conn.Close()
}

func (ic *imapConn) nextTag() string {
	ic.tag++
	return fmt.Sprintf("A%03d", ic.tag)
}

func (ic *imapConn) readLine(t *testing.T) string {
	t.Helper()
	_ = ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := ic.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read IMAP line: %v", err)
	}
	return strings.TrimSpace(line)
}

// readUntilTag reads lines until it gets one starting with the given tag.
func (ic *imapConn) readUntilTag(t *testing.T, tag string) []string {
	t.Helper()
	var lines []string
	for {
		line := ic.readLine(t)
		lines = append(lines, line)
		if strings.HasPrefix(line, tag+" ") {
			break
		}
	}
	return lines
}

// command sends an IMAP command and returns all response lines.
func (ic *imapConn) command(t *testing.T, cmd string) (string, []string) {
	t.Helper()
	tag := ic.nextTag()
	_ = ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := fmt.Fprintf(ic.conn, "%s %s\r\n", tag, cmd)
	if err != nil {
		t.Fatalf("send IMAP command %q: %v", cmd, err)
	}
	lines := ic.readUntilTag(t, tag)
	lastLine := lines[len(lines)-1]
	return lastLine, lines
}

// login logs in to the IMAP server.
func (ic *imapConn) login(t *testing.T, user, pass string) {
	t.Helper()
	result, _ := ic.command(t, fmt.Sprintf("LOGIN %s %s", user, pass))
	if !strings.Contains(result, "OK") {
		t.Fatalf("IMAP LOGIN failed: %s", result)
	}
}

// fetchBody sends FETCH n (BODY[]) and returns all response lines joined.
func (ic *imapConn) fetchBody(t *testing.T, seqNum int) string {
	t.Helper()
	_, lines := ic.command(t, fmt.Sprintf("FETCH %d (BODY[])", seqNum))
	// Join all untagged lines (everything except the final tag OK line)
	var body strings.Builder
	for _, line := range lines[:len(lines)-1] {
		body.WriteString(line)
		body.WriteString("\n")
	}
	return body.String()
}

// ── POP3 helper ──────────────────────────────────────────────────────

type pop3Conn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialPOP3(t *testing.T, addr string) *pop3Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("dial POP3 %s: %v", addr, err)
	}
	pc := &pop3Conn{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
	greeting := pc.readLine(t)
	if !strings.HasPrefix(greeting, "+OK") {
		t.Fatalf("expected POP3 +OK greeting from %s, got: %s", addr, greeting)
	}
	return pc
}

func (pc *pop3Conn) close() {
	pc.conn.Close()
}

func (pc *pop3Conn) readLine(t *testing.T) string {
	t.Helper()
	_ = pc.conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := pc.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read POP3 line: %v", err)
	}
	return strings.TrimSpace(line)
}

func (pc *pop3Conn) sendExpect(t *testing.T, cmd string, expectedPrefix string) string {
	t.Helper()
	_ = pc.conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := fmt.Fprintf(pc.conn, "%s\r\n", cmd)
	if err != nil {
		t.Fatalf("send POP3 %q: %v", cmd, err)
	}
	resp := pc.readLine(t)
	if !strings.HasPrefix(resp, expectedPrefix) {
		t.Fatalf("POP3 %q: expected %s, got: %s", cmd, expectedPrefix, resp)
	}
	return resp
}

// capa sends CAPA and returns the capability lines.
func (pc *pop3Conn) capa(t *testing.T) []string {
	t.Helper()
	pc.sendExpect(t, "CAPA", "+OK")
	var caps []string
	for {
		line := pc.readLine(t)
		if line == "." {
			break
		}
		caps = append(caps, line)
	}
	return caps
}

// retr retrieves a full message by number.
func (pc *pop3Conn) retr(t *testing.T, msgNum int) string {
	t.Helper()
	pc.sendExpect(t, fmt.Sprintf("RETR %d", msgNum), "+OK")
	var body strings.Builder
	for {
		line := pc.readLine(t)
		if line == "." {
			break
		}
		// Byte-unstuff (RFC 1939 §3)
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	return body.String()
}

// stat sends STAT and returns the response.
func (pc *pop3Conn) stat(t *testing.T) string {
	t.Helper()
	return pc.sendExpect(t, "STAT", "+OK")
}

// ── DNS helper ───────────────────────────────────────────────────────

func resolveDomain(t *testing.T, domain string) []string {
	t.Helper()
	// Use net.LookupHost which will use the system resolver.
	// In the Docker environment, containers use dnsmasq (10.99.0.3).
	addrs, err := net.LookupHost(domain)
	if err != nil {
		t.Logf("DNS lookup for %s failed: %v (may not be using dnsmasq)", domain, err)
		return nil
	}
	return addrs
}

// waitForImapMessage polls an IMAP server until a message with the given
// subject is present in INBOX. Reference-server mailboxes live in the
// reference instances' own databases and are invisible to the product API,
// so delivery to them is verified over IMAP — the same way a real user of
// that server would see it.
func waitForImapMessage(t *testing.T, imapAddr, user, pass, subject string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		found := func() bool {
			ic := dialIMAP(t, imapAddr)
			defer ic.close()
			ic.login(t, user, pass)
			if result, _ := ic.command(t, "SELECT INBOX"); !strings.Contains(result, "OK") {
				return false
			}
			result, lines := ic.command(t, fmt.Sprintf(`SEARCH SUBJECT "%s"`, subject))
			if !strings.Contains(result, "OK") {
				return false
			}
			for _, line := range lines {
				if strings.HasPrefix(line, "* SEARCH") && len(strings.Fields(line)) >= 3 {
					return true
				}
			}
			return false
		}()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %q not delivered to %s (%s) within %s", subject, user, imapAddr, timeout)
		}
		time.Sleep(2 * time.Second)
	}
}

// dialIMAPTLS dials an implicit-TLS IMAP endpoint (993). It used to connect in the clear
// on 143 and issue STARTTLS; there is no 143 to connect to any more.
func dialIMAPTLS(t *testing.T, addr string) *imapConn {
	t.Helper()
	conn := dialTLS(t, addr)
	ic := &imapConn{conn: conn, reader: bufio.NewReader(conn)}
	if greeting := ic.readLine(t); !strings.Contains(greeting, "OK") {
		t.Fatalf("IMAP greeting = %q, want OK", greeting)
	}
	return ic
}

// dialPOP3TLS dials an implicit-TLS POP3 endpoint (995). Previously 110 plus STLS.
func dialPOP3TLS(t *testing.T, addr string) *pop3Conn {
	t.Helper()
	conn := dialTLS(t, addr)
	pc := &pop3Conn{conn: conn, reader: bufio.NewReader(conn)}
	if greeting := pc.readLine(t); !strings.HasPrefix(greeting, "+OK") {
		t.Fatalf("POP3 greeting = %q, want +OK", greeting)
	}
	return pc
}

// dialSMTPTLS dials an implicit-TLS SMTP endpoint (465, submission). Previously 587 plus
// STARTTLS, which no longer exists on rest-mail.
func dialSMTPTLS(t *testing.T, addr string) *smtpConn {
	t.Helper()
	conn := dialTLS(t, addr)
	sc := &smtpConn{conn: conn, reader: bufio.NewReader(conn)}
	if greeting := sc.readLine(t); !strings.HasPrefix(greeting, "220") {
		t.Fatalf("SMTP greeting = %q, want 220", greeting)
	}
	return sc
}

// dialTLS opens a TLS connection to a testbed listener. Certificates come from the
// testbed's own CA, so verification is skipped rather than teaching every test about it —
// what is under test is the protocol, not the chain.
func dialTLS(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr,
		&tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("dial TLS %s: %v", addr, err)
	}
	return conn
}
