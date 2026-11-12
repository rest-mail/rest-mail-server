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

type sentCopyLocalBackend struct {
	*apiclient.Client
	recipient string
}

func (backend *sentCopyLocalBackend) CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error) {
	response := &apiclient.MailboxCheckResponse{}
	response.Data.Exists = address == backend.recipient
	return response, nil
}

func (backend *sentCopyLocalBackend) DeliverMessage(request *apiclient.DeliverRequest) (*apiclient.DeliverResponse, error) {
	if request.Address != backend.recipient {
		return nil, fmt.Errorf("unexpected local recipient %q", request.Address)
	}
	return &apiclient.DeliverResponse{}, nil
}

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
	var queued int64
	if err := db.Model(&models.OutboundQueue{}).Count(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("local-only submission queued %d outbound messages, want none", queued)
	}
}
