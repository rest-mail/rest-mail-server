package rfctest

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// A counter independent of session state proves rejected plaintext commands
// never reach authentication. Atomic access also covers failure/cleanup paths.
type stlsBackend struct {
	backend
	calls atomic.Int32
}

func (b *stlsBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	b.calls.Add(1)
	return b.backend.Authenticate(user, pass)
}

func stlsConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	// Trust only this temporary self-signed certificate; normal hostname and
	// chain verification remain enabled. No listener or external endpoint.
	return &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}},
	}, &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		RootCAs: roots, ServerName: "localhost",
	}
}

func stlsConnect(t *testing.T, config *tls.Config) (*wire, *stlsBackend) {
	t.Helper()
	client, server := net.Pipe()
	b := &stlsBackend{backend: backend{password: "secret"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pop3server.NewSession(server, b, config, nil).Handle()
	}()
	t.Cleanup(func() {
		// Close raw endpoints instead of waiting for a TLS close_notify exchange
		// on net.Pipe. This also unblocks either side of a failed handshake.
		_ = client.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("STLS session did not stop")
		}
	})
	w := &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}
	w.status(w.line(), "+OK")
	return w, b
}

func stlsCapabilities(t *testing.T, w *wire, wantSTLS bool) {
	t.Helper()
	w.status(w.cmd("CAPA"), "+OK")
	want := map[string]bool{"USER": true, "TOP": true, "UIDL": true, "RESP-CODES": true, "AUTH-RESP-CODE": true, "PIPELINING": true}
	if wantSTLS {
		want["STLS"] = true
	}
	for i := 0; ; i++ {
		if i > 32 {
			t.Fatal("CAPA missing terminator")
		}
		line := w.line() // Checks CRLF and the RFC 2449 line bound.
		if line == "." {
			break
		}
		if !want[line] {
			t.Fatalf("unexpected, duplicate or parameterized capability %q", line)
		}
		delete(want, line)
	}
	if len(want) != 0 {
		t.Fatalf("CAPA omitted %v", want)
	}
}

func stlsUpgrade(t *testing.T, w *wire, config *tls.Config) {
	t.Helper()
	w.status(w.cmd("STLS"), "+OK")
	stlsHandshake(t, w, config)
}

// stlsHandshake negotiates TLS after the client has read the positive STLS
// reply, and replaces the wire's plaintext endpoints with the encrypted ones.
func stlsHandshake(t *testing.T, w *wire, config *tls.Config) {
	t.Helper()
	if w.reader.Buffered() != 0 {
		t.Fatal("unexpected plaintext bytes after STLS response")
	}
	secured := tls.Client(w.conn, config)
	if err := secured.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := secured.Handshake(); err != nil {
		t.Fatalf("STLS handshake: %v", err)
	}
	state := secured.ConnectionState()
	if !state.HandshakeComplete || state.Version != tls.VersionTLS13 || len(state.VerifiedChains) == 0 {
		t.Fatalf("TLS handshake/verification incomplete: %+v", state)
	}
	w.conn, w.reader = secured, bufio.NewReader(secured)
}

// checklist: POP3-STLS-01
func TestSTLSNoConfig(t *testing.T) {
	w, b := stlsConnect(t, nil)
	stlsCapabilities(t, w, false)
	w.status(w.cmd("STLS"), "-ERR")
	stlsCapabilities(t, w, false) // Refusal preserves the plaintext session.
	w.login("secret")
	w.status(w.cmd("STAT"), "+OK")
	if got := b.calls.Load(); got != 1 {
		t.Fatalf("authentication calls = %d, want 1", got)
	}
}

// checklist: POP3-STLS-01 POP3-STLS-03 POP3-STLS-04 POP3-STLS-05 POP3-TLS-POLICY-01 POP3-STLS-11 POP3-STLS-13 POP3-STLS-16 POP3-ITLS-08
func TestSTLSCredentials(t *testing.T) {
	server, client := stlsConfigs(t)
	w, b := stlsConnect(t, server)
	stlsCapabilities(t, w, true)
	w.status(w.cmd("USER alice"), "-ERR")
	w.status(w.cmd("PASS secret"), "-ERR")
	w.status(w.cmd("STAT"), "-ERR")
	if got := b.calls.Load(); got != 0 {
		t.Fatalf("plaintext authentication calls = %d, want 0", got)
	}
	stlsUpgrade(t, w, client)
	stlsCapabilities(t, w, false)   // Fresh encrypted CAPA, not cached plaintext.
	w.status(w.cmd("STAT"), "-ERR") // Handshake alone does not authenticate.
	w.status(w.cmd("PASS secret"), "-ERR")
	if got := b.calls.Load(); got != 0 {
		t.Fatalf("authentication calls before encrypted USER/PASS = %d", got)
	}
	w.login("secret")
	expect(t, w.cmd("STAT"), "+OK 1 42")
	if got := b.calls.Load(); got != 1 {
		t.Fatalf("encrypted authentication calls = %d, want 1", got)
	}
	stlsCapabilities(t, w, false)
	w.status(w.cmd("NOOP"), "+OK")
}

// checklist: POP3-STLS-02
func TestSTLSArguments(t *testing.T) {
	server, client := stlsConfigs(t)
	w, b := stlsConnect(t, server)
	for _, command := range []string{"STLS unexpected", "STLS one two"} {
		w.status(w.cmd(command), "-ERR")
		stlsCapabilities(t, w, true) // Still plaintext; negotiation did not start.
		w.status(w.cmd("USER alice"), "-ERR")
	}
	if got := b.calls.Load(); got != 0 {
		t.Fatalf("authentication calls = %d, want 0", got)
	}
	stlsUpgrade(t, w, client) // A subsequent argumentless STLS still works.
	stlsCapabilities(t, w, false)
	w.login("secret")
	w.status(w.cmd("NOOP"), "+OK")
}

// checklist: POP3-STLS-06
func TestSTLSStates(t *testing.T) {
	server, client := stlsConfigs(t)
	w, _ := stlsConnect(t, server)
	stlsUpgrade(t, w, client)
	expect(t, w.cmd("STLS"), "-ERR Already using TLS") // Still AUTHORIZATION.
	stlsCapabilities(t, w, false)
	w.status(w.cmd("STAT"), "-ERR")
	w.login("secret")
	expect(t, w.cmd("STLS"), "-ERR Command only valid before authentication")
	w.status(w.cmd("NOOP"), "+OK")

	// Existing #17 regression: distinguish TRANSACTION refusal from the
	// already-TLS and unavailable-TLS guards by the library's reply text.
	plain, _ := stlsConnect(t, nil)
	plain.login("secret")
	expect(t, plain.cmd("STLS"), "-ERR Command only valid before authentication")
	plain.status(plain.cmd("NOOP"), "+OK")
}

// checklist: POP3-TLS-POLICY-02 POP3-STLS-15
func TestSTLSHandshakeFailure(t *testing.T) {
	server, _ := stlsConfigs(t)
	w, b := stlsConnect(t, server)
	w.status(w.cmd("STLS"), "+OK")
	_ = w.conn.SetDeadline(time.Now().Add(5 * time.Second))
	// A complete five-byte non-TLS record header deterministically fails the
	// server handshake; no timing race or partially written client hello.
	if _, err := io.WriteString(w.conn, "PLAIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("after invalid TLS header: %v, want EOF", err)
	}
	waitStopped(t, w)
	if got := b.calls.Load(); got != 0 {
		t.Fatalf("failed-handshake authentication calls = %d, want 0", got)
	}
}

// checklist: POP3-STLS-07
func TestSTLSDiscardsPipelinedPlaintext(t *testing.T) {
	server, client := stlsConfigs(t)
	w, b := stlsConnect(t, server)
	// One plaintext write puts commands behind STLS in the server's read
	// buffer before the handshake: the STARTTLS command-injection shape
	// (CVE-2011-0411). None may run as if the client had sent it under TLS.
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(w.conn, "STLS\r\nUSER alice\r\nPASS secret\r\nNOOP\r\n"); err != nil {
		t.Fatalf("send pipelined STLS: %v", err)
	}
	w.status(w.line(), "+OK")
	stlsHandshake(t, w, client)
	// The first encrypted reply answers the first encrypted command; no
	// injected NOOP reply precedes it and no injected USER/PASS authenticated.
	w.status(w.cmd("STAT"), "-ERR")
	w.status(w.cmd("PASS secret"), "-ERR") // The injected USER was not kept.
	if got := b.calls.Load(); got != 0 {
		t.Fatalf("authentication calls from injected plaintext = %d, want 0", got)
	}
	stlsCapabilities(t, w, false)
	w.login("secret")
	expect(t, w.cmd("STAT"), "+OK 1 42")
	if got := b.calls.Load(); got != 1 {
		t.Fatalf("encrypted authentication calls = %d, want 1", got)
	}
}
