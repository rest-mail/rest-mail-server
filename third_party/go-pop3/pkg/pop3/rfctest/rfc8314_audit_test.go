package rfctest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"
)

// freePorts reserves n distinct free ports and releases them. ListenAndServe
// binds every interface, so a port it opened stays unavailable afterwards.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var ports []int
	var held []net.Listener
	for range n {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range held {
		_ = l.Close()
	}
	return ports
}

// expectUnbound proves ListenAndServe left the port closed.
func expectUnbound(t *testing.T, port int) {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("port %d was opened: %v", port, err)
	}
	_ = l.Close()
}

// RFC 8314 §3.1: on the "pop3s" port a TLS handshake begins immediately. With
// no TLS config the Server used to open that port in plaintext; it now refuses
// to start, and opens neither listener when a plaintext port is also asked for.
// checklist: POP3-ITLS-05
func TestRFC8314ImplicitTLSRequiresConfig(t *testing.T) {
	ports := freePorts(t, 2)
	for name, p := range map[string]pop3server.Ports{
		"pop3s-only": {POP3TLS: ports[1]},
		"both":       {POP3: ports[0], POP3TLS: ports[1]},
	} {
		t.Run(name, func(t *testing.T) {
			srv := pop3server.New(&backend{password: "secret"}, nil, nil)
			t.Cleanup(func() { _ = srv.Close() })
			err := srv.ListenAndServe(p)
			if err == nil || !strings.Contains(err.Error(), "implicit TLS needs a TLS config") {
				t.Fatalf("ListenAndServe(%+v) with nil TLS config = %v, want refusal", p, err)
			}
			expectUnbound(t, ports[0])
			expectUnbound(t, ports[1])
		})
	}
}

// RFC 8314 §4: servers SHOULD NOT advertise STLS when using it is likely to
// fail, for example with no server certificate. A TLS config with no
// certificate source would advertise STLS and fail every handshake, so the
// Server refuses it for either port. Each of the three certificate sources
// is accepted.
// checklist: POP3-ITLS-06
func TestRFC8314TLSConfigNeedsCertificate(t *testing.T) {
	for name, p := range map[string]pop3server.Ports{
		"pop3":  {POP3: freePorts(t, 1)[0]},
		"pop3s": {POP3TLS: freePorts(t, 1)[0]},
	} {
		t.Run(name, func(t *testing.T) {
			srv := pop3server.New(&backend{password: "secret"}, &tls.Config{MinVersion: tls.VersionTLS12}, nil)
			t.Cleanup(func() { _ = srv.Close() })
			err := srv.ListenAndServe(p)
			if err == nil || !strings.Contains(err.Error(), "TLS config has no certificate") {
				t.Fatalf("ListenAndServe(%+v) without a certificate = %v, want refusal", p, err)
			}
			expectUnbound(t, p.POP3+p.POP3TLS)
		})
	}
	server, _ := stlsConfigs(t)
	for name, config := range map[string]*tls.Config{
		"certificates":          {MinVersion: tls.VersionTLS12, Certificates: server.Certificates},
		"get-certificate":       {MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &server.Certificates[0], nil }},
		"get-config-for-client": {MinVersion: tls.VersionTLS12, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return server, nil }},
	} {
		t.Run(name, func(t *testing.T) {
			srv := pop3server.New(&backend{password: "secret"}, config, nil)
			t.Cleanup(func() { _ = srv.Close() })
			if err := srv.ListenAndServe(pop3server.Ports{POP3TLS: freePorts(t, 1)[0]}); err != nil && !strings.HasPrefix(err.Error(), "failed to listen") {
				t.Fatalf("ListenAndServe with %s = %v, want accepted", name, err)
			}
		})
	}
}

// RFC 8314 §3.1: after the greeting the session is in AUTHORIZATION even if a
// client certificate was supplied during the implicit-TLS handshake; §4.2: a
// server that does not accept certificates as sufficient does not offer SASL
// EXTERNAL. The host's tls.Config here requires any client certificate.
// checklist: POP3-ITLS-07
func TestRFC8314ClientCertificateImplicitTLS(t *testing.T) {
	server, client := stlsConfigs(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "POP3 client"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	server.ClientAuth = tls.RequireAnyClientCert
	var presented atomic.Int32
	server.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 1 {
			presented.Add(1)
		}
		return nil
	}
	client.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}}
	addr, b := implicitTLSServer(t, server)
	w := implicitTLSDial(t, addr, client)
	if presented.Load() != 1 {
		t.Fatal("server did not receive the client certificate")
	}
	for _, command := range []string{"STAT", "LIST", "RETR 1", "PASS secret"} {
		w.status(w.cmd(command), "-ERR")
	}
	if got := saslCapa(t, w); len(got) != 0 {
		t.Fatalf("SASL offered on a certificate connection: %q", got)
	}
	expect(t, w.cmd("AUTH EXTERNAL ="), "-ERR Unknown command")
	if b.calls.Load() != 0 {
		t.Fatalf("certificate authenticated POP3: %d backend calls", b.calls.Load())
	}
	w.login("secret")
	expect(t, w.cmd("STAT"), "+OK 1 42")
	w.status(w.cmd("QUIT"), "+OK")
}

// logBuffer collects slog output from session goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) line(t *testing.T, msg string) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(line, `msg="`+msg+`"`) {
			return line
		}
	}
	t.Fatalf("no %q log in:\n%s", msg, b.buf.String())
	return ""
}

// captureLogs swaps the default logger; it is restored after every later
// cleanup, so sessions have stopped logging by then.
func captureLogs(t *testing.T) *logBuffer {
	t.Helper()
	logs := &logBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

// RFC 8314 §4: servers implementing TLS SHOULD log TLS cipher information
// with their connection and authentication logs. The STLS log and the
// successful and failed login logs name the negotiated version and cipher
// suite, on STLS and implicit TLS alike; plaintext logins name none.
// checklist: POP3-ITLS-11
func TestRFC8314LogsTLSCipher(t *testing.T) {
	cipherAttrs := func(t *testing.T, conn net.Conn) string {
		t.Helper()
		state := conn.(*tls.Conn).ConnectionState()
		return `tls_version="` + tls.VersionName(state.Version) + `" tls_cipher=` + tls.CipherSuiteName(state.CipherSuite)
	}
	t.Run("stls", func(t *testing.T) {
		logs := captureLogs(t)
		server, client := stlsConfigs(t)
		w, _ := stlsConnect(t, server)
		stlsUpgrade(t, w, client)
		want := cipherAttrs(t, w.conn)
		w.status(w.cmd("USER alice"), "+OK")
		w.status(w.cmd("PASS wrong"), "-ERR")
		w.login("secret")
		for _, msg := range []string{"pop3: TLS established", "pop3: auth failed", "pop3: authenticated"} {
			if line := logs.line(t, msg); !strings.Contains(line, want) {
				t.Errorf("%s log lacks %s:\n%s", msg, want, line)
			}
		}
	})
	t.Run("implicit", func(t *testing.T) {
		logs := captureLogs(t)
		server, client := stlsConfigs(t)
		addr, _ := implicitTLSServer(t, server)
		w := implicitTLSDial(t, addr, client)
		want := cipherAttrs(t, w.conn)
		w.status(w.cmd("USER alice"), "+OK")
		w.status(w.cmd("PASS wrong"), "-ERR")
		w.login("secret")
		for _, msg := range []string{"pop3: auth failed", "pop3: authenticated"} {
			if line := logs.line(t, msg); !strings.Contains(line, want) {
				t.Errorf("%s log lacks %s:\n%s", msg, want, line)
			}
		}
	})
	t.Run("plaintext", func(t *testing.T) {
		logs := captureLogs(t)
		w := connect(t, &backend{password: "secret"})
		w.login("secret")
		if line := logs.line(t, "pop3: authenticated"); strings.Contains(line, "tls_") {
			t.Errorf("plaintext login log names TLS:\n%s", line)
		}
	})
}
