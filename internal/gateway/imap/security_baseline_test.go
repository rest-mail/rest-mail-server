package imap

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil/reloadtest"
)

// The RFC 9051 §11 baseline lives in the gateway rather than the IMAP engine:
// the engine serves whatever tls.Config and Limiter it is handed. These tests
// prove what REST Mail hands it (#1672).

// TestServerTLSConfig_RFC9051Baseline proves the configuration the gateway
// serves on 993 sets the §11.1 floor of TLS 1.2, allows the mandated
// TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 suite, and allows only ECDHE AEAD
// suites for TLS 1.2, as RFC 9325 §4.2 recommends.
func TestServerTLSConfig_RFC9051Baseline(t *testing.T) {
	cfg := ServerTLSConfig(nil)
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %s, want TLS 1.2", tls.VersionName(cfg.MinVersion))
	}
	if cfg.MaxVersion != 0 {
		t.Fatalf("MaxVersion = %s, want unset so TLS 1.3 is offered", tls.VersionName(cfg.MaxVersion))
	}
	if !slices.Contains(cfg.CipherSuites, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256) {
		t.Fatal("TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 is not allowed; RFC 9051 §11.1 requires it")
	}
	secure := map[uint16]bool{}
	for _, s := range tls.CipherSuites() {
		secure[s.ID] = true
	}
	for _, id := range cfg.CipherSuites {
		name := tls.CipherSuiteName(id)
		if !secure[id] {
			t.Errorf("%s is one of Go's insecure suites", name)
		}
		if !strings.HasPrefix(name, "TLS_ECDHE_") || !(strings.Contains(name, "_GCM_") || strings.Contains(name, "CHACHA20_POLY1305")) {
			t.Errorf("%s is not an ECDHE AEAD suite", name)
		}
	}
}

// TestServerTLSConfig_Handshakes drives the gateway's TLS configuration
// through the real implicit-TLS listener: a client that speaks at most
// TLS 1.1 is refused, and a TLS 1.2 client offering only the mandated suite
// negotiates it and receives the IMAP greeting. The floor and the AEAD-only
// suite list each refuse TLS 1.1 on their own (no AEAD suite exists below
// TLS 1.2), so the refusal holds if either is loosened alone.
func TestServerTLSConfig_Handshakes(t *testing.T) {
	const host = "imap.test"
	cert := rsaCertificate(t, host)
	cfg := ServerTLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil })
	port := serveIMAPTLS(t, host, cfg, goimap.NopLimiter{}, http.NotFoundHandler())

	t.Run("TLS 1.1 client is refused", func(t *testing.T) {
		conn := dialPort(t, port)
		client := tls.Client(conn, &tls.Config{ //nolint:gosec // proving the server refuses an old client
			ServerName:         host,
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS10,
			MaxVersion:         tls.VersionTLS11,
		})
		if err := client.Handshake(); err == nil {
			t.Fatalf("a TLS 1.1 client negotiated %s; the gateway must refuse below TLS 1.2",
				tls.VersionName(client.ConnectionState().Version))
		}
	})

	t.Run("TLS 1.2 with the mandated suite connects", func(t *testing.T) {
		conn := dialPort(t, port)
		client := tls.Client(conn, &tls.Config{ //nolint:gosec // throwaway self-signed certificate
			ServerName:         host,
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
			MaxVersion:         tls.VersionTLS12,
			CipherSuites:       []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		})
		if err := client.Handshake(); err != nil {
			t.Fatalf("TLS 1.2 handshake with TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256: %v", err)
		}
		state := client.ConnectionState()
		if state.Version != tls.VersionTLS12 || state.CipherSuite != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
			t.Fatalf("negotiated %s with %s", tls.VersionName(state.Version), tls.CipherSuiteName(state.CipherSuite))
		}
		greeting, err := bufio.NewReader(client).ReadString('\n')
		if err != nil || !strings.HasPrefix(greeting, "* OK") {
			t.Fatalf("greeting = %q, %v; want * OK", greeting, err)
		}
	})
}

// TestPasswordSprayingFromOneIPIsCut proves the gateway, wired as
// cmd/imap-gateway wires it (a connlimiter behind both the server and the
// backend), detects password spraying (RFC 9051 §11.7): one address trying one
// password against many accounts never trips any account's lock, but its
// failures are counted together and the connection is cut at the threshold.
func TestPasswordSprayingFromOneIPIsCut(t *testing.T) {
	const host = "imap.test"
	lim := connlimiter.New(connlimiter.Config{
		MaxPerIP:        20,
		MaxGlobal:       100,
		AuthMaxFails:    3,
		AuthBanWindow:   time.Minute,
		AuthBanDuration: time.Minute,
	})
	reject := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	cert := rsaCertificate(t, host)
	cfg := ServerTLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil })
	port := serveIMAPTLSWithBackend(t, host, cfg, lim, reject)

	client := tls.Client(dialPort(t, port), &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // throwaway certificate
	r := bufio.NewReader(client)
	if line, err := r.ReadString('\n'); err != nil || !strings.HasPrefix(line, "* OK") {
		t.Fatalf("greeting = %q, %v", line, err)
	}
	for i := 1; i <= 3; i++ {
		user := fmt.Sprintf("user%d@example.com", i)
		tag := "a" + strconv.Itoa(i)
		if _, err := fmt.Fprintf(client, "%s LOGIN %s Summer2026\r\n", tag, user); err != nil {
			t.Fatalf("send LOGIN %d: %v", i, err)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reply to LOGIN %d: %v", i, err)
		}
		if lim.IsUserBanned(user) {
			t.Fatalf("one failure locked %s; spraying must be caught per address, not by locking each account", user)
		}
		if i < 3 {
			if !strings.HasPrefix(line, tag+" NO [AUTHENTICATIONFAILED]") {
				t.Fatalf("LOGIN %d reply = %q, want NO [AUTHENTICATIONFAILED]", i, line)
			}
			continue
		}
		// The cut-off is an untagged BYE, then the tagged NO [UNAVAILABLE]: a
		// temporary refusal, so the client may try again later (RFC 5530 §3).
		if !strings.HasPrefix(line, "* BYE Too many authentication failures") {
			t.Fatalf("LOGIN %d reply = %q, want the spraying cut-off's BYE", i, line)
		}
		if line, err = r.ReadString('\n'); err != nil || !strings.HasPrefix(line, tag+" NO [UNAVAILABLE] Too many authentication failures") {
			t.Fatalf("LOGIN %d tagged reply = %q, %v, want the spraying cut-off", i, line, err)
		}
	}
	if !lim.IsBanned("127.0.0.1") {
		t.Fatal("the spraying address is not banned")
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, err := r.ReadString('\n'); err == nil {
		t.Fatalf("connection still open after the cut-off, read %q", line)
	}
}

func serveIMAPTLS(t *testing.T, host string, cfg *tls.Config, lim goimap.Limiter, api http.Handler) int {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return listen(t, imapserver.New(host, NewBackend(apiclient.New(srv.URL), nil), cfg, lim))
}

func serveIMAPTLSWithBackend(t *testing.T, host string, cfg *tls.Config, lim *connlimiter.Limiter, api http.Handler) int {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return listen(t, imapserver.New(host, NewBackend(apiclient.New(srv.URL), lim), cfg, lim))
}

func listen(t *testing.T, srv *imapserver.Server) int {
	t.Helper()
	listener, port := reloadtest.Listen(t)
	srv.Serve(imapserver.Listeners{IMAPTLS: listener})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return port
}

func dialPort(t *testing.T, port int) net.Conn {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var conn net.Conn
	var err error
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		conn, err = net.DialTimeout("tcp", addr, time.Second)
		if err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// rsaCertificate issues a throwaway self-signed RSA certificate: the mandated
// suite is ECDHE_RSA, so it needs an RSA key.
func rsaCertificate(t *testing.T, host string) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
