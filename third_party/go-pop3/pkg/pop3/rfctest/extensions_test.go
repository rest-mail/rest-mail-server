package rfctest

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// extensionCapabilities checks the actual wire ABNF, including parameterized
// capabilities, independently of the server's capability construction.
func extensionCapabilities(t *testing.T, w *wire) map[string][]string {
	t.Helper()
	w.status(w.cmd("CAPA"), "+OK")
	caps := map[string][]string{}
	for i := 0; ; i++ {
		if i > 32 {
			t.Fatal("CAPA missing terminator")
		}
		line := w.line() // Includes CRLF and 512-octet checks.
		if line == "." {
			return caps
		}
		fields := strings.Split(line, " ")
		for n, field := range fields {
			if field == "" {
				t.Fatalf("empty tag/parameter in %q", line)
			}
			for _, c := range []byte(field) {
				// RFC 2449 §3: cchar excludes '.', param is VCHAR.
				if c < 0x21 || (n == 0 && (c > 0x7f || c == '.')) || (n > 0 && c > 0x7e) {
					t.Fatalf("invalid capability %q", line)
				}
			}
		}
		tag := strings.ToUpper(fields[0])
		if _, exists := caps[tag]; exists {
			t.Fatalf("duplicate capability %q", line)
		}
		caps[tag] = fields[1:]
	}
}

func extensionConnect(t *testing.T, b pop3.Backend, config *tls.Config) *wire {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); pop3server.NewSession(server, b, config, nil).Handle() }()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("extension session did not stop")
		}
	})
	w := &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}
	w.status(w.line(), "+OK")
	return w
}

// checklist: POP3-CAPA-06
func TestCapaTopContract(t *testing.T) {
	m := &transactionMailbox{raw: transactionMessages, deleted: make(chan string, 10)}
	w := connect(t, m)
	for _, authenticated := range []bool{false, true} {
		if authenticated {
			w.login("secret")
		}
		caps := extensionCapabilities(t, w)
		params, ok := caps["TOP"]
		if !ok || len(params) != 0 {
			t.Fatalf("TOP capability = %v, present=%t", params, ok)
		}
		if authenticated {
			top(t, w, "TOP 1 0", "Subject: a", "")
			top(t, w, "TOP 1 1", "Subject: a", "", "..")
			w.status(w.cmd("NOOP"), "+OK")
		} else {
			w.status(w.cmd("TOP 1 0"), "-ERR")
		}
	}
	quit(t, w, "+OK")
}

// checklist: POP3-CAPA-07
func TestCapaUIDLContract(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	for _, authenticated := range []bool{false, true} {
		if authenticated {
			w.login("secret")
		}
		caps := extensionCapabilities(t, w)
		params, ok := caps["UIDL"]
		if !ok || len(params) != 0 {
			t.Fatalf("UIDL capability = %v, present=%t", params, ok)
		}
		if authenticated {
			expect(t, w.cmd("UIDL 1"), "+OK 1 one")
			w.status(w.cmd("UIDL"), "+OK")
			expect(t, w.line(), "1 one")
			expect(t, w.line(), ".")
			w.status(w.cmd("NOOP"), "+OK")
		} else {
			w.status(w.cmd("UIDL"), "-ERR")
			w.status(w.cmd("UIDL 1"), "-ERR")
		}
	}
	quit(t, w, "+OK")
}

type extensionUTF8Backend struct{ pop3.Backend }

func (b extensionUTF8Backend) AuthenticateUTF8(user, pass string, _ bool) (pop3.Mailbox, error) {
	return b.Authenticate(user, pass)
}

// Preserve LOGIN-DELAY in the configuration that combines both interfaces.
type extensionUTF8DelayBackend struct{ *loginDelayCapaBackend }

func (b extensionUTF8DelayBackend) AuthenticateUTF8(user, pass string, _ bool) (pop3.Mailbox, error) {
	return b.Authenticate(user, pass)
}

// capaImplementation adds IMPLEMENTATION to the configuration wrappers below.
// Each wrapper embeds the interface it extends, so the session still sees the
// LOGIN-DELAY and UTF8 methods of the backend underneath.
type capaImplementation struct{ transactionOnly bool }

func (c capaImplementation) Implementation() (string, bool) {
	return "go-pop3 test v1", c.transactionOnly
}

type extensionUTF8DelayAPI interface {
	pop3.UTF8Backend
	LoginDelay() (int, bool)
}

type (
	extensionImplBackend struct {
		pop3.Backend
		capaImplementation
	}
	extensionImplDelayBackend struct {
		pop3.LoginDelayBackend
		capaImplementation
	}
	extensionImplUTF8Backend struct {
		pop3.UTF8Backend
		capaImplementation
	}
	extensionImplUTF8DelayBackend struct {
		extensionUTF8DelayAPI
		capaImplementation
	}
)

var (
	_ pop3.ImplementationBackend = extensionImplBackend{}
	_ pop3.LoginDelayBackend     = extensionImplDelayBackend{}
	_ pop3.UTF8Backend           = extensionImplUTF8Backend{}
	_ pop3.LoginDelayBackend     = extensionImplUTF8DelayBackend{}
	_ pop3.UTF8Backend           = extensionImplUTF8DelayBackend{}
)

// withImplementation wraps b so it also announces IMPLEMENTATION, keeping
// whichever of LOGIN-DELAY and UTF8 b already offers.
func withImplementation(b pop3.Backend, transactionOnly bool) pop3.Backend {
	impl := capaImplementation{transactionOnly}
	switch b := b.(type) {
	case extensionUTF8DelayAPI:
		return extensionImplUTF8DelayBackend{b, impl}
	case pop3.UTF8Backend:
		return extensionImplUTF8Backend{b, impl}
	case pop3.LoginDelayBackend:
		return extensionImplDelayBackend{b, impl}
	}
	return extensionImplBackend{b, impl}
}

// checklist: POP3-CAPA-11 POP3-CAPA-12
func TestCapaConfiguredGrammar(t *testing.T) {
	for _, secured := range []bool{false, true} {
		for _, delay := range []bool{false, true} {
			for _, utf8 := range []bool{false, true} {
				for _, impl := range []string{"none", "both", "transaction"} {
					name := fmt.Sprintf("tls=%t/delay=%t/utf8=%t/implementation=%s", secured, delay, utf8, impl)
					t.Run(name, func(t *testing.T) {
						var b pop3.Backend = backend{password: "secret"}
						if delay {
							b = &loginDelayCapaBackend{seconds: 60, perUser: true}
						}
						if utf8 {
							if delay {
								b = extensionUTF8DelayBackend{b.(*loginDelayCapaBackend)}
							} else {
								b = extensionUTF8Backend{b}
							}
						}
						if impl != "none" {
							b = withImplementation(b, impl == "transaction")
						}
						var config, client *tls.Config
						if secured {
							config, client = stlsConfigs(t)
						}
						w := extensionConnect(t, b, config)
						want := map[string][]string{"USER": {}, "TOP": {}, "UIDL": {}, "RESP-CODES": {}, "AUTH-RESP-CODE": {}, "PIPELINING": {}}
						if utf8 {
							want["UTF8"] = []string{}
						}
						if delay {
							want["LOGIN-DELAY"] = []string{"60", "USER"}
						}
						if impl == "both" {
							want["IMPLEMENTATION"] = []string{"go-pop3", "test", "v1"}
						}
						if secured {
							want["STLS"] = []string{}
						}
						check := func() {
							t.Helper()
							if got := extensionCapabilities(t, w); !reflect.DeepEqual(got, want) {
								t.Fatalf("CAPA=%v, want %v", got, want)
							}
						}
						check()
						if secured {
							stlsUpgrade(t, w, client)
							delete(want, "STLS")
							check()
						}
						w.login("secret")
						if delay {
							want["LOGIN-DELAY"] = []string{"60"}
						}
						if impl != "none" {
							want["IMPLEMENTATION"] = []string{"go-pop3", "test", "v1"}
						}
						check()
						w.status(w.cmd("NOOP"), "+OK")
						// Raw-pipe cleanup avoids simultaneous TLS close_notify writes.
					})
				}
			}
		}
	}
}

// checklist: POP3-RESP-CODES-08
func TestResponseCodesRemainOptional(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	caps := extensionCapabilities(t, w)
	if params, ok := caps["RESP-CODES"]; !ok || len(params) != 0 {
		t.Fatal("missing parameterless RESP-CODES")
	}
	// CAPA support does not require a bracketed code on every response. Exact
	// replies assert the distinction and CRLF framing, then prove recovery.
	expect(t, w.cmd("STAT"), "-ERR Not authenticated")
	expect(t, w.cmd("PASS secret"), "-ERR USER first")
	expect(t, w.cmd("USER alice"), "+OK")
	expect(t, w.cmd("PASS wrong"), "-ERR [AUTH] Authentication failed")
	expect(t, w.cmd("ZZZ"), "-ERR Unknown command")
	w.login("secret")
	expect(t, w.cmd("LIST 2"), "-ERR No such message")
	expect(t, w.cmd("STAT"), "+OK 1 42")
	expect(t, w.cmd("NOOP"), "+OK")
	quit(t, w, "+OK")
}

// checklist: POP3-STLS-08
func TestSTLSClientCertificateRequiresPOPLogin(t *testing.T) {
	server, client := stlsConfigs(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "POP3 client"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	server.ClientCAs = x509.NewCertPool()
	server.ClientCAs.AddCert(cert)
	server.ClientAuth = tls.RequireAndVerifyClientCert
	var verified atomic.Int32
	server.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) > 0 && len(state.PeerCertificates) == 1 && state.PeerCertificates[0].Equal(cert) {
			verified.Add(1)
		}
		return nil
	}
	client.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}}
	w, b := stlsConnect(t, server)
	stlsCapabilities(t, w, true)
	stlsUpgrade(t, w, client)
	stlsCapabilities(t, w, false)
	if verified.Load() != 1 {
		t.Fatal("server did not verify the supplied client certificate")
	}
	for _, command := range []string{"STAT", "LIST", "UIDL", "RETR 1", "TOP 1 0", "DELE 1", "NOOP", "RSET", "PASS secret"} {
		w.status(w.cmd(command), "-ERR")
	}
	if b.calls.Load() != 0 {
		t.Fatalf("certificate authenticated POP3: %d backend calls", b.calls.Load())
	}
	w.login("secret")
	expect(t, w.cmd("STAT"), "+OK 1 42")
	expect(t, w.cmd("NOOP"), "+OK")
	if b.calls.Load() != 1 {
		t.Fatalf("backend calls=%d, want 1", b.calls.Load())
	}
}
