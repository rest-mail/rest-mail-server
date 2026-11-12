package handlers

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	netsmtp "net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	gatewaysmtp "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/mtls/mtlstest"
)

// sentCopyLocalBackend answers that only recipient is on this server and
// records any direct delivery, which an authenticated submission must not make:
// a local recipient gets it through the outbound queue (#1564).
type sentCopyLocalBackend struct {
	*apiclient.Client
	recipient string
	delivered []string
}

func (backend *sentCopyLocalBackend) CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error) {
	response := &apiclient.MailboxCheckResponse{}
	response.Data.Exists = address == backend.recipient
	return response, nil
}

func (backend *sentCopyLocalBackend) DeliverMessageFrom(request *apiclient.DeliverRequest, _ apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	backend.delivered = append(backend.delivered, request.Address)
	return nil, fmt.Errorf("submission delivered directly to %q", request.Address)
}

// TestIMAPAppendAfterLocalOnlySubmissionKeepsOneSentCopy: a submission whose
// only recipient is on this server saves one Sent copy (#1809), goes on the
// outbound queue linked to that copy (#1564), and a mail app's own APPEND of
// the same message to Sent stores nothing more.
func TestIMAPAppendAfterLocalOnlySubmissionKeepsOneSentCopy(t *testing.T) {
	db := importProofDB(t)
	fixture := seedAccountsFixture(t, db)
	account := fixture.user
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	apiURL := imapProofAPI(t, db, account)
	backend := &sentCopyLocalBackend{Client: apiclient.New(apiURL), recipient: fixture.admin.Address}
	ca, err := mtlstest.NewCA("sent-copy-ca")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.IssueServer("smtp.test", []string{"smtp.test"}, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	server := gatewaysmtp.NewServer("smtp.test", backend, &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
	}, gatewaysmtp.NewStore(db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 10}))
	if err := server.ListenAndServe(gatewaysmtp.SMTPPorts{SubmissionTLS: port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Shutdown)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("could not trust test certificate")
	}
	connection, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", address, &tls.Config{
		RootCAs: roots, ServerName: "smtp.test", MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	client, err := netsmtp.NewClient(connection, "smtp.test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Auth(netsmtp.PlainAuth("", account.Address, accountsPassword, "smtp.test")); err != nil {
		t.Fatal(err)
	}
	if err := client.Mail(account.Address); err != nil {
		t.Fatal(err)
	}
	if err := client.Rcpt(backend.recipient); err != nil {
		t.Fatal(err)
	}
	const messageID = "local-one-copy@example.test"
	message := strings.ReplaceAll(sentCopyMessage(messageID), "sender@example.test", account.Address)
	message = strings.ReplaceAll(message, "someone@example.net", backend.recipient)
	writer, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sent := folderMessages(t, db, account, "Sent")
	if len(sent) != 1 || sent[0].SubmittedAt == nil {
		t.Fatalf("local-only submission saved %d Sent messages, want one marked as submitted", len(sent))
	}
	imap := newIMAPProofClient(t, apiURL)
	imap.ok("SELECT Sent")
	untagged, uid := imap.append("Sent", `(\Seen) `, message)
	if uid != sent[0].ID {
		t.Errorf("APPENDUID = %d, want submitted message %d", uid, sent[0].ID)
	}
	for _, line := range untagged {
		if strings.HasSuffix(line, "EXISTS") {
			t.Errorf("APPEND stored nothing but announced %q", line)
		}
	}
	stored := folderMessages(t, db, account, "Sent")
	if len(stored) != 1 || stored[0].ID != sent[0].ID || stored[0].RawMessage != sent[0].RawMessage {
		t.Fatalf("APPEND changed the submitted Sent copy: %+v", stored)
	}
	if len(backend.delivered) != 0 {
		t.Errorf("submission delivered directly to %v, want it queued", backend.delivered)
	}
	var queued []models.OutboundQueue
	if err := db.Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Recipient != backend.recipient {
		t.Fatalf("local-only submission queued %+v, want one row for %s", queued, backend.recipient)
	}
	if queued[0].MessageID == nil || *queued[0].MessageID != sent[0].ID {
		t.Errorf("queue row names message %v, want the Sent copy %d", queued[0].MessageID, sent[0].ID)
	}
}
