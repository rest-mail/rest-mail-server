package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	pop3gw "github.com/rest-mail/rest-mail-server/internal/gateway/pop3"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/mtls"
	"github.com/rest-mail/rest-mail-server/internal/mtls/mtlstest"
	yaml "go.yaml.in/yaml/v2"
)

// #2098 AC03: gateway sessions identify the real caller safely.
//
// Every test here runs the API's internal listener as production does: a real
// TLS listener that requires a client certificate signed by the internal CA
// (mtls.ServerTLSConfig), with each gateway presenting its own certificate
// (mtls.ClientTLSConfig). The mail clients speak real IMAP, POP3 and
// submission over TCP to the real gateways, which call that listener through
// their real API clients. The mail clients connect from a loopback address
// other than 127.0.0.1, the address every gateway reaches the API from, so
// what the API records can only have come from the gateway's statement.
//
// Criteria and their tests:
//   - Sessions end with their credential or account (SRC-1586):
//     TestAC03MailSessionsEndWithTheCredentialOrAccount.
//   - Client identity comes only from an authenticated gateway (SRC-1957):
//     TestAC03OnlyAnAuthenticatedGatewayStatesTheClientAddress.
//   - Independent per-client-address budgets (SRC-1957):
//     TestAC03EachClientAddressHasItsOwnAllowanceOverMTLS, here over real
//     mTLS with stated addresses; TestSignInsFromManyClientsDoNotShareAnAllowance
//     proves it with real sockets from 20 source addresses where 127/8 is all
//     loopback (Linux CI).
//   - HTTP Basic and protocolSignIn stay removed (SRC-1935):
//     TestAC03RemovedSignInRoutesStayRemoved.

// ac03Topology is one API with its public listener and its real mTLS internal
// listener, and the internal CA that signs the gateways' certificates.
type ac03Topology struct {
	*credentialFixture
	public   *httptest.Server
	internal *httptest.Server
	ca       *mtlstest.CA
	caPath   string
	dir      string
	// mailHost is the address the mail clients connect to the gateways from.
	mailHost string
	app      createdCredential
}

func newAC03Topology(t *testing.T, cfg config.Config) *ac03Topology {
	t.Helper()
	cfg.Environment, cfg.InternalMTLSEnabled = "test", true
	f := newCredentialFixtureWith(t, &cfg)
	if f.routers.Internal == nil {
		t.Fatal("no internal router with mTLS enabled")
	}
	if f.routers.signInTarpit != nil {
		// A wrong password waits as a failed login does; these tests count
		// answers, not seconds.
		f.routers.signInTarpit.SetWait(func(context.Context, time.Duration) {})
	}
	a := &ac03Topology{credentialFixture: f, dir: t.TempDir(), mailHost: mailClientHost(t)}

	ca, err := mtlstest.NewCA("rest-mail internal CA")
	if err != nil {
		t.Fatal(err)
	}
	a.ca = ca
	a.caPath = a.write("ca.crt", ca.CertPEM)
	now := time.Now()
	serverCert, serverKey, err := ca.IssueServer("rest-mail-api", nil, []net.IP{net.ParseIP("127.0.0.1")}, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := mtls.ServerTLSConfig(a.caPath, a.write("server.crt", serverCert), a.write("server.key", serverKey))
	if err != nil {
		t.Fatal(err)
	}
	a.internal = httptest.NewUnstartedServer(f.routers.Internal)
	a.internal.TLS = serverTLS
	a.internal.StartTLS()
	t.Cleanup(a.internal.Close)
	a.public = httptest.NewServer(f.routers.Public)
	t.Cleanup(a.public.Close)

	a.app = f.createAppPassword("Mail app")
	return a
}

// mailClientHost is a loopback address other than 127.0.0.1 to connect from:
// 127.0.1.9 where all of 127/8 is loopback (Linux), else ::1 (macOS).
func mailClientHost(t *testing.T) string {
	t.Helper()
	for _, host := range []string{"127.0.1.9", "::1"} {
		if l, err := net.Listen("tcp", net.JoinHostPort(host, "0")); err == nil {
			_ = l.Close()
			return host
		}
	}
	t.Fatal("no loopback address other than 127.0.0.1 to connect mail clients from")
	return ""
}

func (a *ac03Topology) write(name string, data []byte) string {
	a.t.Helper()
	path := filepath.Join(a.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		a.t.Fatal(err)
	}
	return path
}

// certificate is the TLS configuration of a caller presenting a client
// certificate named cn and signed by ca, trusting the API's server
// certificate.
func (a *ac03Topology) certificate(ca *mtlstest.CA, cn string) *tls.Config {
	a.t.Helper()
	now := time.Now()
	certPEM, keyPEM, err := ca.IssueClient(cn, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		a.t.Fatal(err)
	}
	name := fmt.Sprintf("%s-%d", cn, time.Now().UnixNano())
	cfg, err := mtls.ClientTLSConfig(a.caPath, a.write(name+".crt", certPEM), a.write(name+".key", keyPEM))
	if err != nil {
		a.t.Fatal(err)
	}
	return cfg
}

// gatewayClient is the API client of the gateway whose certificate names cn,
// as cmd/imap-gateway, cmd/pop3-gateway and cmd/smtp-gateway build it.
func (a *ac03Topology) gatewayClient(cn string) *apiclient.Client {
	a.t.Helper()
	return apiclient.New(a.public.URL, apiclient.WithInternalMTLS(a.internal.URL, a.certificate(a.ca, cn)))
}

func (a *ac03Topology) mailListener() net.Listener {
	a.t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(a.mailHost, "0"))
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// startIMAP runs the IMAP gateway and returns its address.
func (a *ac03Topology) startIMAP() string {
	a.t.Helper()
	backend := imapgw.NewBackend(a.gatewayClient(imapCN), nil)
	listener := a.mailListener()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go imapserver.NewSession(conn, backend, "imap.test", nil, goimap.NopLimiter{}).Handle()
		}
	}()
	return listener.Addr().String()
}

// startPOP3 runs the POP3 gateway and returns its address.
func (a *ac03Topology) startPOP3() string {
	a.t.Helper()
	server := pop3gw.NewServer(pop3gw.NewBackend(a.gatewayClient(pop3CN), nil), nil)
	listener := a.mailListener()
	if err := server.Serve(listener); err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}

// startSubmission runs the SMTP gateway's submission port, with its delivery
// and submission clients as cmd/smtp-gateway makes them, and returns its
// address and the roots its certificate verifies against.
func (a *ac03Topology) startSubmission() (string, *x509.CertPool) {
	a.t.Helper()
	api := smtpgw.Clients{Delivery: a.gatewayClient(deliveryCN), Submission: a.gatewayClient(submissionCN)}
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	a.t.Cleanup(certSrv.Close)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(certSrv.Certificate())
	gateway := smtpgw.NewServer("mail.example.test", api,
		&tls.Config{Certificates: certSrv.TLS.Certificates, MinVersion: tls.VersionTLS12},
		smtpgw.NewStore(a.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	listener := a.mailListener()
	if err := gateway.Serve(smtpgw.SMTPListeners{SubmissionTLS: listener}); err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(gateway.Shutdown)
	return listener.Addr().String(), rootCAs
}

// mailConn is one mail client's TCP connection to a gateway.
type mailConn struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	tag  int
}

func (a *ac03Topology) dialMail(t *testing.T, addr string) *mailConn {
	t.Helper()
	conn := fromSource(t, a.mailHost, addr)
	c := &mailConn{t: t, conn: conn, r: bufio.NewReader(conn)}
	if _, err := c.r.ReadString('\n'); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	return c
}

// imap sends one IMAP command and returns its untagged lines and its tagged
// answer, or "closed" when the gateway hung up first.
func (c *mailConn) imap(command string) (untagged []string, tagged string) {
	c.t.Helper()
	c.tag++
	tag := fmt.Sprintf("a%d", c.tag)
	if _, err := fmt.Fprintf(c.conn, "%s %s\r\n", tag, command); err != nil {
		return nil, "closed"
	}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return untagged, "closed"
		}
		if rest, ok := strings.CutPrefix(line, tag+" "); ok {
			return untagged, strings.TrimSpace(rest)
		}
		untagged = append(untagged, strings.TrimSpace(line))
	}
}

// pop3 sends one POP3 command and returns its status line, or "closed".
func (c *mailConn) pop3(command string) string {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "%s\r\n", command); err != nil {
		return "closed"
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "closed"
	}
	return strings.TrimSpace(line)
}

// pop3Body reads a multi-line POP3 answer to its terminator.
func (c *mailConn) pop3Body() string {
	c.t.Helper()
	var body strings.Builder
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("POP3 body: %v", err)
		}
		if line == ".\r\n" {
			return body.String()
		}
		body.WriteString(strings.TrimPrefix(line, "."))
	}
}

func (a *ac03Topology) dialSubmission(t *testing.T, addr string, rootCAs *x509.CertPool) *smtp.Client {
	t.Helper()
	conn := tls.Client(fromSource(t, a.mailHost, addr), &tls.Config{RootCAs: rootCAs, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	c, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		t.Fatalf("SMTP greeting: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Hello("client.test"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	return c
}

// sessions is every gateway session the API holds, oldest first.
func (a *ac03Topology) sessions() []models.GatewaySession {
	a.t.Helper()
	var all []models.GatewaySession
	if err := a.db.Order("id ASC").Find(&all).Error; err != nil {
		a.t.Fatal(err)
	}
	return all
}

// auditIPs is the address on every audit entry with action, oldest first.
func (a *ac03Topology) auditIPs(action string) []string {
	var ips []string
	for _, e := range a.entries(action) {
		ips = append(ips, e.IP)
	}
	return ips
}

// mailProtocol is one of the three ways a mail client signs in through a
// gateway: sign in, run a command that reaches the API with the connection's
// session, and report whether it was served.
type mailProtocol struct {
	protocol, service string
	// refused starts the gateway's answer to a command whose session ended.
	refused string
	start   func(a *ac03Topology) func(t *testing.T, address, password string) mailSession
}

// mailSession is a signed-in connection: next runs the protocol's next command
// and reports whether it was served, with what the gateway answered.
type mailSession struct {
	signedIn bool
	answer   string
	next     func() (bool, string)
}

var ac03MailProtocols = []mailProtocol{
	{"imap", models.GatewayServiceIMAP, "NO ", func(a *ac03Topology) func(*testing.T, string, string) mailSession {
		addr := a.startIMAP()
		return func(t *testing.T, address, password string) mailSession {
			c := a.dialMail(t, addr)
			_, login := c.imap(fmt.Sprintf("LOGIN %q %q", address, password))
			return mailSession{signedIn: strings.HasPrefix(login, "OK"), answer: login, next: func() (bool, string) {
				untagged, tagged := c.imap("STATUS INBOX (MESSAGES)")
				return strings.HasPrefix(tagged, "OK") && slices.Contains(untagged, `* STATUS "INBOX" (MESSAGES 1)`), tagged
			}}
		}
	}},
	{"pop3", models.GatewayServicePOP3, "-ERR ", func(a *ac03Topology) func(*testing.T, string, string) mailSession {
		addr := a.startPOP3()
		return func(t *testing.T, address, password string) mailSession {
			c := a.dialMail(t, addr)
			c.pop3("USER " + address)
			pass := c.pop3("PASS " + password)
			return mailSession{signedIn: strings.HasPrefix(pass, "+OK"), answer: pass, next: func() (bool, string) {
				status := c.pop3("RETR 1")
				if !strings.HasPrefix(status, "+OK") {
					return false, status
				}
				return strings.Contains(c.pop3Body(), "Subject: AC03 inbox"), status
			}}
		}
	}},
	{"smtp", models.GatewayServiceSMTPSubmission, "530 ", func(a *ac03Topology) func(*testing.T, string, string) mailSession {
		addr, rootCAs := a.startSubmission()
		return func(t *testing.T, address, password string) mailSession {
			c := a.dialSubmission(t, addr, rootCAs)
			if err := c.Auth(smtp.PlainAuth("", address, password, "127.0.0.1")); err != nil {
				return mailSession{answer: err.Error()}
			}
			return mailSession{signedIn: true, answer: "235", next: func() (bool, string) {
				if err := c.Mail(address); err != nil {
					return false, err.Error()
				}
				if err := c.Reset(); err != nil {
					return false, err.Error()
				}
				return true, "250"
			}}
		}
	}},
}

// deliverToInbox puts one message in address's INBOX through the delivery
// gateway's own certificate, as inbound mail arrives.
func (a *ac03Topology) deliverToInbox(address string) {
	a.t.Helper()
	// The sender has already retried past greylisting, as eightBitServer.pastGreylist records.
	now := time.Now()
	if err := a.db.Create(&models.GreylistEntry{Sender: "sender@remote.test", Recipient: address, SourceIP: "192.0.2.1",
		FirstSeen: now.Add(-time.Hour), RetryAfter: now.Add(-time.Minute), Passed: true, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
		a.t.Fatal(err)
	}
	message := fmt.Sprintf("From: sender@remote.test\r\nTo: %s\r\nSubject: AC03 inbox\r\nDate: %s\r\nMessage-ID: <%d@remote.test>\r\n\r\nHello.\r\n",
		address, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
	if _, err := a.gatewayClient(deliveryCN).DeliverMessage(&apiclient.DeliverRequest{
		Address: address, Sender: "sender@remote.test",
		ClientIP: "192.0.2.1", HeloName: "mail.remote.test", RawMessage: []byte(message),
	}); err != nil {
		a.t.Fatalf("deliver to %s: %v", address, err)
	}
}

// Criterion 1 (SRC-1586). A real IMAP, POP3 or submission sign-in through its
// gateway opens one session for that connection, for the real account, its
// app password and the gateway's own service. Revoking the app password, or
// an administrator disabling the account, refuses that connection's next
// command and closes its session, while another account's connection through
// the same gateway goes on. The ended credential cannot sign in again.
func TestAC03MailSessionsEndWithTheCredentialOrAccount(t *testing.T) {
	for _, mp := range ac03MailProtocols {
		for _, end := range []struct {
			name string
			do   func(t *testing.T, a *ac03Topology)
			// refusedAfter are the passwords that no longer sign in.
			refusedAfter func(a *ac03Topology) []string
		}{
			{
				name: "app password revoked",
				do: func(t *testing.T, a *ac03Topology) {
					path := fmt.Sprintf("/api/v1/accounts/%d/credentials/%d", a.accountID, a.app.ID)
					if rr := a.do(http.MethodDelete, path, a.session, ""); rr.Code != http.StatusNoContent {
						t.Fatalf("revoke the app password: %d %s", rr.Code, rr.Body.String())
					}
				},
				refusedAfter: func(a *ac03Topology) []string { return []string{a.app.Password} },
			},
			{
				name: "account disabled",
				do: func(t *testing.T, a *ac03Topology) {
					admin := "Bearer " + adminToken(t, a.jwt, []string{middleware.CapAccountsWrite})
					path := fmt.Sprintf("/api/v1/admin/mailboxes/%d", a.mailboxID)
					if rr := a.do(http.MethodPatch, path, admin, `{"active":false}`); rr.Code != http.StatusOK {
						t.Fatalf("disable the account: %d %s", rr.Code, rr.Body.String())
					}
				},
				refusedAfter: func(a *ac03Topology) []string { return []string{a.app.Password, credentialTestPassword} },
			},
		} {
			t.Run(mp.protocol+"/"+end.name, func(t *testing.T) {
				a := newAC03Topology(t, config.Config{})
				var domain models.Domain
				if err := a.db.First(&domain).Error; err != nil {
					t.Fatal(err)
				}
				danaID := a.seedMailbox(domain.ID, "dana")
				_, danaPassword, err := repositories.NewCredentialRepository(a.db).CreateAppPassword(danaID, danaID, "Dana's phone", nil)
				if err != nil {
					t.Fatal(err)
				}
				a.deliverToInbox(a.address)
				a.deliverToInbox("dana@example.test")
				signIn := mp.start(a)

				chris := signIn(t, a.address, a.app.Password)
				if !chris.signedIn {
					t.Fatalf("sign-in with the app password: %s", chris.answer)
				}
				dana := signIn(t, "dana@example.test", danaPassword)
				if !dana.signedIn {
					t.Fatalf("dana's sign-in: %s", dana.answer)
				}
				opened := a.sessions()
				if len(opened) != 2 {
					t.Fatalf("%d gateway sessions for two connections, want 2", len(opened))
				}
				s := opened[0]
				if s.AccountID != a.accountID || s.CredentialID != a.app.ID || s.CredentialKind != models.CredentialKindAppPassword ||
					s.Service != mp.service || s.Protocol != mp.protocol || s.ClosedAt != nil {
					t.Fatalf("the connection's session: %+v, want open, for account %d, app password %d, service %s, protocol %s",
						s, a.accountID, a.app.ID, mp.service, mp.protocol)
				}
				if ok, answer := chris.next(); !ok {
					t.Fatalf("a command before the end was refused: %s", answer)
				}

				end.do(t, a)

				if ok, answer := chris.next(); ok || !strings.HasPrefix(answer, mp.refused) {
					t.Fatalf("the next command after the end: served %v, %q, want refused %q…", ok, answer, mp.refused)
				} else {
					t.Logf("the next command after the end: %s", answer)
				}
				for _, closed := range a.sessions() {
					if closed.ID == s.ID && closed.ClosedAt == nil {
						t.Errorf("the ended connection's session is still open")
					}
					if closed.AccountID == danaID && closed.ClosedAt != nil {
						t.Errorf("dana's session was closed too")
					}
				}
				if ok, answer := dana.next(); !ok {
					t.Errorf("another account's connection through the same gateway was refused: %s", answer)
				}
				for _, password := range end.refusedAfter(a) {
					if again := signIn(t, a.address, password); again.signedIn {
						t.Errorf("a new sign-in after the end was accepted")
					}
				}
			})
		}
	}
}

// ac03Request is one raw request to a listener, sent with tlsCfg (nil: plain HTTP).
func ac03Request(t *testing.T, tlsCfg *tls.Config, method, url, authorization, body string, headers map[string]string) (int, string, http.Header, error) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header, nil
}

// forgedIdentityHeaders claim a client address the way a proxy would.
func forgedIdentityHeaders(ip string) map[string]string {
	return map[string]string{"X-Forwarded-For": ip, "X-Real-IP": ip, "Forwarded": "for=" + ip, "True-Client-IP": ip}
}

// Criterion 2 (SRC-1957). The address a sign-in is throttled and recorded by
// is the mail client's, and only a gateway authenticated by its own
// certificate states it. Real sign-ins through the three gateways are
// recorded from the address the mail client connected from, never the
// gateway's. A claimed client address in the body or in proxy headers is
// refused or ignored from every other caller: a certificate naming no
// gateway, the delivery gateway's certificate, a gateway's name on a
// certificate the internal CA did not sign, no certificate, and the public
// listener. Even from a gateway, proxy headers never override its statement.
func TestAC03OnlyAnAuthenticatedGatewayStatesTheClientAddress(t *testing.T) {
	a := newAC03Topology(t, config.Config{})
	a.deliverToInbox(a.address)
	const gatewayHost = "127.0.0.1"

	t.Run("real sign-ins are recorded from the mail client", func(t *testing.T) {
		for _, mp := range ac03MailProtocols {
			signIn := mp.start(a)
			if s := signIn(t, a.address, a.app.Password); !s.signedIn {
				t.Fatalf("%s sign-in: %s", mp.protocol, s.answer)
			}
			if s := signIn(t, a.address, "not the app password"); s.signedIn {
				t.Fatalf("%s sign-in with a wrong password was accepted", mp.protocol)
			}
		}
		want := []string{a.mailHost, a.mailHost, a.mailHost}
		if got := a.auditIPs("auth.gateway_session_open"); !slices.Equal(got, want) {
			t.Errorf("sign-ins recorded from %v, want the mail client's %v (the gateways reach the API from %s)", got, want, gatewayHost)
		}
		if got := a.auditIPs("auth.credential_refused"); !slices.Equal(got, want) {
			t.Errorf("refusals recorded from %v, want the mail client's %v", got, want)
		}
	})

	before := len(a.sessions())
	opened := len(a.entries("auth.gateway_session_open"))
	const claimed = "198.51.100.50"
	body := openBodyFrom("imap", a.address, a.app.Password, claimed)
	otherCA, err := mtlstest.NewCA("not the internal CA")
	if err != nil {
		t.Fatal(err)
	}
	noCertificate := &tls.Config{RootCAs: a.certificate(a.ca, imapCN).RootCAs, MinVersion: tls.VersionTLS12}

	for _, tc := range []struct {
		name   string
		tls    *tls.Config
		url    string
		status int    // 0: the TLS handshake fails
		code   string // the error code the answer names
	}{
		{"a certificate naming no gateway", a.certificate(a.ca, "someone-else"), a.internal.URL, http.StatusForbidden, "gateway_required"},
		{"the delivery gateway's certificate", a.certificate(a.ca, deliveryCN), a.internal.URL, http.StatusForbidden, "gateway_required"},
		{"the IMAP gateway's name, signed by another CA", a.certificate(otherCA, imapCN), a.internal.URL, 0, ""},
		{"no certificate", noCertificate, a.internal.URL, 0, ""},
		{"the public listener", nil, a.public.URL, http.StatusNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, answer, _, err := ac03Request(t, tc.tls, http.MethodPost, tc.url+"/api/v1/gateway-sessions", "", body, forgedIdentityHeaders("198.51.100.51"))
			switch {
			case tc.status == 0:
				if err == nil {
					t.Fatalf("the handshake succeeded: %d %s", status, answer)
				}
			case err != nil:
				t.Fatalf("request: %v", err)
			case status != tc.status || !strings.Contains(answer, tc.code):
				t.Fatalf("got %d %s, want %d %s", status, answer, tc.status, tc.code)
			}
		})
	}
	if got := len(a.sessions()); got != before {
		t.Fatalf("%d sessions opened by callers that are not gateways", got-before)
	}
	// The two callers that reached the route with a certificate are recorded
	// as refused, from their own address.
	refusals := a.entries("auth.gateway_session_open")[opened:]
	if len(refusals) != 2 {
		t.Fatalf("%d entries for the callers that are not gateways, want the 2 that reached the route", len(refusals))
	}
	for _, e := range refusals {
		if e.Status != http.StatusForbidden || e.ErrorCode != "gateway_required" || e.IP != gatewayHost {
			t.Errorf("recorded %d %s from %s, want 403 gateway_required from the caller's own %s", e.Status, e.ErrorCode, e.IP, gatewayHost)
		}
	}
	var all []models.AuditEntry
	if err := a.db.Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if strings.HasPrefix(e.IP, "198.51.100.5") {
			t.Errorf("%s recorded from the claimed %s", e.Action, e.IP)
		}
	}

	t.Run("a gateway's statement, never a proxy header", func(t *testing.T) {
		imap := a.certificate(a.ca, imapCN)
		status, answer, _, err := ac03Request(t, imap, http.MethodPost, a.internal.URL+"/api/v1/gateway-sessions", "",
			openBodyFrom("imap", a.address, a.app.Password, "198.51.100.60"), forgedIdentityHeaders("198.51.100.61"))
		if err != nil || status != http.StatusCreated {
			t.Fatalf("open as the IMAP gateway: %d %s %v", status, answer, err)
		}
		ips := a.auditIPs("auth.gateway_session_open")
		if got := ips[len(ips)-1]; got != "198.51.100.60" {
			t.Errorf("recorded from %s, want the gateway's statement 198.51.100.60", got)
		}
		var opened struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(answer), &opened); err != nil {
			t.Fatalf("decode %s: %v", answer, err)
		}
		folders := fmt.Sprintf("%s/api/v1/accounts/%d/folders", a.internal.URL, a.accountID)
		for name, cfg := range map[string]*tls.Config{
			"the POP3 gateway's certificate":  a.certificate(a.ca, pop3CN),
			"a certificate naming no gateway": a.certificate(a.ca, "someone-else"),
		} {
			if status, answer, _, err := ac03Request(t, cfg, http.MethodGet, folders, "Bearer "+opened.Data.Token, "", nil); err != nil || status != http.StatusUnauthorized {
				t.Errorf("the IMAP connection's token with %s: %d %s %v, want 401", name, status, answer, err)
			}
		}
		if status, answer, _, err := ac03Request(t, imap, http.MethodGet, folders, "Bearer "+opened.Data.Token, "", nil); err != nil || status != http.StatusOK {
			t.Errorf("the token with its own gateway's certificate: %d %s %v, want 200", status, answer, err)
		}
	})
}

// Criterion 3 (SRC-1957). Through one gateway's certificate, from one TCP
// address, every mail client address the gateway states has its own sign-in
// allowance: more clients than the burst all sign in, one busy client is
// refused past the burst without taking anything from the others. A caller
// that is not a gateway gains nothing by stating addresses: it is refused, and
// its own address runs out. The same with real mail clients on 20 source
// addresses is TestSignInsFromManyClientsDoNotShareAnAllowance, which needs
// all of 127/8 on loopback and runs on Linux CI.
func TestAC03EachClientAddressHasItsOwnAllowanceOverMTLS(t *testing.T) {
	throttled := config.Config{AuthRateLimitEnabled: true, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: signInBurst}
	for _, gw := range []struct{ cn, protocol string }{{imapCN, "imap"}, {pop3CN, "pop3"}, {submissionCN, "smtp"}} {
		t.Run(gw.protocol, func(t *testing.T) {
			a := newAC03Topology(t, throttled)
			client := a.gatewayClient(gw.cn)
			for i := range signInBurst + 5 {
				ip := fmt.Sprintf("198.51.100.%d", i+1)
				if _, err := client.OpenSession(a.address, a.app.Password, gw.protocol, ip); err != nil {
					t.Fatalf("client %s through the gateway: %v", ip, err)
				}
			}
			const busy = "198.51.100.200"
			for i := range signInBurst {
				if _, err := client.OpenSession(a.address, a.app.Password, gw.protocol, busy); err != nil {
					t.Fatalf("busy client's sign-in %d of the burst: %v", i+1, err)
				}
			}
			var refused *apiclient.APIError
			if _, err := client.OpenSession(a.address, a.app.Password, gw.protocol, busy); !errors.As(err, &refused) || refused.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("busy client past the burst: %v, want 429", err)
			}
			if _, err := client.OpenSession(a.address, a.app.Password, gw.protocol, "198.51.100.201"); err != nil {
				t.Fatalf("another client after the busy one ran out: %v", err)
			}
			if got := len(a.sessions()); got != 2*signInBurst+6 {
				t.Fatalf("%d sessions opened, want %d", got, 2*signInBurst+6)
			}
		})
	}

	t.Run("not a gateway", func(t *testing.T) {
		a := newAC03Topology(t, throttled)
		impostor := a.certificate(a.ca, "someone-else")
		for i := range signInBurst + 1 {
			ip := fmt.Sprintf("198.51.100.%d", i+1)
			status, answer, _, err := ac03Request(t, impostor, http.MethodPost, a.internal.URL+"/api/v1/gateway-sessions", "",
				openBodyFrom("imap", a.address, a.app.Password, ip), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := http.StatusForbidden
			if i == signInBurst {
				want = http.StatusTooManyRequests
			}
			if status != want {
				t.Fatalf("attempt %d stating %s: %d %s, want %d", i+1, ip, status, answer, want)
			}
		}
		if n := len(a.sessions()); n != 0 {
			t.Fatalf("%d sessions opened", n)
		}
	})
}

// Criterion 4 (SRC-1935, SRC-1586). HTTP Basic and the protocolSignIn route
// were removed and stay removed: on both real listeners the old route is not
// found, Basic opens nothing and is never asked for, the description names
// neither, and the replacement, openGatewaySession, signs the same app
// password in over mTLS.
func TestAC03RemovedSignInRoutesStayRemoved(t *testing.T) {
	a := newAC03Topology(t, config.Config{})
	imap := a.certificate(a.ca, imapCN)
	b := basic(a.address, a.app.Password)
	folders := fmt.Sprintf("/api/v1/accounts/%d/folders", a.accountID)

	for _, tc := range []struct {
		name, method, url, body string
		tls                     *tls.Config
		status                  int
		code                    string
	}{
		{"protocolSignIn on the public listener", http.MethodPost, a.public.URL + "/api/v1/auth/protocol-sign-in", `{"protocol":"imap"}`, nil, http.StatusNotFound, ""},
		{"protocolSignIn on the internal listener", http.MethodPost, a.internal.URL + "/api/v1/auth/protocol-sign-in", `{"protocol":"imap"}`, imap, http.StatusNotFound, ""},
		{"Basic on the public listener", http.MethodGet, a.public.URL + folders, "", nil, http.StatusUnauthorized, "unauthorized"},
		// The internal listener serves a mail route only to a gateway session.
		{"Basic on the internal listener", http.MethodGet, a.internal.URL + folders, "", imap, http.StatusNotFound, ""},
		{"Basic to open a session", http.MethodPost, a.internal.URL + "/api/v1/gateway-sessions", `{"protocol":"imap","client_ip":"198.51.100.9"}`, imap, http.StatusBadRequest, "credential_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, answer, header, err := ac03Request(t, tc.tls, tc.method, tc.url, b, tc.body, nil)
			if err != nil {
				t.Fatal(err)
			}
			if status != tc.status || !strings.Contains(answer, tc.code) {
				t.Fatalf("got %d %s, want %d %s", status, answer, tc.status, tc.code)
			}
			if challenge := header.Values("WWW-Authenticate"); len(challenge) != 0 {
				t.Errorf("the answer asks for %v", challenge)
			}
		})
	}
	if n := len(a.sessions()); n != 0 {
		t.Fatalf("%d sessions opened with HTTP Basic", n)
	}

	t.Run("the description names neither", func(t *testing.T) {
		served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Paths      map[string]map[string]any `yaml:"paths"`
			Components struct {
				SecuritySchemes map[string]struct {
					Type   string `yaml:"type"`
					Scheme string `yaml:"scheme"`
				} `yaml:"securitySchemes"`
			} `yaml:"components"`
		}
		if err := yaml.Unmarshal(served, &doc); err != nil {
			t.Fatal(err)
		}
		if _, ok := doc.Paths["/api/v1/auth/protocol-sign-in"]; ok {
			t.Error("the description has /api/v1/auth/protocol-sign-in")
		}
		if strings.Contains(string(served), "protocolSignIn") {
			t.Error("the description names protocolSignIn")
		}
		for name, scheme := range doc.Components.SecuritySchemes {
			if strings.EqualFold(scheme.Scheme, "basic") {
				t.Errorf("security scheme %s is HTTP Basic", name)
			}
		}
		open, ok := doc.Paths["/api/v1/gateway-sessions"]["post"].(map[any]any)
		if !ok || open["operationId"] != "openGatewaySession" {
			t.Fatal("the description has no openGatewaySession")
		}
		if security, ok := open["security"].([]any); !ok || len(security) != 0 {
			t.Errorf("openGatewaySession security %v, want [] (the gateway's certificate and the body's credential)", open["security"])
		}
		pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := pairs["protocolSignIn"]; ok {
			t.Error("the examples have protocolSignIn pairs")
		}
		if len(pairs["openGatewaySession"]) == 0 {
			t.Error("the examples have no openGatewaySession pairs")
		}
	})

	t.Run("the replacement signs the same app password in", func(t *testing.T) {
		opened, err := a.gatewayClient(imapCN).OpenSession(a.address, a.app.Password, "imap", "198.51.100.9")
		if err != nil {
			t.Fatalf("openGatewaySession: %v", err)
		}
		if status, answer, _, err := ac03Request(t, imap, http.MethodGet, a.internal.URL+folders, "Bearer "+opened.Token, "", nil); err != nil || status != http.StatusOK {
			t.Fatalf("folders in the session: %d %s %v", status, answer, err)
		}
	})
}
