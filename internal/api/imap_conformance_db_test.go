package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil"
)

// These #2100 / former #2106 proofs deliberately use Server.Serve and TCP,
// rather than net.Pipe or canned backend replies. As in cmd/imap-gateway, the
// file certificate loader, ServerTLSConfig, Backend and Server share one
// limiter. gatewayAPI supplies the production public/internal routers and the
// fixture's IMAP machine identity; authentication and storage use PostgreSQL.
// A cleartext listener is opened only for the conditional STARTTLS proof:
// production's supported configuration exposes implicit TLS only.
type imapConformanceGateway struct {
	*gatewayFixture
	backend *imapgw.Backend
	limiter *connlimiter.Limiter
	tls     *tls.Config
	roots   *x509.CertPool
	address string
	clear   string
	v6      string
	public  string
	config  config.Config
}

func newIMAPConformanceGateway(t *testing.T, startTLS, ipv6 bool) *imapConformanceGateway {
	t.Helper()
	g := newGatewayFixture(t)
	prepareIMAPImport(t, g.credentialFixture)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(2100), Subject: pkix.Name{CommonName: "imap.test"},
		DNSNames: []string{"imap.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for path, block := range map[string]*pem.Block{
		certPath: {Type: "CERTIFICATE", Bytes: der}, keyPath: {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	loader, err := tlsutil.NewFileCertLoader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 20, MaxGlobal: 1000})
	backend := imapgw.NewBackend(gatewayAPI(t, g.router, g.internal, imapCN), limiter)
	cfg := imapgw.ServerTLSConfig(loader.GetCertificate)
	server := imapserver.New("imap.test", backend, cfg, limiter)
	// Close active test clients before closing the server, and the server before
	// closing its HTTP dependencies (cleanup is LIFO).
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			t.Errorf("drain IMAP sessions: %v", err)
		}
	})
	listen := func(network, address string) net.Listener {
		t.Helper()
		listener, err := net.Listen(network, address)
		if err != nil {
			t.Fatal(err)
		}
		return listener
	}
	implicit := listen("tcp4", "127.0.0.1:0")
	listeners := imapserver.Listeners{IMAPTLS: implicit}
	f := &imapConformanceGateway{gatewayFixture: g, backend: backend, limiter: limiter, tls: cfg, roots: roots, address: implicit.Addr().String(),
		config: config.Config{Environment: "production", TLSCertPath: certPath, TLSKeyPath: keyPath, IMAPTLSPort: implicit.Addr().(*net.TCPAddr).Port}}
	if startTLS {
		listeners.IMAP = listen("tcp4", "127.0.0.1:0")
		f.clear = listeners.IMAP.Addr().String()
	}
	server.Serve(listeners)
	if ipv6 {
		other := listen("tcp6", "[::1]:0")
		f.v6 = other.Addr().String()
		server.Serve(imapserver.Listeners{IMAPTLS: other})
	}
	public := httptest.NewServer(g.router)
	// The mail server must finish closing API sessions before HTTP disappears.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			t.Errorf("drain IMAP sessions: %v", err)
		}
		public.Close()
	})
	f.public = public.URL
	return f
}

func (g *imapConformanceGateway) clientTLS() *tls.Config {
	return &tls.Config{ServerName: "imap.test", RootCAs: g.roots, MinVersion: tls.VersionTLS12}
}

func (g *imapConformanceGateway) connect(t *testing.T, address string, encrypted bool) *imapZoneSession {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted {
		conn = tls.Client(conn, g.clientTLS())
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &imapZoneSession{t: t, conn: conn, reader: bufio.NewReader(conn)}
}

func (g *imapConformanceGateway) login(t *testing.T, address, user, password string) *imapZoneSession {
	t.Helper()
	s := g.connect(t, address, true)
	if greeting := s.line(); !strings.HasPrefix(greeting, "* OK") {
		t.Fatalf("greeting: %q", greeting)
	}
	s.commandOK(fmt.Sprintf("LOGIN %q %q", user, password))
	return s
}

func (g *imapConformanceGateway) ownLogin(t *testing.T) *imapZoneSession {
	t.Helper()
	return g.login(t, g.address, g.gatewayFixture.address, g.app.Password)
}

func requireIMAPStatus(t *testing.T, s *imapZoneSession, command, status string) []string {
	t.Helper()
	lines, completion := s.command(command)
	if !strings.HasPrefix(completion, fmt.Sprintf("z%d %s ", s.tag, status)) {
		t.Fatalf("%s: %q / %q; want tagged %s", command, lines, completion, status)
	}
	return lines
}

func requireIMAPExact(t *testing.T, got []string, want ...string) {
	t.Helper()
	got, want = slices.Clone(got), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("wire responses = %q, want %q", got, want)
	}
}

func (g *imapConformanceGateway) otherAccount(t *testing.T, local string) (string, string, uint) {
	t.Helper()
	id := g.seedMailbox(g.mustDomainID(), local)
	_, password, err := repositories.NewCredentialRepository(g.db).CreateAppPassword(id, id, "wire proof", nil)
	if err != nil {
		t.Fatal(err)
	}
	return local + "@example.test", password, id
}

// RFC 9051 §11.1/11.2, CHECKLIST IMAP2-SEC-233/234/238. Configuration
// assertions distinguish the TLS floor from the AEAD-only suite policy, since
// either alone already prevents a TLS 1.1 handshake.
func TestIMAPGatewayTLSListenerPolicy(t *testing.T) {
	g := newIMAPConformanceGateway(t, false, false)
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("IMAP_PORT", "0")
	if err := g.config.ValidateListenerSecurity(config.RoleIMAPGateway); err != nil {
		t.Fatal(err)
	}
	missing := g.config
	missing.TLSKeyPath = ""
	if err := missing.ValidateListenerSecurity(config.RoleIMAPGateway); err == nil || !strings.Contains(err.Error(), "TLS keypair") {
		t.Fatalf("production without key: %v", err)
	}
	t.Setenv("IMAP_PORT", "143")
	if err := g.config.ValidateListenerSecurity(config.RoleIMAPGateway); err == nil || !strings.Contains(err.Error(), "IMAP_PORT") {
		t.Fatalf("removed production cleartext port: %v", err)
	}
	if g.tls.MinVersion != tls.VersionTLS12 || g.tls.MaxVersion != 0 {
		t.Fatalf("TLS bounds = %x..%x, want TLS1.2 with no upper pin", g.tls.MinVersion, g.tls.MaxVersion)
	}
	if !slices.Contains(g.tls.CipherSuites, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256) {
		t.Fatal("mandated RSA AES128 GCM suite missing")
	}
	for _, id := range g.tls.CipherSuites {
		name := tls.CipherSuiteName(id)
		if !strings.HasPrefix(name, "TLS_ECDHE_") || !(strings.Contains(name, "_GCM_") || strings.Contains(name, "CHACHA20_POLY1305")) {
			t.Fatalf("non-ECDHE AEAD suite: %s", name)
		}
	}
	for _, test := range []struct {
		name    string
		version uint16
		suite   uint16
		accept  bool
	}{
		{"TLS10", tls.VersionTLS10, tls.TLS_RSA_WITH_AES_128_CBC_SHA, false},
		{"TLS11", tls.VersionTLS11, tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, false},
		{"static_RSA", tls.VersionTLS12, tls.TLS_RSA_WITH_AES_128_GCM_SHA256, false},
		{"CBC", tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, false},
		{"mandated_TLS12", tls.VersionTLS12, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, true},
		{"TLS13", tls.VersionTLS13, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", g.address, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			cfg := g.clientTLS()
			cfg.MinVersion, cfg.MaxVersion = test.version, test.version
			if test.suite != 0 {
				cfg.CipherSuites = []uint16{test.suite}
			}
			client := tls.Client(conn, cfg)
			err = client.Handshake()
			if !test.accept {
				if err == nil {
					t.Fatalf("forbidden handshake accepted: %+v", client.ConnectionState())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			state := client.ConnectionState()
			if state.Version != test.version || (test.suite != 0 && state.CipherSuite != test.suite) {
				t.Fatalf("unexpected negotiated TLS: %+v", state)
			}
			s := &imapZoneSession{t: t, conn: client, reader: bufio.NewReader(client)}
			if line := s.line(); !strings.HasPrefix(line, "* OK") {
				t.Fatalf("greeting: %q", line)
			}
			s.commandOK(fmt.Sprintf("LOGIN %q %q", g.gatewayFixture.address, g.app.Password))
			s.commandOK("LOGOUT")
		})
	}
	// A plaintext client never gets a plaintext greeting or authenticates on
	// the implicit listener; it gets a TLS alert/closure instead.
	plain := g.connect(t, g.address, false)
	plain.write("p1 LOGIN ignored ignored\r\n")
	_ = plain.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	bytes, _ := io.ReadAll(plain.conn)
	if strings.Contains(string(bytes), "* OK") || strings.Contains(string(bytes), "p1 OK") {
		t.Fatalf("plaintext response on implicit TLS: %q", bytes)
	}
	other, password, _ := g.otherAccount(t, "tlscontrol")
	g.login(t, g.address, other, password).commandOK("LOGOUT")
}

// Conditional cleartext listener proof for IMAP2-SEC-238 and the RFC 3501
// STARTTLS/LOGINDISABLED rows. This does not add a production port 143.
func TestIMAPGatewaySTARTTLSAuthentication(t *testing.T) {
	g := newIMAPConformanceGateway(t, true, false)
	s := g.connect(t, g.clear, false)
	if line := s.line(); !strings.HasPrefix(line, "* OK") {
		t.Fatal(line)
	}
	before := s.commandOK("CAPABILITY")
	for _, token := range []string{"STARTTLS", "LOGINDISABLED"} {
		if !slices.Contains(strings.Fields(strings.Join(before, " ")), token) {
			t.Fatalf("missing %s: %q", token, before)
		}
	}
	if strings.Contains(strings.Join(before, " "), "AUTH=PLAIN") {
		t.Fatal("PLAIN advertised in cleartext")
	}
	for _, command := range []string{fmt.Sprintf("LOGIN %q %q", g.gatewayFixture.address, g.app.Password), "AUTHENTICATE PLAIN"} {
		_, status := s.command(command)
		if !strings.Contains(status, " NO [PRIVACYREQUIRED]") {
			t.Fatalf("pre-TLS authentication: %q", status)
		}
	}
	var count int64
	if err := g.db.Model(&models.GatewaySession{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("pre-TLS stored sessions: %d, %v", count, err)
	}
	s.commandOK("STARTTLS")
	secure := tls.Client(s.conn, g.clientTLS())
	if err := secure.Handshake(); err != nil {
		t.Fatal(err)
	}
	s.conn, s.reader = secure, bufio.NewReader(secure)
	after := strings.Fields(strings.Join(s.commandOK("CAPABILITY"), " "))
	if slices.Contains(after, "STARTTLS") || slices.Contains(after, "LOGINDISABLED") || !slices.Contains(after, "AUTH=PLAIN") {
		t.Fatalf("post-TLS capabilities: %q", after)
	}
	requireIMAPStatus(t, s, "STARTTLS", "BAD")
	s.commandOK(fmt.Sprintf("LOGIN %q %q", g.gatewayFixture.address, g.app.Password))
	s.commandOK("SELECT INBOX")
}

// Approved #1672 policy applies where a person chooses a password. IMAP does
// not set passwords; the public production HTTP listener refuses weak choices,
// then the actual mail listener proves credentials and unrelated accounts work.
func TestIMAPGatewayWeakPasswordRejection(t *testing.T) {
	g := newIMAPConformanceGateway(t, false, false)
	original := g.passwordHash()
	other, otherPassword, _ := g.otherAccount(t, "passwordcontrol")
	call := func(path, session, body string, want int) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, g.public+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s: %d %s (%v), want %d", path, resp.StatusCode, data, err, want)
		}
		return string(data)
	}
	token, _, err := bootstrap.MintResetToken(g.db, g.gatewayFixture.address, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, weak := range []string{"short", "Summer2026!!", "password123456", "Tr1cky-Orbit-Lantern-chris"} {
		for _, test := range []struct{ path, session, body string }{
			{"/api/v1/auth/password", g.session, fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, g.password, weak)},
			{"/api/v1/auth/reset", "", fmt.Sprintf(`{"token":%q,"password":%q}`, token, weak)},
		} {
			body := call(test.path, test.session, test.body, http.StatusUnprocessableEntity)
			if !strings.Contains(body, "validation_failed") || strings.Contains(body, weak) {
				t.Fatalf("weak password diagnostic: %s", body)
			}
		}
		if got := g.passwordHash(); got != original {
			t.Fatal("refused choice replaced the stored password")
		}
	}
	var unspent int64
	if err := g.db.Model(&models.OneTimeToken{}).Where("account_id = ? AND used_at IS NULL", g.accountID).Count(&unspent).Error; err != nil || unspent != 1 {
		t.Fatalf("weak resets spent their token: %d unspent (%v)", unspent, err)
	}
	g.ownLogin(t).commandOK("LOGOUT")
	g.login(t, g.address, other, otherPassword).commandOK("LOGOUT")
	// A good replacement is accepted on the same change path. A password
	// change revokes app passwords, so check native login, not an old app secret.
	const strong = "Copper-Kestrel-Tundra-741"
	call("/api/v1/auth/password", g.session, fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, g.password, strong), http.StatusOK)
	call("/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, g.gatewayFixture.address, strong), http.StatusOK)
	call("/api/v1/auth/reset", "", fmt.Sprintf(`{"token":%q,"password":%q,"client":"native"}`, token, "Violet-Walrus-Orbit-827"), http.StatusOK)
	g.login(t, g.address, other, otherPassword).commandOK("LOGOUT")
}

// IMAP2-SEC-247: real wrong-password rejections reach both default limiters.
// IPv4 and IPv6 loopbacks supply independent addresses without relying on
// macOS having Linux's entire 127/8 bound to lo0.
func TestIMAPGatewayPasswordSpraying(t *testing.T) {
	t.Run("one_address_many_accounts", func(t *testing.T) {
		g := newIMAPConformanceGateway(t, false, true)
		g.ownLogin(t).commandOK("LOGOUT")
		held := g.connect(t, g.address, true)
		held.line()
		s := g.connect(t, g.address, true)
		s.line()
		for i := 1; i <= 5; i++ {
			user, _, _ := g.otherAccount(t, fmt.Sprintf("spray%d", i))
			status := "NO [AUTHENTICATIONFAILED]"
			if i == 5 {
				status = "NO [UNAVAILABLE]"
			}
			lines := requireIMAPStatus(t, s, fmt.Sprintf("LOGIN %q wrong-password", user), status)
			if g.limiter.IsUserBanned(user) {
				t.Fatalf("one guess locked %s", user)
			}
			if i < 5 {
				requireIMAPExact(t, lines)
			} else {
				requireIMAPExact(t, lines, "* BYE Too many authentication failures, try again later")
			}
		}
		if !g.limiter.IsBanned("127.0.0.1") {
			t.Fatal("spraying address was not banned at default five failures")
		}
		// Correct credentials on a pre-opened connection cannot clear the ban.
		lines := requireIMAPStatus(t, held, fmt.Sprintf("LOGIN %q %q", g.gatewayFixture.address, g.app.Password), "NO [UNAVAILABLE]")
		requireIMAPExact(t, lines, "* BYE Too many authentication failures, try again later")
		reconnected := g.connect(t, g.address, true)
		if line := reconnected.line(); line != "* BYE Too many authentication failures, try again later" {
			t.Fatalf("banned reconnect: %q", line)
		}
		_ = reconnected.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if line, err := reconnected.reader.ReadString('\n'); err == nil {
			t.Fatalf("banned reconnect stayed open: %q", line)
		}
		var sessions int64
		if err := g.db.Model(&models.GatewaySession{}).Count(&sessions).Error; err != nil || sessions != 1 {
			t.Fatalf("banned credentials opened a session: %d (%v)", sessions, err)
		}
		other, password, _ := g.otherAccount(t, "unrelated")
		g.login(t, g.v6, other, password).commandOK("LOGOUT")
		g.login(t, g.v6, g.gatewayFixture.address, g.app.Password).commandOK("LOGOUT")
	})
	t.Run("one_account_two_addresses", func(t *testing.T) {
		g := newIMAPConformanceGateway(t, false, true)
		for i := range 5 {
			address := g.address
			if i%2 == 1 {
				address = g.v6
			}
			s := g.connect(t, address, true)
			s.line()
			requireIMAPStatus(t, s, fmt.Sprintf("LOGIN %q wrong-password", g.gatewayFixture.address), "NO")
			s.commandOK("LOGOUT")
		}
		if !g.limiter.IsUserBanned(g.gatewayFixture.address) || g.limiter.IsBanned("127.0.0.1") || g.limiter.IsBanned("::1") {
			t.Fatal("per-account and per-address failure accounting are not independent")
		}
		s := g.connect(t, g.v6, true)
		s.line()
		requireIMAPStatus(t, s, fmt.Sprintf("LOGIN %q %q", g.gatewayFixture.address, g.app.Password), "NO")
		other, password, _ := g.otherAccount(t, "unrelated")
		g.login(t, g.v6, other, password).commandOK("LOGOUT")
	})
}

// #718: this is a rev1 transcript plus a rev2 client's attempted negotiation,
// not a claim that a rev2 session exists. Its switch/advertisement decision is
// still Leaning in the owner record. Existing RFC 5161 extension ENABLE stays.
func TestIMAPSessionCapabilityEnablePolicy(t *testing.T) {
	g := newIMAPConformanceGateway(t, false, false)
	s := g.ownLogin(t)
	before := s.commandOK("CAPABILITY")
	tokens := strings.Fields(strings.Join(before, " "))
	for _, token := range []string{"IMAP4rev1", "ENABLE", "UTF8=ACCEPT", "CONDSTORE", "QRESYNC", "LIST-EXTENDED", "LIST-STATUS", "SASL-IR", "NAMESPACE", "ESEARCH", "SEARCHRES", "UIDPLUS"} {
		if !slices.Contains(tokens, token) {
			t.Fatalf("missing capability %s: %q", token, before)
		}
	}
	if slices.Contains(tokens, "IMAP4rev2") {
		t.Fatal("undecided IMAP4rev2 advertised")
	}
	requireIMAPExact(t, s.commandOK("ENABLE IMAP4rev2 UNKNOWN"), "* ENABLED")
	requireIMAPExact(t, s.commandOK("ENABLE UTF8=ACCEPT CONDSTORE"), "* ENABLED UTF8=ACCEPT CONDSTORE")
	requireIMAPExact(t, s.commandOK("ENABLE QRESYNC"), "* ENABLED QRESYNC")
	requireIMAPExact(t, s.commandOK("ENABLE UTF8=ACCEPT CONDSTORE QRESYNC"), "* ENABLED")
	requireIMAPExact(t, s.commandOK("CAPABILITY"), before...)
	lines := s.commandOK("SELECT INBOX")
	requireIMAPLines(t, lines, "* 0 RECENT")
	s.commandOK("CHECK")
	requireIMAPExact(t, s.commandOK("SEARCH ALL"), "* SEARCH")
	s.commandOK(`LSUB "" "*"`)
	// Unknown ENABLE did not silently change rev1's removed commands/responses.
}

func appendConformanceMessage(t *testing.T, s *imapZoneSession, folder, flags, id string) []string {
	t.Helper()
	message := "From: a@example.test\r\nTo: b@example.test\r\nSubject: conformance\r\nMessage-ID: <" + id + "@example.test>\r\n\r\nbody\r\n"
	tag := s.nextTag()
	if flags != "" {
		flags += " "
	}
	s.write(fmt.Sprintf("%s APPEND %q %s{%d}\r\n", tag, folder, flags, len(message)))
	if line := s.line(); !strings.HasPrefix(line, "+") {
		t.Fatalf("APPEND continuation: %q", line)
	}
	s.write(message + "\r\n")
	lines, status := s.responses(tag)
	if !strings.HasPrefix(status, tag+" OK ") {
		t.Fatalf("APPEND: %q / %q", lines, status)
	}
	return lines
}

// #1669: every OLDNAME site reaches production persistence. The rev1 spelling
// and negotiated UTF-8 spelling have independent transcripts and exact output.
func TestIMAPMailboxNFCAndOLDNAME(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		t.Run(fmt.Sprintf("UTF8=%t", utf8), func(t *testing.T) {
			g := newIMAPConformanceGateway(t, false, false)
			s := g.ownLogin(t)
			nfc, nfd := "Caf&AOk-", "Cafe&AwE-"
			if utf8 {
				s.commandOK("ENABLE UTF8=ACCEPT")
				nfc, nfd = "Café", "Cafe\u0301"
			}
			wireName := func(name string) string {
				if strings.ContainsAny(name, "é\u0301") {
					return fmt.Sprintf("{%d}\r\n%s", len(name), name)
				}
				return fmt.Sprintf("%q", name)
			}
			oldname := func(name, old string, deleted bool) []string {
				attrs := ""
				if deleted {
					attrs = `\NonExistent`
				}
				return strings.Split(fmt.Sprintf(`* LIST (%s) "/" %s ("OLDNAME" (%s))`, attrs, wireName(name), wireName(old)), "\r\n")
			}
			requireIMAPExact(t, s.commandOK(fmt.Sprintf("CREATE %q", nfd)), oldname(nfc, nfd, false)...)
			for _, name := range []string{nfd, nfc} {
				requireIMAPStatus(t, s, fmt.Sprintf("CREATE %q", name), "NO")
			}
			requireIMAPExact(t, appendConformanceMessage(t, s, nfd, "", "nfc"), oldname(nfc, nfd, false)...)
			for _, command := range []string{"SELECT", "EXAMINE"} {
				lines := s.commandOK(fmt.Sprintf("%s %q", command, nfd))
				requireIMAPLines(t, lines, append(oldname(nfc, nfd, false), "* 1 EXISTS")...)
			}
			requireIMAPExact(t, s.commandOK(fmt.Sprintf("STATUS %q (MESSAGES)", nfd)), strings.Split(fmt.Sprintf(`* STATUS %s (MESSAGES 1)`, wireName(nfc)), "\r\n")...)
			requireIMAPExact(t, s.commandOK(fmt.Sprintf(`LIST "" %q`, nfd)), strings.Split(fmt.Sprintf(`* LIST () "/" %s`, wireName(nfc)), "\r\n")...)
			s.commandOK("UNSELECT")
			requireIMAPExact(t, s.commandOK(fmt.Sprintf("RENAME %q Plain", nfd)), oldname("Plain", nfd, false)...)
			requireIMAPExact(t, s.commandOK(fmt.Sprintf("RENAME Plain %q", nfd)), oldname(nfc, "Plain", false)...)
			var stored []string
			if err := g.db.Model(&models.Message{}).Where("mailbox_id = ?", g.mailboxID).Distinct().Pluck("folder", &stored).Error; err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(stored, []string{"Café"}) {
				t.Fatalf("stored folder names: %q", stored)
			}
			requireIMAPExact(t, s.commandOK(fmt.Sprintf("SELECT %q", nfc))[:1], "* 1 EXISTS")
			s.commandOK("UNSELECT")
			requireIMAPExact(t, s.commandOK(fmt.Sprintf("DELETE %q", nfd)), oldname(nfc, nfd, true)...)
			requireIMAPExact(t, s.commandOK(fmt.Sprintf(`LIST "" %q`, nfc)))
		})
	}
}

// RFC 3501 SUBSCRIBE/LSUB, RFC 5258 and RFC 5819, against stored choices.
func TestIMAPSubscriptionsAndExtensions(t *testing.T) {
	g := newIMAPConformanceGateway(t, false, false)
	s := g.ownLogin(t)
	for _, folder := range []string{"Fruit", "Fruit/Apple", "Fruit/Gone"} {
		s.commandOK(fmt.Sprintf("CREATE %q", folder))
	}
	for _, folder := range []string{"Fruit/Apple", "Fruit/Gone", "Fruit/Missing"} {
		s.commandOK(fmt.Sprintf("SUBSCRIBE %q", folder))
		s.commandOK(fmt.Sprintf("SUBSCRIBE %q", folder))
	}
	s.commandOK("UNSUBSCRIBE Spam")
	s.commandOK("UNSUBSCRIBE Spam")
	s.commandOK(`RENAME "Fruit/Apple" "Fruit/Renamed"`)
	s.commandOK(`DELETE "Fruit/Gone"`)
	s.commandOK("LOGOUT")
	s = g.ownLogin(t)
	want := []string{`* LIST (\Subscribed \NonExistent) "/" "Fruit/Apple"`, `* LIST (\Subscribed \NonExistent) "/" "Fruit/Gone"`, `* LIST (\Subscribed \NonExistent) "/" "Fruit/Missing"`}
	requireIMAPExact(t, s.commandOK(`LIST (SUBSCRIBED) "" "Fruit/*"`), want...)
	requireIMAPExact(t, s.commandOK(`LIST (REMOTE SUBSCRIBED) "" "Fruit/*"`), want...)
	requireIMAPExact(t, s.commandOK(`LSUB "" "Fruit/*"`), `* LSUB () "/" "Fruit/Apple"`, `* LSUB () "/" "Fruit/Gone"`, `* LSUB () "/" "Fruit/Missing"`)
	requireIMAPExact(t, s.commandOK(`LIST (SUBSCRIBED RECURSIVEMATCH) "" "Fruit" RETURN (CHILDREN)`), `* LIST (\HasChildren) "/" "Fruit" ("CHILDINFO" ("SUBSCRIBED"))`)
	requireIMAPExact(t, s.commandOK(`LIST "" ("Fruit/R*" "Fruit/G*") RETURN (CHILDREN STATUS (MESSAGES))`), `* LIST (\HasNoChildren) "/" "Fruit/Renamed"`, `* STATUS "Fruit/Renamed" (MESSAGES 0)`)
	requireIMAPExact(t, s.commandOK(`LIST (SUBSCRIBED) "" "Spam"`))
	s.commandOK(`UNSUBSCRIBE "Fruit/Missing"`)
	var rows []models.FolderSubscription
	if err := g.db.Where("mailbox_id = ?", g.mailboxID).Order("folder").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Folder != "Fruit/Apple" || !rows[0].Subscribed || rows[1].Folder != "Fruit/Gone" || !rows[1].Subscribed || rows[2].Folder != "Spam" || rows[2].Subscribed {
		t.Fatalf("persisted subscriptions: %+v", rows)
	}
	other, password, _ := g.otherAccount(t, "subscriptioncontrol")
	control := g.login(t, g.address, other, password)
	requireIMAPExact(t, control.commandOK(`LIST (SUBSCRIBED) "" "Fruit/*"`))
	// The default subscription also exists before an empty Spam folder does.
	requireIMAPLines(t, control.commandOK(`LIST (SUBSCRIBED) "" "Spam"`), `* LIST (\Subscribed \NonExistent) "/" "Spam"`)
}

// #1853: preserve today's strict decoder in rev1 as well. This does not
// settle the open rev2 compatibility switch, nor treat valid malformed PLAIN
// as an encoding error. Every rejection leaves the session unauthenticated.
func TestIMAPAuthenticateMalformedBase64(t *testing.T) {
	g := newIMAPConformanceGateway(t, false, false)
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial=%t", initial), func(t *testing.T) {
			s := g.connect(t, g.address, true)
			s.line()
			for _, test := range []struct{ payload, status string }{
				{"%%%", "BAD"}, {"A=AA", "BAD"}, {"YQ===", "BAD"}, {"*", "BAD"},
				{base64.StdEncoding.EncodeToString([]byte("not PLAIN")), "NO"},
				{base64.StdEncoding.EncodeToString([]byte("\x00\x00password")), "NO"},
			} {
				tag := s.nextTag()
				if initial {
					s.write(tag + " AUTHENTICATE PLAIN " + test.payload + "\r\n")
				} else {
					s.write(tag + " AUTHENTICATE PLAIN\r\n")
					if line := s.line(); line != "+ " {
						t.Fatalf("continuation: %q", line)
					}
					s.write(test.payload + "\r\n")
				}
				lines, status := s.responses(tag)
				if len(lines) != 0 || !strings.HasPrefix(status, tag+" "+test.status+" ") {
					t.Fatalf("payload %q: %q / %q, want %s", test.payload, lines, status, test.status)
				}
				requireIMAPStatus(t, s, "SELECT INBOX", "NO")
			}
			var sessions int64
			if err := g.db.Model(&models.GatewaySession{}).Where("closed_at IS NULL").Count(&sessions).Error; err != nil || sessions != 0 {
				t.Fatalf("malformed AUTH created sessions: %d, %v", sessions, err)
			}
			payload := base64.StdEncoding.EncodeToString([]byte("\x00" + g.gatewayFixture.address + "\x00" + g.app.Password))
			s.commandOK("AUTHENTICATE PLAIN " + payload)
			s.commandOK("SELECT INBOX")
			s.commandOK("LOGOUT")
		})
	}
}

// #1860, RFC 3501 §2.3.2: seen/unseen and recent are different dimensions.
// Shared-process claim isolation is proven; restart replay is pinned explicitly
// while the owner decision about durable or multi-replica marks remains open.
func TestIMAPRecentConsumptionAndIsolation(t *testing.T) {
	g := newIMAPConformanceGateway(t, false, false)
	other, password, id := g.otherAccount(t, "recentcontrol")
	// Give the other account and folder arrivals lower UIDs than INBOX's later
	// claim. A map missing either key dimension would wrongly consume them.
	otherFixture := *g.credentialFixture
	otherFixture.mailboxID, otherFixture.accountID, otherFixture.address = id, id, other
	prepareIMAPImport(t, &otherFixture)
	control := g.login(t, g.address, other, password)
	appendConformanceMessage(t, control, "INBOX", "", "other")
	writer := g.ownLogin(t)
	writer.commandOK("CREATE Work")
	appendConformanceMessage(t, writer, "Work", "", "work")
	appendConformanceMessage(t, writer, "INBOX", `(\Seen)`, "seen")
	appendConformanceMessage(t, writer, "INBOX", "", "unseen")
	reader := g.ownLogin(t)
	for range 2 {
		requireIMAPExact(t, reader.commandOK("STATUS INBOX (RECENT UNSEEN)"), `* STATUS "INBOX" (RECENT 2 UNSEEN 1)`)
	}
	requireIMAPLines(t, reader.commandOK("EXAMINE INBOX"), "* 2 RECENT")
	reader.requireSearch("RECENT", "1 2")
	reader.requireSearch("NEW", "2")
	reader.requireSearch("OLD", "")
	first := g.ownLogin(t)
	requireIMAPLines(t, first.commandOK("SELECT INBOX"), "* 2 RECENT")
	first.requireSearch("RECENT", "1 2")
	later := g.ownLogin(t)
	requireIMAPLines(t, later.commandOK("SELECT INBOX"), "* 0 RECENT")
	later.requireSearch("RECENT", "")
	later.requireSearch("NEW", "")
	later.requireSearch("OLD", "1 2")
	later.requireSearch("UNSEEN", "2")
	// CREATE Work stores an empty-folder marker as a message row, so get the
	// assigned UIDs from PostgreSQL rather than assuming globally fixed IDs.
	var uids []uint
	if err := g.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND is_deleted = false", g.mailboxID, "INBOX").Order("id").Pluck("id", &uids).Error; err != nil || len(uids) != 2 {
		t.Fatalf("INBOX UIDs: %v (%v)", uids, err)
	}
	requireIMAPLines(t, first.commandOK("FETCH 1:2 FLAGS"), fmt.Sprintf(`* 1 FETCH (UID %d FLAGS (\Seen \Recent))`, uids[0]), fmt.Sprintf(`* 2 FETCH (UID %d FLAGS (\Recent))`, uids[1]))
	requireIMAPLines(t, later.commandOK("FETCH 1:2 FLAGS"), fmt.Sprintf(`* 1 FETCH (UID %d FLAGS (\Seen))`, uids[0]), fmt.Sprintf(`* 2 FETCH (UID %d FLAGS ())`, uids[1]))
	appendConformanceMessage(t, writer, "INBOX", "", "arrival")
	requireIMAPLines(t, first.commandOK("NOOP"), "* 3 EXISTS", "* 3 RECENT")
	later.commandOK("NOOP")
	first.requireSearch("RECENT", "1 2 3")
	later.requireSearch("RECENT", "")
	requireIMAPExact(t, writer.commandOK("STATUS INBOX (RECENT UNSEEN)"), `* STATUS "INBOX" (RECENT 0 UNSEEN 2)`)
	requireIMAPLines(t, reader.commandOK("EXAMINE INBOX"), "* 0 RECENT")
	requireIMAPLines(t, later.commandOK("SELECT Work"), "* 1 RECENT")
	requireIMAPLines(t, control.commandOK("SELECT INBOX"), "* 1 RECENT")
	// A second gateway backend has no shared mark: this is the exact current
	// limitation, not an accepted durability policy.
	api := gatewayAPI(t, g.router, g.internal, imapCN)
	server := imapserver.New("imap.test", imapgw.NewBackend(api, g.limiter), g.tls, g.limiter)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Serve(imapserver.Listeners{IMAPTLS: listener})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			t.Errorf("drain restarted gateway: %v", err)
		}
	})
	restarted := g.login(t, listener.Addr().String(), g.gatewayFixture.address, g.app.Password)
	requireIMAPLines(t, restarted.commandOK("SELECT INBOX"), "* 3 RECENT")
}
