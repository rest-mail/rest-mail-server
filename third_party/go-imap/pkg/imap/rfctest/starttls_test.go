package rfctest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"
)

// serverTLS returns a config with a throwaway self-signed certificate.
func serverTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "imap.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"imap.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// tlsClient dials a STARTTLS-capable session and consumes the greeting.
func tlsClient(t *testing.T) *client {
	t.Helper()
	c := dialTLS(t, nil, nil, serverTLS(t))
	if g := c.line(); !strings.HasPrefix(g, "* OK ") {
		t.Fatalf("greeting %q", g)
	}
	return c
}

// startTLS issues STARTTLS and negotiates TLS. The negotiation must begin
// right after the tagged OK's CRLF, so no further octets may be buffered.
func (c *client) startTLS(tag string) {
	c.t.Helper()
	if _, tagged := c.command(tag, "STARTTLS"); !strings.HasPrefix(tagged, tag+" OK") {
		c.t.Fatalf("STARTTLS: %q", tagged)
	}
	if n := c.r.Buffered(); n != 0 {
		c.t.Fatalf("%d octets followed the STARTTLS OK before negotiation", n)
	}
	conn := tls.Client(c.conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only self-signed peer
	if err := conn.Handshake(); err != nil {
		c.t.Fatalf("TLS handshake: %v", err)
	}
	c.conn = conn
	c.r = bufio.NewReader(conn)
}

// capabilities returns the tokens of the single untagged CAPABILITY response.
func (c *client) capabilities(tag string) []string {
	c.t.Helper()
	lines, tagged := c.command(tag, "CAPABILITY")
	requireOK(c.t, tagged, tag)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "* CAPABILITY ") {
		c.t.Fatalf("CAPABILITY responses %q", lines)
	}
	return strings.Fields(strings.TrimPrefix(lines[0], "* CAPABILITY "))
}

func requireCaps(t *testing.T, caps []string, have, lack []string) {
	t.Helper()
	for _, want := range have {
		if !slices.Contains(caps, want) {
			t.Fatalf("capabilities %q lack %s", caps, want)
		}
	}
	for _, bad := range lack {
		if slices.Contains(caps, bad) {
			t.Fatalf("capabilities %q advertise %s", caps, bad)
		}
	}
}

// TestStartTLSExample follows the RFC 3501 §6.2.1 example transcript.
// checklist: IMAP-STARTTLS-01 IMAP-STARTTLS-02 IMAP-STARTTLS-03 IMAP-CAP-02 IMAP-LOGIN-02
// checklist: IMAP2-CMD-45 IMAP2-CMD-49 IMAP2-CMD-50 IMAP2-CMD-58 IMAP2-CMD-62
// checklist: IMAP2-RESP-181 IMAP2-RESP-182
// checklist: IMAP2-SEC-236 IMAP2-SEC-244
func TestStartTLSExample(t *testing.T) {
	c := tlsClient(t)
	requireCaps(t, c.capabilities("a001"), []string{"IMAP4rev1", "STARTTLS", "LOGINDISABLED"}, []string{"AUTH=PLAIN"})
	if _, tagged := c.command("a0", "LOGIN %s %s", testUser, testPass); !strings.HasPrefix(tagged, "a0 NO") {
		t.Fatalf("cleartext LOGIN with valid credentials: %q, want NO", tagged)
	}
	if _, tagged := c.command("a0b", "AUTHENTICATE PLAIN"); !strings.HasPrefix(tagged, "a0b NO") {
		t.Fatalf("cleartext AUTHENTICATE: %q, want NO before any continuation", tagged)
	}
	if c.backend.calls != 0 {
		t.Fatalf("backend saw %d cleartext credential checks", c.backend.calls)
	}
	c.startTLS("a002")
	requireCaps(t, c.capabilities("a003"), []string{"IMAP4rev1", "AUTH=PLAIN"}, []string{"STARTTLS", "LOGINDISABLED"})
	if _, tagged := c.command("a004", "LOGIN %s %s", testUser, testPass); !strings.HasPrefix(tagged, "a004 OK") {
		t.Fatalf("LOGIN under TLS: %q", tagged)
	}
}

// TestStartTLSNonAuthenticated proves TLS alone authenticates nobody.
// checklist: IMAP-STARTTLS-04
func TestStartTLSNonAuthenticated(t *testing.T) {
	c := tlsClient(t)
	c.startTLS("a1")
	if _, tagged := c.command("a2", "SELECT INBOX"); strings.HasPrefix(tagged, "a2 OK") {
		t.Fatalf("SELECT after STARTTLS without LOGIN: %q", tagged)
	}
	if c.backend.calls != 0 {
		t.Fatalf("backend saw %d credential checks", c.backend.calls)
	}
	c.login()
	_, tagged := c.command("a3", "SELECT INBOX")
	requireOK(t, tagged, "a3")
}

// TestStartTLSCleartextCredentialsRefused proves a TLS-capable session never
// permits LOGIN or AUTHENTICATE before TLS is active: every credential form
// gets NO [PRIVACYREQUIRED] without a continuation request, no credential
// reaches the backend, and the session stays not authenticated. A session
// built without a TLS config is a development deployment and is out of scope.
//
// checklist: IMAP-STARTTLS-08 IMAP-AUTHENTICATE-09 IMAP-LOGIN-02
// checklist: IMAP2-CMD-57 IMAP2-CMD-62
// checklist: IMAP2-RESP-182
// checklist: IMAP2-SEC-244
func TestStartTLSCleartextCredentialsRefused(t *testing.T) {
	plain := base64.StdEncoding.EncodeToString([]byte("\x00" + testUser + "\x00" + testPass))
	c := tlsClient(t)
	for i, cmd := range []string{
		fmt.Sprintf("LOGIN %s %s", testUser, testPass),
		fmt.Sprintf("login %q %q", testUser, testPass),
		fmt.Sprintf("LOGIN {%d+}\r\n%s {%d+}\r\n%s", len(testUser), testUser, len(testPass), testPass),
		fmt.Sprintf("LOGIN %s wrong", testUser),
		"LOGIN",
		"AUTHENTICATE PLAIN",
		"authenticate plain",
		"AUTHENTICATE PLAIN " + plain,
		"AUTHENTICATE PLAIN =",
		"AUTHENTICATE EXTERNAL",
	} {
		tag := fmt.Sprintf("c%d", i)
		lines, tagged := c.command(tag, "%s", cmd)
		if tagged != tag+" NO [PRIVACYREQUIRED] STARTTLS required" {
			t.Fatalf("cleartext %q: %q, want NO [PRIVACYREQUIRED]", cmd, tagged)
		}
		requireExactly(t, lines)
		if _, tagged := c.command(tag+"s", "SELECT INBOX"); strings.HasPrefix(tagged, tag+"s OK") {
			t.Fatalf("SELECT after cleartext %q: %q", cmd, tagged)
		}
	}
	if c.backend.calls != 0 {
		t.Fatalf("backend saw %d cleartext credential checks", c.backend.calls)
	}
	requireCaps(t, c.capabilities("cc"), []string{"STARTTLS", "LOGINDISABLED"}, []string{"AUTH=PLAIN"})
	c.startTLS("c10")
	c.login()
	if c.backend.calls != 1 {
		t.Fatalf("backend saw %d credential checks, want only the protected LOGIN", c.backend.calls)
	}
}

// TestStartTLSRefusals covers the BAD results: no TLS configured, arguments
// supplied, and TLS already active. Each leaves the session usable.
// checklist: IMAP-STARTTLS-05
// checklist: IMAP2-CMD-45 IMAP2-CMD-48
// checklist: IMAP2-RESP-181
func TestStartTLSRefusals(t *testing.T) {
	requireBAD := func(t *testing.T, c *client, tag, format string) {
		t.Helper()
		if _, tagged := c.command(tag, format); !strings.HasPrefix(tagged, tag+" BAD") {
			t.Fatalf("%s: %q, want BAD", format, tagged)
		}
		_, tagged := c.command(tag+"n", "NOOP")
		requireOK(t, tagged, tag+"n")
	}
	t.Run("not configured", func(t *testing.T) {
		c := dial(t)
		c.line()
		requireCaps(t, c.capabilities("a1"), nil, []string{"STARTTLS", "LOGINDISABLED"})
		requireBAD(t, c, "a2", "STARTTLS")
		c.login()
		requireBAD(t, c, "a3", "STARTTLS")
	})
	t.Run("arguments", func(t *testing.T) {
		c := tlsClient(t)
		requireBAD(t, c, "a1", "STARTTLS now")
		requireCaps(t, c.capabilities("a2"), []string{"STARTTLS", "LOGINDISABLED"}, nil)
		c.startTLS("a3")
	})
	t.Run("already active", func(t *testing.T) {
		c := tlsClient(t)
		c.startTLS("a1")
		requireBAD(t, c, "a2", "STARTTLS")
		c.login()
		requireBAD(t, c, "a3", "STARTTLS")
	})
}

// TestStartTLSDiscardsPipelinedPlaintext pipelines a LOGIN behind STARTTLS in
// one cleartext write. It must never run inside the TLS layer.
// checklist: IMAP-STARTTLS-06
// checklist: IMAP2-CMD-49
func TestStartTLSDiscardsPipelinedPlaintext(t *testing.T) {
	c := tlsClient(t)
	if _, err := io.WriteString(c.conn, "a1 STARTTLS\r\na2 LOGIN "+testUser+" "+testPass+"\r\n"); err != nil {
		t.Fatal(err)
	}
	if l := c.line(); !strings.HasPrefix(l, "a1 OK") {
		t.Fatalf("STARTTLS: %q", l)
	}
	conn := tls.Client(c.conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only self-signed peer
	if err := conn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	c.conn, c.r = conn, bufio.NewReader(conn)
	lines, tagged := c.command("b1", "NOOP")
	requireOK(t, tagged, "b1")
	for _, l := range lines {
		if strings.HasPrefix(l, "a2 ") {
			t.Fatalf("pipelined cleartext command answered under TLS: %q", l)
		}
	}
	if c.backend.calls != 0 {
		t.Fatalf("pipelined cleartext LOGIN reached the backend %d times", c.backend.calls)
	}
	if _, tagged := c.command("b2", "SELECT INBOX"); strings.HasPrefix(tagged, "b2 OK") {
		t.Fatalf("pipelined LOGIN authenticated the TLS session: %q", tagged)
	}
}

// TestStartTLSFailedNegotiation sends cleartext where the TLS handshake should
// start. The server must close rather than fall back to cleartext commands.
// checklist: IMAP-STARTTLS-07
// checklist: IMAP2-CMD-49
// checklist: IMAP2-SEC-236
func TestStartTLSFailedNegotiation(t *testing.T) {
	c := tlsClient(t)
	if _, tagged := c.command("a1", "STARTTLS"); !strings.HasPrefix(tagged, "a1 OK") {
		t.Fatalf("STARTTLS: %q", tagged)
	}
	go func() { _, _ = io.WriteString(c.conn, "a2 LOGIN "+testUser+" "+testPass+"\r\na3 NOOP\r\n") }()
	rest, err := io.ReadAll(c.r)
	if err != nil {
		t.Fatalf("read after failed negotiation: %v", err)
	}
	if strings.Contains(string(rest), "a2 ") || strings.Contains(string(rest), "a3 ") {
		t.Fatalf("cleartext commands answered after failed negotiation: %q", rest)
	}
	if c.backend.calls != 0 {
		t.Fatalf("cleartext LOGIN reached the backend %d times", c.backend.calls)
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("session still running after failed negotiation")
	}
}
