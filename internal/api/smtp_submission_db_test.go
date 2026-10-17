package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	smtpgw "github.com/restmail/restmail/internal/gateway/smtp"
	"golang.org/x/net/dns/dnsmessage"
)

// serveTXT answers DNS queries on the loopback from records (lower-case names
// without the trailing dot), and points this process's resolver at it until
// the test ends. A name it has no records for does not exist.
func serveTXT(t *testing.T, records map[string][]string) {
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
			if answer, ok := answerTXT(buf[:n], records); ok {
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

func answerTXT(query []byte, records map[string][]string) ([]byte, bool) {
	var p dnsmessage.Parser
	header, err := p.Start(query)
	if err != nil {
		return nil, false
	}
	q, err := p.Question()
	if err != nil {
		return nil, false
	}
	txts, known := records[strings.TrimSuffix(strings.ToLower(q.Name.String()), ".")]
	reply := dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true, RecursionDesired: header.RecursionDesired}
	if !known {
		reply.RCode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(nil, reply)
	if b.StartQuestions() != nil || b.Question(q) != nil || b.StartAnswers() != nil {
		return nil, false
	}
	if q.Type == dnsmessage.TypeTXT {
		for _, txt := range txts {
			// A TXT string holds at most 255 bytes; longer values are split.
			var parts []string
			for len(txt) > 255 {
				parts, txt = append(parts, txt[:255]), txt[255:]
			}
			parts = append(parts, txt)
			if b.TXTResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.TXTResource{TXT: parts}) != nil {
				return nil, false
			}
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
}

func newSubmissionFixture(t *testing.T) *submissionFixture {
	t.Helper()
	f := newCredentialFixture(t)

	private, public, err := dkim.GenerateKey(1024)
	if err != nil {
		t.Fatalf("generate a DKIM key: %v", err)
	}
	record, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatalf("DKIM record: %v", err)
	}
	// Stored as plaintext, as it is with no MASTER_KEY.
	if err := f.db.Model(&models.Domain{}).Where("name = ?", "example.test").
		Updates(models.Domain{DKIMSelector: "rm1", DKIMPrivateKey: private}).Error; err != nil {
		t.Fatalf("set the domain's DKIM key: %v", err)
	}
	serveTXT(t, map[string][]string{
		"example.test":                {"v=spf1 ip4:192.0.2.25 -all"},
		"_dmarc.example.test":         {"v=DMARC1; p=reject"},
		"rm1._domainkey.example.test": {record},
	})

	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	api := apiclient.New(srv.URL)

	// The submission port's certificate: httptest's, which is valid for
	// 127.0.0.1.
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certSrv.Close)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(certSrv.Certificate())

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	gateway := smtpgw.NewServer("mail.example.test", api,
		&tls.Config{Certificates: certSrv.TLS.Certificates, MinVersion: tls.VersionTLS12},
		smtpgw.NewStore(f.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.ListenAndServe(smtpgw.SMTPPorts{SubmissionTLS: port}); err != nil {
		t.Fatalf("start the SMTP gateway: %v", err)
	}
	t.Cleanup(gateway.Shutdown)

	return &submissionFixture{credentialFixture: f, api: api, addr: fmt.Sprintf("127.0.0.1:%d", port), rootCAs: rootCAs}
}

// dial opens a submission session signed in as address with password.
func (f *submissionFixture) dial(address, password string) *smtp.Client {
	f.t.Helper()
	conn, err := tls.Dial("tcp", f.addr, &tls.Config{RootCAs: f.rootCAs, MinVersion: tls.VersionTLS12})
	if err != nil {
		f.t.Fatalf("dial the submission port: %v", err)
	}
	c, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		f.t.Fatalf("SMTP greeting: %v", err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	if err := c.Hello("client.test"); err != nil {
		f.t.Fatalf("EHLO: %v", err)
	}
	if err := c.Auth(smtp.PlainAuth("", address, password, "127.0.0.1")); err != nil {
		f.t.Fatalf("AUTH: %v", err)
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
	return errors.As(err, &apiErr) && apiErr.StatusCode == 550 && strings.Contains(apiErr.Body, "DMARC policy reject")
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
// reaches it, however the session signed in: through the real SMTP gateway,
// the router and its inbound pipeline, with SPF -all, DMARC p=reject and the
// domain's DKIM key published. E2E stage 14 failed here with 550 5.7.1: the
// gateway delivered the message unsigned, from the submitting client's
// address, which SPF failed and DMARC refused.
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
		if !f.inInbox(subject) {
			t.Error("not in the INBOX")
		}
	})

	t.Run("with an app password", func(t *testing.T) {
		created := f.createAppPassword("Thunderbird")
		c := f.dial(f.address, created.Password)
		subject := "sent with an app password"
		if err := send(c, f.address, f.address, submittedMessage(f.address, f.address, subject)); err != nil {
			t.Fatalf("send to its own mailbox: %v", err)
		}
		if !f.inInbox(subject) {
			t.Error("not in the INBOX")
		}
	})
}

// An app password sends only as its own mailbox, never as one its account has
// linked, although the mailbox's password may.
func TestSMTPSubmission_AppPasswordNeverSendsAsALinkedMailbox(t *testing.T) {
	f := newSubmissionFixture(t)
	var domain models.Domain
	f.db.First(&domain)
	receipts := f.seedMailbox(domain.ID, "receipts")
	f.db.Create(&models.LinkedAccount{WebmailAccountID: f.accountID, MailboxID: receipts})
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
