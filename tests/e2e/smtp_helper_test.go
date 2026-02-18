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
	sc.conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := fmt.Fprintf(sc.conn, "%s\r\n", cmd)
	if err != nil {
		t.Fatalf("send SMTP command %q: %v", cmd, err)
	}
}

func (sc *smtpConn) readLine(t *testing.T) string {
	t.Helper()
	sc.conn.SetDeadline(time.Now().Add(10 * time.Second))
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
	sc := dialSMTP(t, smtpAddr)
	defer sc.close()

	sc.ehlo(t, "test.local")
	sc.sendExpect(t, "MAIL FROM:<"+from+">", "250")
	sc.sendExpect(t, "RCPT TO:<"+to+">", "250")
	sc.sendExpect(t, "DATA", "354")

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <test-%d@test.local>\r\n\r\n%s",
		from, to, subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano(), body)

	sc.send(t, msg)
	sc.sendExpect(t, ".", "250")
	sc.sendExpect(t, "QUIT", "221")
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

// authPlain sends AUTH PLAIN with base64-encoded credentials.
func (sc *smtpConn) authPlain(t *testing.T, user, pass string) {
	t.Helper()
	cred := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
	sc.sendExpect(t, "AUTH PLAIN "+cred, "235")
}

// sendMailViaSubmission sends an email via the submission port (587) with STARTTLS + AUTH PLAIN.
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
	ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
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
	ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
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

// starttls upgrades the IMAP connection to TLS.
func (ic *imapConn) starttls(t *testing.T) {
	t.Helper()
	result, _ := ic.command(t, "STARTTLS")
	if !strings.Contains(result, "OK") {
		t.Fatalf("IMAP STARTTLS failed: %s", result)
	}
	tlsConn := tls.Client(ic.conn, &tls.Config{InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("IMAP TLS handshake failed: %v", err)
	}
	ic.conn = tlsConn
	ic.reader = bufio.NewReader(tlsConn)
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
	pc.conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := pc.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read POP3 line: %v", err)
	}
	return strings.TrimSpace(line)
}

func (pc *pop3Conn) sendExpect(t *testing.T, cmd string, expectedPrefix string) string {
	t.Helper()
	pc.conn.SetDeadline(time.Now().Add(10 * time.Second))
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

// stls upgrades the POP3 connection to TLS.
func (pc *pop3Conn) stls(t *testing.T) {
	t.Helper()
	pc.sendExpect(t, "STLS", "+OK")
	tlsConn := tls.Client(pc.conn, &tls.Config{InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("POP3 TLS handshake failed: %v", err)
	}
	pc.conn = tlsConn
	pc.reader = bufio.NewReader(tlsConn)
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
	// In the Docker environment, containers use dnsmasq (172.20.0.3).
	addrs, err := net.LookupHost(domain)
	if err != nil {
		t.Logf("DNS lookup for %s failed: %v (may not be using dnsmasq)", domain, err)
		return nil
	}
	return addrs
}

// ── TLS helper ───────────────────────────────────────────────────────

var insecureTLSConfig = &tls.Config{InsecureSkipVerify: true}
