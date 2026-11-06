package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/srs"
	"gorm.io/gorm"
)

const srsForwardSecret = "fake-srs-secret-current-for-tests"

// srsForwardWorker is a worker with a signer, over a transaction holding one hosted
// domain, the forwarding domain, whose DKIM key can sign and seal.
func srsForwardWorker(t *testing.T) (*Worker, *gorm.DB, string) {
	t.Helper()
	tx := openRelayRouteDB(t)
	// The worker records each SMTP session, and a missing table would abort the
	// transaction the test runs in.
	if err := tx.AutoMigrate(&models.TLSRPTSession{}); err != nil {
		t.Fatal(err)
	}
	forwarder := seedSignedDomain(t, tx, relayRouteMasterKey)
	w := &Worker{db: tx, masterKey: relayRouteMasterKey, hostname: "sender.test", allowPrivateDest: true,
		sendDeadline: defaultSendDeadline, srs: srs.New(srsForwardSecret, "")}
	return w, tx, forwarder
}

// forwardRow is the queue row buildRedirectForwards makes.
func forwardRow(sender, forwarder string) models.OutboundQueue {
	return models.OutboundQueue{
		Sender: sender, SendingDomain: forwarder, Recipient: "fwd@elsewhere.test", Domain: "elsewhere.test",
		RawMessage: "From: alice@orig.example\r\nTo: dana@" + forwarder + "\r\nSubject: Lunch?\r\n" +
			"Date: Mon, 02 Nov 2026 05:00:00 +0000\r\nMessage-ID: <m1@orig.example>\r\n\r\nAre you free?\r\n",
		ARCResults: "restmail; spf=pass smtp.mailfrom=orig.example", ARCChain: "none",
	}
}

// sentMailFrom delivers item over SMTP to a peer and returns its MAIL command.
func sentMailFrom(t *testing.T, w *Worker, item models.OutboundQueue) string {
	t.Helper()
	cert, _ := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{})
	addr := net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
	if err := w.deliverSMTP(addr, settings.host, item, nil, nil); err != nil {
		t.Fatalf("deliverSMTP: %v", err)
	}
	return mailCommand(finish().commands)
}

// A forward from outside leaves with an SRS address under the forwarding domain as
// its envelope sender, which reverses to the sender (#775).
func TestForwardFromOutsideIsRewritten(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	got := sentMailFrom(t, w, forwardRow("alice@orig.example", forwarder))

	re := regexp.MustCompile(`^MAIL FROM:<(SRS0=[A-Z2-7]{4}=[A-Z2-7]{2}=orig\.example=alice@` + regexp.QuoteMeta(forwarder) + `)>`)
	match := re.FindStringSubmatch(got)
	if match == nil {
		t.Fatalf("MAIL = %q, want an SRS address under %s", got, forwarder)
	}
	if sender, err := w.srs.Reverse(match[1], time.Now()); err != nil || sender != "alice@orig.example" {
		t.Fatalf("the address reverses to %q, %v", sender, err)
	}
}

// A forward through a domain's configured relay is rewritten too, and a message goes
// no other way when one is set.
func TestForwardThroughARelayIsRewritten(t *testing.T) {
	w, tx, forwarder := srsForwardWorker(t)
	cert, roots := relayTestCertificate(t, false)
	w.relayRoots = roots
	peer, finish := relayTestPeer(t, cert, relayPeerOptions{requireAUTH: true})
	enc, err := models.EncryptRelayPassword(relayTestPassword, relayRouteMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	seedRelay(t, tx, forwarder, models.DomainRelay{
		Transport: models.OutboundTransportRelay, Host: peer.host, Port: peer.port,
		TLSMode: models.RelayTLSStartTLS, Username: relayTestUser, PasswordEnc: enc,
	})

	if err := w.deliver(context.Background(), forwardRow("alice@orig.example", forwarder)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := mailCommand(finish().commands); !strings.HasPrefix(got, "MAIL FROM:<SRS0=") {
		t.Fatalf("MAIL = %q, want an SRS address", got)
	}
}

// A sender on a domain hosted here passes SPF as it is, in any spelling of the
// domain. A sender on another hosted domain is hosted here all the same.
func TestForwardFromHostedSenderIsNotRewritten(t *testing.T) {
	w, tx, forwarder := srsForwardWorker(t)
	other := fmt.Sprintf("other-%d.test", time.Now().UnixNano())
	if err := tx.Create(&models.Domain{Name: other}).Error; err != nil {
		t.Fatal(err)
	}
	for _, sender := range []string{"dana@" + forwarder, "dana@" + strings.ToUpper(forwarder), "dana@" + other} {
		want := "MAIL FROM:<" + sender + ">"
		if got := sentMailFrom(t, w, forwardRow(sender, forwarder)); !strings.HasPrefix(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("MAIL = %q, want %q", got, want)
		}
	}
}

// A null reverse path stays null (RFC 5321 §6.1).
func TestForwardOfANullSenderIsNotRewritten(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	if got := sentMailFrom(t, w, forwardRow("", forwarder)); !strings.HasPrefix(got, "MAIL FROM:<>") {
		t.Fatalf("MAIL = %q, want a null reverse path", got)
	}
}

// Only a forward is rewritten: a row without a forward's ARC results keeps its sender.
func TestMailThatIsNotAForwardIsNotRewritten(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	item := forwardRow("alice@orig.example", forwarder)
	item.ARCResults, item.ARCChain = "", ""
	if got := sentMailFrom(t, w, item); !strings.HasPrefix(got, "MAIL FROM:<alice@orig.example>") {
		t.Fatalf("MAIL = %q, want the sender unchanged", got)
	}
}

// Without a secret there is nothing to sign with, and the forward goes as it did.
func TestForwardWithoutASecretKeepsItsSender(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	w.srs = nil
	if got := sentMailFrom(t, w, forwardRow("alice@orig.example", forwarder)); !strings.HasPrefix(got, "MAIL FROM:<alice@orig.example>") {
		t.Fatalf("MAIL = %q, want the sender unchanged", got)
	}
}

// A Unicode sender domain goes into the address in its ASCII form, so the forward
// does not need SMTPUTF8 when the original did not.
func TestForwardKeepsAnIDNSenderASCII(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	got := sentMailFrom(t, w, forwardRow("alice@bücher.example", forwarder))
	if !strings.Contains(got, "=xn--bcher-kva.example=alice@"+forwarder+">") {
		t.Fatalf("MAIL = %q, want the domain as an A-label", got)
	}
}

// An address literal cannot go in a local part, so that sender stays as it is.
func TestForwardFromAnAddressLiteralKeepsItsSender(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	if got := sentMailFrom(t, w, forwardRow("alice@[192.0.2.1]", forwarder)); !strings.HasPrefix(got, "MAIL FROM:<alice@[192.0.2.1]>") {
		t.Fatalf("MAIL = %q, want the sender unchanged", got)
	}
}

// Not knowing whether the sender is hosted here retries: a forward does not go out
// with the wrong envelope sender.
func TestForwardRetriesWhenHostingIsUnknown(t *testing.T) {
	w, tx, forwarder := srsForwardWorker(t)
	item := forwardRow("alice@orig.example", forwarder)
	broken := tx.Begin()
	broken.Rollback()
	w.db = broken
	if _, err := w.forwardSender(item); err == nil {
		t.Fatal("no error when the domain lookup failed")
	}
}

// A forward that fails at the next hop is reported to the original sender, not to
// the SRS address, since the row keeps the sender and only the wire has the address.
func TestFailedForwardIsReportedToTheOriginalSender(t *testing.T) {
	w, tx, forwarder := srsForwardWorker(t)
	w.bounceMax, w.bounceWindow = 5, time.Hour
	if err := tx.AutoMigrate(&models.Mailbox{}, &models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	author := fmt.Sprintf("author-%d@orig.example", time.Now().UnixNano())
	item := forwardRow(author, forwarder)
	if sent := sentMailFrom(t, w, item); !strings.Contains(sent, "SRS0=") {
		t.Fatalf("MAIL = %q, want an SRS address", sent)
	}
	w.generateBounce(item, &SMTPError{Code: 550, Enhanced: "5.1.1", Message: "no such user"})

	var notice models.OutboundQueue
	if err := tx.Where("recipient = ?", author).First(&notice).Error; err != nil {
		t.Fatalf("no DSN to %s: %v", author, err)
	}
	if notice.Sender != "" || !notice.IsDSN {
		t.Fatalf("DSN row = %+v, want a null sender, is_dsn", notice)
	}
}

// A RESTMAIL delivery authenticates the sending server, not SPF, and keeps the sender.
func TestForwardOverRESTMAILIsNotRewritten(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	var from string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			From string `json:"from"`
		}
		_ = json.Unmarshal(body, &payload)
		from = payload.From
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	w.restmailRoots = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	if err := w.deliverRESTMAILHTTPS(srv.URL, forwardRow("alice@orig.example", forwarder)); err != nil {
		t.Fatalf("deliverRESTMAILHTTPS: %v", err)
	}
	if from != "alice@orig.example" {
		t.Fatalf("from = %q, want the sender unchanged", from)
	}
}

// The row the SMTP gateway queues for a bounce to an SRS address leaves with a null
// reverse path, to the original sender, as the forwarding domain.
func TestRelayedBounceLeavesWithANullReversePath(t *testing.T) {
	w, _, forwarder := srsForwardWorker(t)
	cert, _ := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{})
	addr := net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
	item := models.OutboundQueue{
		Sender: "", SendingDomain: forwarder, Recipient: "alice@orig.example", Domain: "orig.example", IsDSN: true,
		RawMessage: "From: mailer-daemon@elsewhere.test\r\nTo: alice@orig.example\r\nSubject: Undelivered\r\n\r\nNo such user.\r\n",
	}
	if err := w.deliverSMTP(addr, settings.host, item, nil, nil); err != nil {
		t.Fatalf("deliverSMTP: %v", err)
	}
	result := finish()
	if got := mailCommand(result.commands); !strings.HasPrefix(got, "MAIL FROM:<>") {
		t.Fatalf("MAIL = %q, want a null reverse path", got)
	}
	if commands := strings.Join(result.commands, "\n"); !strings.Contains(commands, "RCPT TO:<alice@orig.example>") {
		t.Fatalf("not sent to the original sender:\n%s", commands)
	}
}
