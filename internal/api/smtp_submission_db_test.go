package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"golang.org/x/net/dns/dnsmessage"
	"gorm.io/gorm"
)

// dnsZone is what serveZone answers from, with lower-case names without the
// trailing dot: TXT records, the one MX host of a domain, and a host's IPv4 address.
type dnsZone struct {
	txt map[string][]string
	mx  map[string]string
	a   map[string]string
}

// serveTXT answers DNS queries on the loopback from records (lower-case names
// without the trailing dot), and points this process's resolver at it until
// the test ends. A name it has no records for does not exist.
func serveTXT(t *testing.T, records map[string][]string) {
	t.Helper()
	serveZone(t, dnsZone{txt: records})
}

// serveZone answers DNS queries on the loopback from zone, and points this
// process's resolver at it until the test ends. A name zone has no records for
// does not exist.
func serveZone(t *testing.T, zone dnsZone) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for DNS: %v", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if answer, ok := answerZone(buf[:n], zone); ok {
				_, _ = pc.WriteTo(answer, from)
			}
		}
	}()

	resolver := net.DefaultResolver
	preferGo, dial := resolver.PreferGo, resolver.Dial
	resolver.PreferGo = true
	resolver.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", pc.LocalAddr().String())
	}
	t.Cleanup(func() {
		resolver.PreferGo, resolver.Dial = preferGo, dial
		_ = pc.Close()
	})
}

func answerZone(query []byte, zone dnsZone) ([]byte, bool) {
	var p dnsmessage.Parser
	header, err := p.Start(query)
	if err != nil {
		return nil, false
	}
	q, err := p.Question()
	if err != nil {
		return nil, false
	}
	name := strings.TrimSuffix(strings.ToLower(q.Name.String()), ".")
	txts, hasTXT := zone.txt[name]
	mx, hasMX := zone.mx[name]
	address, hasA := zone.a[name]
	reply := dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true, RecursionDesired: header.RecursionDesired}
	if !hasTXT && !hasMX && !hasA {
		reply.RCode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(nil, reply)
	if b.StartQuestions() != nil || b.Question(q) != nil || b.StartAnswers() != nil {
		return nil, false
	}
	head := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}
	switch {
	case q.Type == dnsmessage.TypeTXT:
		for _, txt := range txts {
			// A TXT string holds at most 255 bytes; longer values are split.
			var parts []string
			for len(txt) > 255 {
				parts, txt = append(parts, txt[:255]), txt[255:]
			}
			parts = append(parts, txt)
			if b.TXTResource(head, dnsmessage.TXTResource{TXT: parts}) != nil {
				return nil, false
			}
		}
	case q.Type == dnsmessage.TypeMX && hasMX:
		host, err := dnsmessage.NewName(mx + ".")
		if err != nil || b.MXResource(head, dnsmessage.MXResource{Pref: 10, MX: host}) != nil {
			return nil, false
		}
	case q.Type == dnsmessage.TypeA && hasA:
		ip := net.ParseIP(address).To4()
		if ip == nil || b.AResource(head, dnsmessage.AResource{A: [4]byte(ip)}) != nil {
			return nil, false
		}
	}
	answer, err := b.Finish()
	return answer, err == nil
}

// submissionFixture is the credential fixture's server with the SMTP gateway's
// submission port in front of it, and the DNS an install publishes for
// example.test: SPF for the SMTP gateway's address only, DMARC p=reject, and
// the domain's DKIM key.
type submissionFixture struct {
	*credentialFixture
	api     *apiclient.Client
	addr    string
	rootCAs *x509.CertPool
	routeUp *bool // set once the mail route around the fixture is running
}

func newSubmissionFixture(t *testing.T) *submissionFixture {
	t.Helper()
	f := newCredentialFixture(t)

	record := giveDomainDKIMKey(t, f.db, "example.test")
	serveZone(t, dnsZone{
		txt: map[string][]string{
			"example.test":                {"v=spf1 ip4:192.0.2.25 -all"},
			"_dmarc.example.test":         {"v=DMARC1; p=reject"},
			"rm1._domainkey.example.test": {record},
		},
		mx: map[string]string{"example.test": localSendMX},
		a:  map[string]string{localSendMX: "127.0.0.1"},
	})
	// The inbound checks a domain runs, without the greylist that would hold a
	// first message from this server back for minutes.
	var domain models.Domain
	if err := f.db.Where("name = ?", "example.test").First(&domain).Error; err != nil {
		t.Fatal(err)
	}
	steps, err := json.Marshal([]pipeline.FilterConfig{
		{Name: "spf_check", Type: pipeline.FilterTypeAction, Enabled: true, Unskippable: true},
		{Name: "dkim_verify", Type: pipeline.FilterTypeTransform, Enabled: true, Unskippable: true},
		{Name: "dmarc_check", Type: pipeline.FilterTypeAction, Enabled: true, Unskippable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Active: true, Filters: steps}).Error; err != nil {
		t.Fatal(err)
	}

	api, addr, rootCAs := startSubmission(t, f.db, f.router, f.gatewayRouter())
	return &submissionFixture{credentialFixture: f, api: api, addr: addr, rootCAs: rootCAs}
}

// gatewayAPI is the API client of the gateway whose client certificate names
// cn: the routes it calls on the internal listener go to internal, as that
// gateway, and the rest to public.
func gatewayAPI(t *testing.T, public, internal http.Handler, cn string) *apiclient.Client {
	t.Helper()
	pub := httptest.NewServer(public)
	t.Cleanup(pub.Close)
	in := httptest.NewServer(asGateway(cn, internal))
	t.Cleanup(in.Close)
	return apiclient.New(pub.URL, apiclient.WithInternalMTLS(in.URL, nil))
}

// startSubmission puts the SMTP gateway's submission port in front of the
// API's public and internal routers, with the gateway's two clients as
// cmd/smtp-gateway makes them: delivery with the SMTP delivery certificate,
// sign-in sessions with the submission one (#1586). It returns the delivery
// client, the port's address, and the roots the port's certificate verifies
// against.
func startSubmission(t *testing.T, db *gorm.DB, public, internal http.Handler) (*apiclient.Client, string, *x509.CertPool) {
	t.Helper()
	api := smtpgw.Clients{
		Delivery:   gatewayAPI(t, public, internal, deliveryCN),
		Submission: gatewayAPI(t, public, internal, submissionCN),
	}

	// The submission port's certificate: httptest's, which is valid for
	// 127.0.0.1.
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certSrv.Close)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(certSrv.Certificate())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("open the submission port: %v", err)
	}
	gateway := smtpgw.NewServer("mail.example.test", api,
		&tls.Config{Certificates: certSrv.TLS.Certificates, MinVersion: tls.VersionTLS12},
		smtpgw.NewStore(db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.Serve(smtpgw.SMTPListeners{SubmissionTLS: listener}); err != nil {
		t.Fatalf("start the SMTP gateway: %v", err)
	}
	t.Cleanup(gateway.Shutdown)
	return api.Delivery, listener.Addr().String(), rootCAs
}

// dial opens a submission session signed in as address with password.
func (f *submissionFixture) dial(address, password string) *smtp.Client {
	f.t.Helper()
	return dialSubmission(f.t, f.addr, f.rootCAs, address, password)
}

// dialSubmission opens a session on the submission port at addr, signed in as
// address with password.
func dialSubmission(t *testing.T, addr string, rootCAs *x509.CertPool, address, password string) *smtp.Client {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("dial the submission port: %v", err)
	}
	c, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		t.Fatalf("SMTP greeting: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Hello("client.test"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if err := c.Auth(smtp.PlainAuth("", address, password, "127.0.0.1")); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	return c
}

// submittedMessage is a message from from to to, with the given subject.
func submittedMessage(from, to, subject string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%d@client.test>\r\n\r\nSent over SMTP.\r\n",
		from, to, subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
}

// send sends a message over c, and returns the reply to its end of data.
func send(c *smtp.Client, from, to, message string) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(message)); err != nil {
		return err
	}
	return w.Close()
}

func isDMARCReject(err error) bool {
	var apiErr *apiclient.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden && strings.Contains(apiErr.Body, "DMARC policy reject")
}

func smtpCode(err error) int {
	var tp *textproto.Error
	if errors.As(err, &tp) {
		return tp.Code
	}
	return 0
}

func (f *submissionFixture) inInbox(subject string) bool {
	var n int64
	f.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND subject = ?", f.mailboxID, "INBOX", subject).Count(&n)
	return n == 1
}

// Mail submitted over SMTP to a mailbox on this server, its own included,
// reaches it, however the session signed in: through the real SMTP gateway, the
// outbound queue and worker, and the inbound port with its pipeline, with SPF
// -all, DMARC p=reject and the domain's DKIM key published. E2E stage 14 failed
// here with 550 5.7.1: the gateway delivered the message unsigned, from the
// submitting client's address, which SPF failed and DMARC refused.
func TestSMTPSubmission_ToALocalMailbox(t *testing.T) {
	f := newSubmissionFixture(t)

	// The DNS is in force: delivered as the gateway used to deliver it, the
	// message is refused.
	refused := submittedMessage(f.address, f.address, "unsigned, from the client")
	if _, err := f.api.DeliverMessage(&apiclient.DeliverRequest{
		Address: f.address, Sender: f.address, RawMessage: []byte(refused),
		ClientIP: "127.0.0.1", HeloName: "client.test",
	}); !isDMARCReject(err) {
		t.Fatalf("delivering unsigned from the client's address = %v, want a DMARC reject", err)
	}

	t.Run("with the mailbox's password", func(t *testing.T) {
		c := f.dial(f.address, credentialTestPassword)
		subject := "sent with the mailbox's password"
		if err := send(c, f.address, f.address, submittedMessage(f.address, f.address, subject)); err != nil {
			t.Fatalf("send to its own mailbox: %v", err)
		}
		f.wantInInboxSigned(subject)
	})

	t.Run("with an app password", func(t *testing.T) {
		created := f.createAppPassword("Thunderbird")
		c := f.dial(f.address, created.Password)
		subject := "sent with an app password"
		if err := send(c, f.address, f.address, submittedMessage(f.address, f.address, subject)); err != nil {
			t.Fatalf("send to its own mailbox: %v", err)
		}
		f.wantInInboxSigned(subject)
	})
}

// A submission whose domain has a key that cannot sign is accepted and queued,
// and the worker holds it on the queue rather than let it leave unsigned.
func TestSMTPSubmission_ThatCannotBeSignedWaitsOnTheQueue(t *testing.T) {
	f := newSubmissionFixture(t)
	if err := f.db.Model(&models.Domain{}).Where("name = ?", "example.test").Update("dkim_private_key", "not a key").Error; err != nil {
		t.Fatal(err)
	}
	subject := "the key cannot sign"
	c := f.dial(f.address, credentialTestPassword)
	if err := send(c, f.address, f.address, submittedMessage(f.address, f.address, subject)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	startMailRoute(t, f.db, f.router, f.gatewayRouter())
	row := queueRow(t, f.db, f.address, "deferred", "delivered", "bounced")
	if row.Status != "deferred" {
		t.Errorf("queue row: %s: %s; want deferred", row.Status, row.LastError)
	}
	if f.inInbox(subject) {
		t.Error("delivered unsigned")
	}
}

// wantInInboxSigned runs the mail route until the message has arrived, and checks
// that it arrived with the verdicts the signature earned.
func (f *submissionFixture) wantInInboxSigned(subject string) {
	f.t.Helper()
	if f.routeUp == nil {
		f.routeUp = new(bool)
		startMailRoute(f.t, f.db, f.router, f.gatewayRouter())
	}
	deadline := time.Now().Add(30 * time.Second)
	for !f.inInbox(subject) {
		if time.Now().After(deadline) {
			var rows []models.OutboundQueue
			f.db.Find(&rows)
			f.t.Fatalf("%q not in the INBOX; the queue: %+v", subject, rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var copy models.Message
	if err := f.db.Where("mailbox_id = ? AND folder = ? AND subject = ?", f.mailboxID, "INBOX", subject).First(&copy).Error; err != nil {
		f.t.Fatal(err)
	}
	for _, want := range []string{"DKIM-Signature:", "dkim=pass", "dmarc=pass"} {
		if !strings.Contains(copy.RawMessage, want) {
			f.t.Errorf("stored copy has no %q; its verdicts: %s", want, authenticationResults(copy.RawMessage))
		}
	}
}

// An app password sends only as its own mailbox, never as an account its
// account owns, although the account's password may.
func TestSMTPSubmission_AppPasswordNeverSendsAsALinkedMailbox(t *testing.T) {
	f := newSubmissionFixture(t)
	var domain models.Domain
	f.db.First(&domain)
	receipts := f.seedMailbox(domain.ID, "receipts")
	f.own(receipts, "receipts@example.test")
	const linked = "receipts@example.test"
	created := f.createAppPassword("Thunderbird")

	c := f.dial(f.address, created.Password)
	if err := c.Mail(linked); smtpCode(err) != 553 {
		t.Errorf("MAIL FROM a linked mailbox with an app password = %v, want 553", err)
	}
	_ = c.Reset()
	subject := "From a linked mailbox"
	if err := send(c, f.address, f.address, submittedMessage(linked, f.address, subject)); smtpCode(err) != 550 {
		t.Errorf("a linked From header with an app password = %v, want 550", err)
	}
	if f.inInbox(subject) {
		t.Error("delivered")
	}

	// The mailbox's password may.
	c = f.dial(f.address, credentialTestPassword)
	if err := c.Mail(linked); err != nil {
		t.Errorf("MAIL FROM a linked mailbox with the mailbox's password: %v", err)
	}
}
