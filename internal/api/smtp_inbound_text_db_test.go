package api

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
)

// The SMTP gateway delivers a message's source and not its text, so what a mailbox shows is
// what the server reads from that source: through the real inbound port, the real client and
// the router's delivery route, into the row. A message the parser can't read is still stored,
// with its body readable (#1915).
func TestSMTPInboundStoresTextReadFromTheSource(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	// What's under test is the text, so nothing in the pipeline judges these messages.
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[]`), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	gateway := smtpgw.NewServer("mail.example.test", apiclient.New(srv.URL), nil,
		smtpgw.NewStore(f.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.ListenAndServe(smtpgw.SMTPPorts{Inbound: port}); err != nil {
		t.Fatalf("start the SMTP gateway: %v", err)
	}
	t.Cleanup(gateway.Shutdown)

	const head = "From: sender@remote.test\r\nTo: chris@example.test\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	long := strings.Repeat("hello world\r\n", 10_000)
	for _, tc := range []struct {
		name, message, text, html string
	}{
		{name: "plain", message: head + "Message-ID: <plain@remote.test>\r\nSubject: plain\r\n\r\nbody\r\n", text: "body\r\n"},
		{name: "as UTF-8", message: head + "Message-ID: <utf8@remote.test>\r\nSubject: utf8\r\nContent-Type: text/plain; charset=utf-8\r\n\r\ncafé\r\n", text: "café\r\n"},
		{name: "quoted-printable Latin-1", message: head + "Message-ID: <latin@remote.test>\r\nSubject: latin\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9\r\n", text: "café\r\n"},
		{name: "a NUL only once decoded", message: head + "Message-ID: <nul@remote.test>\r\nSubject: nul\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nbe=00fore\r\n", text: "be�fore\r\n"},
		{name: "both alternatives", message: head + "Message-ID: <alt@remote.test>\r\nSubject: alt\r\nContent-Type: multipart/alternative; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\ntext\r\n--B\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--B--\r\n", text: "text", html: "<p>html</p>"},
		{name: "html only", message: head + "Message-ID: <html@remote.test>\r\nSubject: html\r\nContent-Type: text/html\r\n\r\n<p>only</p>\r\n", html: "<p>only</p>\r\n"},
		{name: "attachment only", message: head + "Message-ID: <pdf@remote.test>\r\nSubject: pdf\r\nContent-Type: application/pdf\r\n\r\n%PDF\r\n"},
		{name: "long enough to be written on its own", message: head + "Message-ID: <long@remote.test>\r\nSubject: long\r\n\r\n" + long, text: long},
		{name: "unparseable", message: "this line is not a header\r\nFrom: sender@remote.test\r\n\r\nstill readable\r\n", text: "still readable\r\n"},
		{name: "a header past 1 MiB", message: head + "Subject: " + strings.Repeat("y\r\n ", 300_000) + "y\r\n\r\nstill readable\r\n", text: "still readable\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.Message{}).Error; err != nil {
				t.Fatal(err)
			}
			client, err := smtp.Dial(free.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if err := client.Hello("client.test"); err != nil {
				t.Fatal(err)
			}
			if err := send(client, "sender@remote.test", f.address, tc.message); err != nil {
				t.Fatalf("send: %v", err)
			}
			var stored models.Message
			if err := f.db.Where("mailbox_id = ?", f.mailboxID).First(&stored).Error; err != nil {
				t.Fatalf("not stored: %v", err)
			}
			if stored.BodyText != tc.text || stored.BodyHTML != tc.html {
				t.Fatalf("stored text %.80q and html %.80q, want %.80q and %.80q", stored.BodyText, stored.BodyHTML, tc.text, tc.html)
			}
			if want := len(stored.Subject) + len(tc.text) + len(tc.html); stored.SizeBytes != want {
				t.Fatalf("size_bytes %d, want %d", stored.SizeBytes, want)
			}
			if !strings.HasSuffix(stored.RawMessage, tc.message) {
				t.Fatalf("source changed: %.200q", stored.RawMessage)
			}
			// The seal covers the text, however it was written.
			digest, err := stored.SealDigest()
			if err != nil {
				t.Fatal(err)
			}
			if stored.SealedHash == "" || digest != stored.SealedHash {
				t.Fatalf("seal %q, digest of what is stored %q", stored.SealedHash, digest)
			}
		})
	}
}
