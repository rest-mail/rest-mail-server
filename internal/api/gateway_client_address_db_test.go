package api

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	pop3gw "github.com/rest-mail/rest-mail-server/internal/gateway/pop3"
)

// The sign-in throttle and the failed-sign-in slowing are per client address,
// and a gateway stands between the mail client and the API: the address they
// key on has to be the mail client's, or everyone using a gateway shares one
// allowance (#1957).

const signInBurst = config.DefaultAuthRateLimitBurst

// throttledGatewayFixture has the sign-in throttle on, with the production
// burst and a refill too slow to matter during the test.
func throttledGatewayFixture(t *testing.T, internalMTLS bool) *gatewayFixture {
	t.Helper()
	cfg := &config.Config{Environment: "test", InternalMTLSEnabled: internalMTLS,
		AuthRateLimitEnabled: true, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: signInBurst}
	f := newCredentialFixtureWith(t, cfg)
	return &gatewayFixture{credentialFixture: f, internal: f.gatewayRouter(), app: f.createAppPassword("Mail app")}
}

// fromSource dials over TCP from the given loopback source address, which is
// the address the gateway sees the mail client at.
func fromSource(t *testing.T, source, addr string) net.Conn {
	t.Helper()
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}, Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial from %s: %v", source, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

// signIn is one sign-in over a gateway's real protocol from a source address,
// reporting whether it was accepted.
type signIn func(t *testing.T, source, password string) bool

// imapGateway runs real IMAP sessions over TCP with the real gateway backend
// and API client, in front of the real internal router.
func imapGateway(t *testing.T, g *gatewayFixture) signIn {
	t.Helper()
	internal := httptest.NewServer(asGateway(imapCN, g.internal))
	t.Cleanup(internal.Close)
	backend := imapgw.NewBackend(apiclient.New(internal.URL, apiclient.WithInternalMTLS(internal.URL, nil)), nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go imapserver.NewSession(conn, backend, "imap.test", nil, goimap.NopLimiter{}).Handle()
		}
	}()
	return func(t *testing.T, source, password string) bool {
		t.Helper()
		conn := fromSource(t, source, listener.Addr().String())
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("greeting for %s: %v", source, err)
		}
		if _, err := fmt.Fprintf(conn, "a1 LOGIN %s %s\r\n", g.address, password); err != nil {
			t.Fatal(err)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("LOGIN from %s: %v", source, err)
		}
		return strings.HasPrefix(line, "a1 OK")
	}
}

// pop3Gateway is the gateway's real POP3 server in front of the real internal router.
func pop3Gateway(t *testing.T, g *gatewayFixture) signIn {
	t.Helper()
	internal := httptest.NewServer(asGateway(pop3CN, g.internal))
	t.Cleanup(internal.Close)
	server := pop3gw.NewServer(pop3gw.NewBackend(apiclient.New(internal.URL, apiclient.WithInternalMTLS(internal.URL, nil)), nil), nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(listener); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return func(t *testing.T, source, password string) bool {
		t.Helper()
		conn := fromSource(t, source, listener.Addr().String())
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("greeting for %s: %v", source, err)
		}
		reply := func(command string) string {
			t.Helper()
			if _, err := fmt.Fprintf(conn, "%s\r\n", command); err != nil {
				t.Fatal(err)
			}
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("%s from %s: %v", strings.Fields(command)[0], source, err)
			}
			return line
		}
		reply("USER " + g.address)
		return strings.HasPrefix(reply("PASS "+password), "+OK")
	}
}

// submissionGateway is the SMTP gateway's real submission port in front of the
// real public and internal routers.
func submissionGateway(t *testing.T, g *gatewayFixture) signIn {
	t.Helper()
	_, addr, rootCAs := startSubmission(t, g.db, g.router, g.internal)
	return func(t *testing.T, source, password string) bool {
		t.Helper()
		return submissionAuth(t, source, addr, rootCAs, g.address, password) == nil
	}
}

func submissionAuth(t *testing.T, source, addr string, rootCAs *x509.CertPool, address, password string) error {
	t.Helper()
	conn := tls.Client(fromSource(t, source, addr), &tls.Config{RootCAs: rootCAs, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	c, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		t.Fatalf("SMTP greeting for %s: %v", source, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("client.test"); err != nil {
		t.Fatalf("EHLO from %s: %v", source, err)
	}
	return c.Auth(smtp.PlainAuth("", address, password, "127.0.0.1"))
}

var everyGateway = []struct {
	protocol string
	start    func(*testing.T, *gatewayFixture) signIn
}{{"imap", imapGateway}, {"pop3", pop3Gateway}, {"smtp", submissionGateway}}

func clientSource(i int) string { return fmt.Sprintf("127.0.1.%d", i+1) }

// More mail clients than the burst sign in through one gateway at once, each
// from its own address: none is refused because of the others.
func TestSignInsFromManyClientsDoNotShareAnAllowance(t *testing.T) {
	for _, gw := range everyGateway {
		t.Run(gw.protocol, func(t *testing.T) {
			g := throttledGatewayFixture(t, true)
			signIn := gw.start(t, g)
			var refused []string
			for i := range signInBurst + 5 {
				if source := clientSource(i); !signIn(t, source, g.app.Password) {
					refused = append(refused, source)
				}
			}
			if len(refused) != 0 {
				t.Fatalf("%d of %d mail clients on different addresses were refused sign-in (%v): they share one allowance",
					len(refused), signInBurst+5, refused)
			}
		})
	}
}

// One client address signing in over and over is still throttled, at the same
// burst, and that takes nothing from any other address.
func TestRepeatedSignInsFromOneClientAreThrottled(t *testing.T) {
	for _, gw := range everyGateway {
		t.Run(gw.protocol, func(t *testing.T) {
			g := throttledGatewayFixture(t, true)
			signIn := gw.start(t, g)
			busy, other := clientSource(100), clientSource(101)
			for i := range signInBurst {
				if !signIn(t, busy, g.app.Password) {
					t.Fatalf("sign-in %d of the burst was refused", i+1)
				}
			}
			if signIn(t, busy, g.app.Password) {
				t.Fatalf("sign-in %d from one address was accepted, past the burst of %d", signInBurst+1, signInBurst)
			}
			if !signIn(t, other, g.app.Password) {
				t.Fatal("another client was refused because of the busy one")
			}
		})
	}
}

func auditIPs(t *testing.T, g *gatewayFixture, action string) []string {
	t.Helper()
	var entries []models.AuditEntry
	if err := g.db.Where("action = ?", action).Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	ips := make([]string, len(entries))
	for i, e := range entries {
		ips[i] = e.IP
	}
	return ips
}

func (g *gatewayFixture) openFrom(cn, protocol, password, clientIP string) *httptest.ResponseRecorder {
	return g.as(cn, http.MethodPost, "/api/v1/gateway-sessions", "", openBodyFrom(protocol, g.address, password, clientIP))
}

// One client's wrong passwords use up its own budget, not that of everyone
// using the gateway: once refused, it stays refused even with the right
// password, while another client signs in.
func TestFailedSignInsSlowOnlyTheirClient(t *testing.T) {
	g := throttledGatewayFixture(t, true)
	const guesser, other = "198.51.100.1", "198.51.100.2"
	for i := range signInBurst {
		if rr := g.openFrom(imapCN, "imap", "wrong", guesser); rr.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d = %d %s", i+1, rr.Code, rr.Body.String())
		}
	}
	if rr := g.openFrom(imapCN, "imap", "wrong", guesser); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("a guess past the budget = %d %s, want 429", rr.Code, rr.Body.String())
	}
	if rr := g.openFrom(imapCN, "imap", g.app.Password, guesser); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("the guesser's right password = %d %s, want 429", rr.Code, rr.Body.String())
	}
	if rr := g.openFrom(imapCN, "imap", g.app.Password, other); rr.Code != http.StatusCreated {
		t.Fatalf("another client through the same gateway = %d %s, want 201", rr.Code, rr.Body.String())
	}
}

// What the audit log says about a gateway's sign-in names the mail client's
// address, whatever else the request says.
func TestGatewaySignInsAreAuditedWithTheClientsAddress(t *testing.T) {
	g := newGatewayFixture(t)
	body := func(password, clientIP string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/gateway-sessions", strings.NewReader(openBodyFrom("imap", g.address, password, clientIP)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "203.0.113.99")
		req.Header.Set("X-Real-IP", "203.0.113.98")
		req.RemoteAddr = "192.0.2.20:50000"
		rr := httptest.NewRecorder()
		asGateway(imapCN, g.internal).ServeHTTP(rr, req)
		return rr
	}
	if rr := body(g.app.Password, "198.51.100.3"); rr.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", rr.Code, rr.Body.String())
	}
	if rr := body("wrong", "198.51.100.4"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password = %d %s", rr.Code, rr.Body.String())
	}
	if got := auditIPs(t, g, "auth.gateway_session_open"); len(got) != 1 || got[0] != "198.51.100.3" {
		t.Errorf("the sign-in is recorded from %v, want the client's 198.51.100.3", got)
	}
	if got := auditIPs(t, g, "auth.credential_refused"); len(got) != 1 || got[0] != "198.51.100.4" {
		t.Errorf("the refusal is recorded from %v, want the client's 198.51.100.4", got)
	}
}

// A caller that is not a gateway signing mail clients in cannot choose the
// address it is throttled by, in the body or a header: on the public route,
// and with a certificate that is not such a gateway's. Its own address runs out.
func TestOnlyAGatewayChoosesTheClientAddress(t *testing.T) {
	attempt := func(h http.Handler, i int, g *gatewayFixture) *httptest.ResponseRecorder {
		claimed := fmt.Sprintf("198.51.100.%d", i+1)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/gateway-sessions", strings.NewReader(openBodyFrom("imap", g.address, g.app.Password, claimed)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", claimed)
		req.Header.Set("X-Real-IP", claimed)
		req.RemoteAddr = "192.0.2.77:50000"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	for _, tc := range []struct {
		name         string
		internalMTLS bool
		handler      func(*gatewayFixture) http.Handler
	}{
		{"the public route", false, func(g *gatewayFixture) http.Handler { return g.router }},
		{"a certificate naming no gateway", true, func(g *gatewayFixture) http.Handler { return asGateway("someone-else", g.internal) }},
		{"the delivery gateway's certificate", true, func(g *gatewayFixture) http.Handler { return asGateway(deliveryCN, g.internal) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := throttledGatewayFixture(t, tc.internalMTLS)
			h := tc.handler(g)
			for i := range signInBurst {
				rr := attempt(h, i, g)
				if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "gateway_required") {
					t.Fatalf("attempt %d = %d %s, want 403 gateway_required", i+1, rr.Code, rr.Body.String())
				}
			}
			if rr := attempt(h, signInBurst, g); rr.Code != http.StatusTooManyRequests {
				t.Fatalf("a new address past the burst = %d %s, want 429: the caller chose what it is throttled by", rr.Code, rr.Body.String())
			}
			for _, ip := range auditIPs(t, g, "auth.gateway_session_open") {
				if ip != "192.0.2.77" {
					t.Fatalf("recorded from %s, want the caller's own 192.0.2.77", ip)
				}
			}
		})
	}
}

// A gateway has to say whose sign-in this is, before any password is checked.
func TestAGatewayMustStateTheClientAddress(t *testing.T) {
	g := newGatewayFixture(t)
	for name, body := range map[string]string{
		"none":        fmt.Sprintf(`{"protocol":"imap","address":%q,"password":"wrong"}`, g.address),
		"not an IP":   openBodyFrom("imap", g.address, "wrong", "client.example.test"),
		"with a port": openBodyFrom("imap", g.address, "wrong", "198.51.100.5:143"),
	} {
		t.Run(name, func(t *testing.T) {
			rr := g.as(imapCN, http.MethodPost, "/api/v1/gateway-sessions", "", body)
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "client_ip") {
				t.Fatalf("got %d %s, want 422 naming client_ip", rr.Code, rr.Body.String())
			}
		})
	}
	if got := auditIPs(t, g, "auth.credential_refused"); len(got) != 0 {
		t.Fatalf("the password was checked and refused from %v", got)
	}
}

// Other sign-ins are as they were: POST /api/v1/auth/login takes no client_ip
// and goes by its caller's own address, even through the same limiter.
func TestLoginStillGoesByItsCallersOwnAddress(t *testing.T) {
	g := throttledGatewayFixture(t, true)
	login := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
			strings.NewReader(fmt.Sprintf(`{"email":%q,"password":"wrong","client":"native","client_ip":"198.51.100.6"}`, g.address)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "198.51.100.6")
		req.RemoteAddr = "192.0.2.88:50000"
		rr := httptest.NewRecorder()
		g.router.ServeHTTP(rr, req)
		return rr
	}
	for i := range signInBurst {
		if rr := login(); rr.Code == http.StatusTooManyRequests {
			t.Fatalf("login %d of the burst was throttled", i+1)
		}
	}
	if rr := login(); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("a login past the burst = %d %s, want 429", rr.Code, rr.Body.String())
	}
}
