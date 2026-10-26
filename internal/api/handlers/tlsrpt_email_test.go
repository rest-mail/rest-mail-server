package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

func tlsReportEmail(t *testing.T, payload []byte, compressed bool) string {
	t.Helper()
	mediaType := "application/tlsrpt+json"
	if compressed {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		if _, err := writer.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		payload = buffer.Bytes()
		mediaType = "application/tlsrpt+gzip"
	}
	return "From: tlsrpt@reporter.test\r\nTo: reports@local.test\r\n" +
		"TLS-Report-Submitter: reporter.test\r\nTLS-Report-Domain: local.test\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=tlsrpt; boundary=report\r\n\r\n" +
		"--report\r\nContent-Type: text/plain\r\n\r\nTLS report\r\n" +
		"--report\r\nContent-Type: " + mediaType + "\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString(payload) + "\r\n--report--\r\n"
}

func tlsReportEmailBody(t *testing.T, domain string) []byte {
	t.Helper()
	report := makeReport(domain, 1)
	report.ContactInfo = "tlsrpt@reporter.test"
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signedTLSReport(t *testing.T, raw, service string) (string, dkim.TXTResolver) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	header, err := dkim.Sign([]byte(raw), dkim.SignOptions{
		Domain: "reporter.test", Selector: "report", PrivateKey: private,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(_ context.Context, name string) ([]string, error) {
		if name != "report._domainkey.reporter.test" {
			t.Fatalf("unexpected lookup: %s", name)
		}
		return []string{"v=DKIM1;k=ed25519;s=" + service + ";p=" + base64.StdEncoding.EncodeToString(public)}, nil
	}
	return "DKIM-Signature: " + header + "\r\n" + raw, resolver
}

func TestTLSReportEmailAuthentication(t *testing.T) {
	for _, service := range []string{"tlsrpt", "email", "*", "tlsrpt:email", "other"} {
		t.Run(service, func(t *testing.T) {
			raw, resolver := signedTLSReport(t, tlsReportEmail(t, tlsReportEmailBody(t, "local.test"), true), service)
			receiver := NewTLSReportEmailReceiver(nil, "reports@local.test")
			receiver.resolver = resolver
			if got := receiver.authenticated(context.Background(), raw, "reporter.test"); got != (service != "other") {
				t.Fatalf("service %s: authenticated=%v", service, got)
			}
			for _, invalid := range []string{
				strings.Replace(raw, "b=", "l=0; b=", 1),
				strings.Replace(raw, "TLS report", "tampered", 1),
				strings.Replace(raw, "Content-Transfer-Encoding: base64", "Content-Transfer-Encoding: 8bit", 1),
			} {
				if receiver.authenticated(context.Background(), invalid, "reporter.test") {
					t.Fatal("tampered or limited signature passed")
				}
			}
			if receiver.authenticated(context.Background(), raw, "attacker.test") {
				t.Fatal("other signer's domain accepted")
			}
		})
	}
}

func TestTLSReportEmailParsingAndLimits(t *testing.T) {
	payload := tlsReportEmailBody(t, "local.test")
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			message, err := mail.ReadMessage(strings.NewReader(tlsReportEmail(t, payload, compressed)))
			if err != nil {
				t.Fatal(err)
			}
			report, body, err := readTLSReportEmail(message, "report")
			if err != nil {
				t.Fatal(err)
			}
			if report.ContactInfo != "tlsrpt@reporter.test" || !bytes.Equal(body, payload) {
				t.Fatal("report changed")
			}
		})
	}
	for _, payload := range [][]byte{
		[]byte("not JSON"),
		[]byte(strings.Repeat("x", int(tlsRPTMaxBodyBytes)+1)),
		buildTLSRPTBody(t, 101),
	} {
		for _, compressed := range []bool{false, true} {
			message, err := mail.ReadMessage(strings.NewReader(tlsReportEmail(t, payload, compressed)))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := readTLSReportEmail(message, "report"); err == nil {
				t.Fatal("invalid or over-limit report accepted")
			}
		}
	}
}

func TestTLSReportEmailUnsignedAndUnrelated(t *testing.T) {
	receiver := NewTLSReportEmailReceiver(nil, "reports@local.test")
	receiver.resolver = func(context.Context, string) ([]string, error) {
		t.Fatal("unsigned mail triggered DNS")
		return nil, nil
	}
	raw := tlsReportEmail(t, tlsReportEmailBody(t, "local.test"), true)
	consumed, err := receiver.receive(context.Background(), "reports@local.test", raw)
	if !consumed || err != nil {
		t.Fatalf("unsigned report: consumed=%v, err=%v", consumed, err)
	}
	for _, tc := range []struct{ recipient, raw string }{
		{"other@local.test", raw},
		{"reports@local.test", "From: person@reporter.test\r\n\r\nOrdinary mail"},
		{"reports@local.test", "From: person@reporter.test\r\n\r\n" + strings.Repeat("x", tlsRPTMaxEmailBytes+1)},
	} {
		consumed, err := receiver.receive(context.Background(), tc.recipient, tc.raw)
		if consumed || err != nil {
			t.Fatal("ordinary mail consumed")
		}
	}
	var disabled *TLSReportEmailReceiver
	if consumed, err := disabled.receive(context.Background(), "reports@local.test", raw); consumed || err != nil {
		t.Fatal("nil receiver consumed mail")
	}
}

func TestTLSReportEmailStorage(t *testing.T) {
	database := openTLSRPTTestDB(t)
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: fmt.Sprintf("email-report-%d.test", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	receiver := NewTLSReportEmailReceiver(tx, "reports@local.test")
	raw, resolver := signedTLSReport(t, tlsReportEmail(t, tlsReportEmailBody(t, domain.Name), true), "tlsrpt")
	receiver.resolver = resolver
	consumed, err := receiver.receive(context.Background(), "reports@local.test", raw)
	if !consumed || err != nil {
		t.Fatalf("signed report: consumed=%v, err=%v", consumed, err)
	}
	var rows []models.TLSReport
	if err := tx.Where("domain_id = ?", domain.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ReportingOrg != "Reporter Inc" {
		t.Fatalf("stored reports: %+v", rows)
	}
	unsigned := tlsReportEmail(t, tlsReportEmailBody(t, domain.Name), false)
	if consumed, err := receiver.receive(context.Background(), "reports@local.test", unsigned); !consumed || err != nil {
		t.Fatal("unsigned report was not ignored")
	}
	var count int64
	tx.Model(&models.TLSReport{}).Where("domain_id = ?", domain.ID).Count(&count)
	if count != 1 {
		t.Fatalf("unsigned report stored: count=%d", count)
	}
}

// Both registered delivery routes consume authenticated reports into the same
// table; neither retains an unsigned report as ordinary mailbox mail.
func TestTLSReportEmailDeliveryRoutes(t *testing.T) {
	database := openMsgDeleteTestDB(t)
	if err := database.AutoMigrate(&models.TLSReport{}); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedMsgDeleteFixture(t, tx, 0)
	if err := tx.Model(&models.Domain{}).Where("id = ?", fixture.mailbox.DomainID).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	receiver := NewTLSReportEmailReceiver(tx, fixture.mailbox.Address)
	messageHandler := NewMessageHandler(tx, nil, nil, "", nil)
	messageHandler.SetTLSReportEmailReceiver(receiver)
	restmailHandler := NewRestmailHandler(tx, pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default()), nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	restmailHandler.SetTLSReportEmailReceiver(receiver)
	for _, signed := range []bool{false, true} {
		for _, transport := range []string{"SMTP", "RESTMAIL"} {
			t.Run(fmt.Sprintf("%s/signed=%v", transport, signed), func(t *testing.T) {
				raw := tlsReportEmail(t, tlsReportEmailBody(t, "delq.test"), true)
				if signed {
					var resolver dkim.TXTResolver
					raw, resolver = signedTLSReport(t, raw, "tlsrpt")
					receiver.resolver = resolver
				}
				recorder := httptest.NewRecorder()
				if transport == "SMTP" {
					body, _ := json.Marshal(map[string]any{"mailbox_id": fixture.mailbox.ID, "sender": "tlsrpt@reporter.test", "raw_message": []byte(raw)})
					deliveryRoute(messageHandler)(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(body)))
				} else {
					body, _ := json.Marshal(map[string]any{"from": "tlsrpt@reporter.test", "to": []string{fixture.mailbox.Address}, "raw_message": raw})
					restmailRoutes(restmailHandler).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewReader(body)))
				}
				if recorder.Code < 200 || recorder.Code >= 300 {
					t.Fatalf("delivery: %d %s", recorder.Code, recorder.Body)
				}
			})
		}
	}
	var count int64
	if err := tx.Model(&models.TLSReport{}).Where("domain_id = ?", fixture.mailbox.DomainID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("got %d reports, want only the two signed deliveries", count)
	}
	if got := countMessages(t, tx, fixture.mailbox.ID); got != 0 {
		t.Fatalf("reports retained as mail: %d", got)
	}
}
