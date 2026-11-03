package server

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// selfSignedTLSConfig builds a *tls.Config carrying a throwaway self-signed
// certificate, so a test can drive a real STARTTLS handshake over net.Pipe
// (unlike newIMAPHarnessTLS, whose empty config never completes a handshake).
func selfSignedTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "imap.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"imap.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
}

// capHarness is a bare transcript driver that can swap its underlying conn to a
// TLS conn mid-stream, which the literal-aware imapHarness cannot. It reads
// CRLF-terminated lines and never buffers past a line boundary — safe because
// the server blocks after each write on the synchronous net.Pipe.
type capHarness struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	done chan struct{}
}

func newCapHarness(t *testing.T, tlsConfig *tls.Config, usingTLS bool) *capHarness {
	t.Helper()
	client, server := net.Pipe()
	sess := NewSession(server, newMockBackend(), "imap.test", tlsConfig, imap.NopLimiter{})
	sess.usingTLS = usingTLS

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()

	h := &capHarness{t: t, conn: client, r: bufio.NewReader(client), done: done}
	t.Cleanup(func() {
		_ = client.Close()
		<-h.done
	})
	return h
}

func (h *capHarness) readLine() string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := h.r.ReadString('\n')
	if err != nil {
		h.t.Fatalf("readLine: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

func (h *capHarness) send(line string) {
	h.t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := h.conn.Write([]byte(line + "\r\n")); err != nil {
		h.t.Fatalf("send %q: %v", line, err)
	}
}

// startTLS upgrades the client side of the pipe to TLS, mirroring the server's
// post-"OK" handshake. It must be called immediately after reading the STARTTLS
// tagged OK, while the server goroutine is blocked in its own Handshake.
func (h *capHarness) startTLS() {
	h.t.Helper()
	_ = h.conn.SetDeadline(time.Now().Add(5 * time.Second))
	tlsConn := tls.Client(h.conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test-only self-signed peer
	if err := tlsConn.Handshake(); err != nil {
		h.t.Fatalf("client TLS handshake: %v", err)
	}
	h.conn = tlsConn
	h.r = bufio.NewReader(tlsConn)
}

// capabilityCode returns the bracketed CAPABILITY response-code payload from a
// greeting like "* OK [CAPABILITY IMAP4rev1 STARTTLS ...] host ready".
func capabilityCode(t *testing.T, greeting string) string {
	t.Helper()
	open := strings.Index(greeting, "[CAPABILITY ")
	if open < 0 {
		t.Fatalf("greeting has no [CAPABILITY ...] code: %q", greeting)
	}
	rest := greeting[open+len("[CAPABILITY "):]
	end := strings.Index(rest, "]")
	if end < 0 {
		t.Fatalf("greeting [CAPABILITY has no closing ]: %q", greeting)
	}
	return rest[:end]
}

// capTokens sends CAPABILITY and returns the tokens from the untagged
// "* CAPABILITY ..." line, failing if the command did not complete OK.
func (h *capHarness) capTokens(tag string) string {
	h.t.Helper()
	h.send(tag + " CAPABILITY")
	var untagged string
	for {
		line := h.readLine()
		if strings.HasPrefix(line, "* CAPABILITY ") {
			untagged = strings.TrimPrefix(line, "* CAPABILITY ")
			continue
		}
		if strings.HasPrefix(line, tag+" ") {
			if !strings.Contains(line, " OK") {
				h.t.Fatalf("CAPABILITY status = %q, want OK", line)
			}
			return untagged
		}
	}
}

func hasToken(list, tok string) bool {
	for _, f := range strings.Fields(list) {
		if f == tok {
			return true
		}
	}
	return false
}

func mustHaveToken(t *testing.T, list, tok, where string) {
	t.Helper()
	if !hasToken(list, tok) {
		t.Fatalf("%s: %q missing token %q", where, list, tok)
	}
}

func mustLackToken(t *testing.T, list, tok, where string) {
	t.Helper()
	if hasToken(list, tok) {
		t.Fatalf("%s: %q must not advertise token %q", where, list, tok)
	}
}

// TestCapability_LoginDisabledOnCleartextTLSServer is the core red-green case
// (issue #23). On a cleartext link to a TLS-requiring server:
//   - the greeting's [CAPABILITY ...] code and the untagged CAPABILITY response
//     must both advertise STARTTLS and LOGINDISABLED and must NOT advertise
//     AUTH=PLAIN (which the server would refuse);
//   - LOGIN must be refused (tagged NO) until TLS is established;
//   - after STARTTLS, LOGINDISABLED and STARTTLS disappear, AUTH=PLAIN appears,
//     and LOGIN is accepted.
//
// Old code fails RED here: the greeting is a hardcoded
// "IMAP4rev1 STARTTLS AUTH=PLAIN" (no LOGINDISABLED, wrongly offers AUTH=PLAIN),
// and capabilities() never emits LOGINDISABLED.
func TestCapability_LoginDisabledOnCleartextTLSServer(t *testing.T) {
	h := newCapHarness(t, selfSignedTLSConfig(t), false)

	greetCode := capabilityCode(t, h.readLine())
	mustHaveToken(t, greetCode, "IMAP4rev1", "greeting CAPABILITY")
	mustHaveToken(t, greetCode, "STARTTLS", "greeting CAPABILITY")
	mustHaveToken(t, greetCode, "LOGINDISABLED", "greeting CAPABILITY")
	mustLackToken(t, greetCode, "AUTH=PLAIN", "greeting CAPABILITY")

	pre := h.capTokens("a1")
	mustHaveToken(t, pre, "IMAP4rev1", "pre-TLS CAPABILITY")
	mustHaveToken(t, pre, "STARTTLS", "pre-TLS CAPABILITY")
	mustHaveToken(t, pre, "LOGINDISABLED", "pre-TLS CAPABILITY")
	mustLackToken(t, pre, "AUTH=PLAIN", "pre-TLS CAPABILITY")

	// LOGIN must be refused while LOGINDISABLED is advertised.
	h.send("a2 LOGIN alice@example.com s3cret")
	if resp := h.readLine(); !strings.HasPrefix(resp, "a2 NO") || !strings.Contains(resp, "[PRIVACYREQUIRED]") {
		t.Fatalf("cleartext LOGIN = %q, want \"a2 NO [PRIVACYREQUIRED] ...\"", resp)
	}

	// Upgrade.
	h.send("a3 STARTTLS")
	if resp := h.readLine(); !strings.HasPrefix(resp, "a3 OK") {
		t.Fatalf("STARTTLS = %q, want a3 OK", resp)
	}
	h.startTLS()

	post := h.capTokens("a4")
	mustHaveToken(t, post, "IMAP4rev1", "post-TLS CAPABILITY")
	mustHaveToken(t, post, "AUTH=PLAIN", "post-TLS CAPABILITY")
	mustLackToken(t, post, "STARTTLS", "post-TLS CAPABILITY")
	mustLackToken(t, post, "LOGINDISABLED", "post-TLS CAPABILITY")

	// LOGIN now succeeds over the encrypted link.
	h.send("a5 LOGIN alice@example.com s3cret")
	if resp := h.readLine(); !strings.HasPrefix(resp, "a5 OK") {
		t.Fatalf("LOGIN after STARTTLS = %q, want a5 OK", resp)
	}
}

// TestCapability_ImplicitTLSAdvertisesAuthNotLoginDisabled covers an already
// protected link (implicit TLS / port 993): no STARTTLS, no LOGINDISABLED,
// AUTH=PLAIN offered, and LOGIN accepted.
func TestCapability_ImplicitTLSAdvertisesAuthNotLoginDisabled(t *testing.T) {
	h := newCapHarness(t, selfSignedTLSConfig(t), true)

	greetCode := capabilityCode(t, h.readLine())
	mustHaveToken(t, greetCode, "AUTH=PLAIN", "greeting CAPABILITY")
	mustLackToken(t, greetCode, "STARTTLS", "greeting CAPABILITY")
	mustLackToken(t, greetCode, "LOGINDISABLED", "greeting CAPABILITY")

	caps := h.capTokens("a1")
	mustHaveToken(t, caps, "AUTH=PLAIN", "CAPABILITY")
	mustLackToken(t, caps, "STARTTLS", "CAPABILITY")
	mustLackToken(t, caps, "LOGINDISABLED", "CAPABILITY")

	h.send("a2 LOGIN alice@example.com s3cret")
	if resp := h.readLine(); !strings.HasPrefix(resp, "a2 OK") {
		t.Fatalf("LOGIN over implicit TLS = %q, want a2 OK", resp)
	}
}

// TestCapability_NoTLSConfigOffersPlaintextLogin covers development mode (no TLS
// configured at all): STARTTLS would be refused and plaintext LOGIN is allowed,
// so the greeting must advertise neither STARTTLS nor LOGINDISABLED. Old code
// fails RED here too: its hardcoded greeting wrongly advertises STARTTLS.
func TestCapability_NoTLSConfigOffersPlaintextLogin(t *testing.T) {
	h := newCapHarness(t, nil, false)

	greetCode := capabilityCode(t, h.readLine())
	mustHaveToken(t, greetCode, "AUTH=PLAIN", "greeting CAPABILITY")
	mustLackToken(t, greetCode, "STARTTLS", "greeting CAPABILITY")
	mustLackToken(t, greetCode, "LOGINDISABLED", "greeting CAPABILITY")

	caps := h.capTokens("a1")
	mustLackToken(t, caps, "STARTTLS", "CAPABILITY")
	mustLackToken(t, caps, "LOGINDISABLED", "CAPABILITY")

	h.send("a2 LOGIN alice@example.com s3cret")
	if resp := h.readLine(); !strings.HasPrefix(resp, "a2 OK") {
		t.Fatalf("plaintext LOGIN (no TLS config) = %q, want a2 OK", resp)
	}
}
