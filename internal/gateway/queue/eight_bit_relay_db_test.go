package queue

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	arc "github.com/rest-mail/go-arc/pkg/arc"
	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"
	smtpserver "github.com/rest-mail/go-smtp/pkg/smtp/server"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgateway "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm"
)

// eightBitMail is a message as a real sender writes it, of a kind a text column can't hold.
type eightBitMail struct {
	name    string
	message string
}

func eightBitMails(from, to string) []eightBitMail {
	head := func(mime string) string {
		return "From: " + from + "\r\nTo: " + to + "\r\nSubject: shape\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <" +
			strconv.FormatInt(time.Now().UnixNano(), 36) + "@remote.test>\r\n" + mime + "\r\n"
	}
	// Every octet but CR and LF, which only end its lines, so DATA can carry it. A line starts
	// with a dot, which SMTP must stuff and the far end must unstuff.
	var binary strings.Builder
	binary.WriteString(".leading dot\r\n")
	for i := 0; i < 2048; i++ {
		if b := byte(i % 256); b != '\r' && b != '\n' {
			binary.WriteByte(b)
		}
		if i%64 == 63 {
			binary.WriteString("\r\n")
		}
	}
	return []eightBitMail{
		{"Latin-1 no charset", head("MIME-Version: 1.0\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: 8bit\r\n") + "Stra\xdfe \xa3100 caf\xe9\r\n"},
		{"no MIME headers", head("") + "Gr\xfc\xdfe\r\n"},
		{"binary attachment", head("MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n") + "--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsee attached\r\n--b1\r\nContent-Type: application/octet-stream; name=\"a.bin\"\r\nContent-Disposition: attachment; filename=\"a.bin\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + binary.String() + "\r\n--b1--\r\n"},
		{"NUL in text body", head("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n") + "before\x00after \xe9\r\n"},
	}
}

// mxSession is what one transaction brought to a real SMTP server.
type mxSession struct {
	body gosmtp.BodyType
	utf8 bool
	data string
}

// realMX is the go-smtp server, the receiver this gateway itself runs on, keeping the octets
// each transaction carried. refuse, when set, answers the end of a DATA.
type realMX struct {
	addr string

	mu       sync.Mutex
	sessions []mxSession
	dataSeen int
	refuse   func(attempt int) error
}

type realMXSession struct {
	mx   *realMX
	opts *gosmtp.MailOptions
}

func (s *realMXSession) Reset()        {}
func (s *realMXSession) Logout() error { return nil }
func (s *realMXSession) Mail(_ string, opts *gosmtp.MailOptions) error {
	s.opts = opts
	return nil
}
func (s *realMXSession) Rcpt(string, *gosmtp.RcptOptions) error { return nil }
func (s *realMXSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mx.mu.Lock()
	defer s.mx.mu.Unlock()
	s.mx.dataSeen++
	if s.mx.refuse != nil {
		if err := s.mx.refuse(s.mx.dataSeen); err != nil {
			return err
		}
	}
	session := mxSession{data: string(data)}
	if s.opts != nil {
		session.body, session.utf8 = s.opts.Body, s.opts.UTF8
	}
	s.mx.sessions = append(s.mx.sessions, session)
	return nil
}

func newRealMX(t *testing.T, smtpUTF8 bool, refuse func(attempt int) error) *realMX {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mx := &realMX{addr: listener.Addr().String(), refuse: refuse}
	server := smtpserver.New(smtpserver.BackendFunc(func(*smtpserver.Conn) (smtpserver.Session, error) {
		return &realMXSession{mx: mx}, nil
	}))
	server.Domain = "mx.elsewhere.test"
	server.EnableSMTPUTF8 = smtpUTF8
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return mx
}

func (mx *realMX) received() []mxSession {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	return append([]mxSession(nil), mx.sessions...)
}

// mxList answers with these hosts, most preferred first.
type mxList []string

func (l mxList) LookupMX(context.Context, string) ([]*net.MX, error) {
	records := make([]*net.MX, 0, len(l))
	for i, host := range l {
		records = append(records, &net.MX{Host: host, Pref: uint16(10 * (i + 1))})
	}
	return records, nil
}

func (l mxList) LookupHost(_ context.Context, host string) ([]string, error) {
	return []string{host}, nil
}

// eightBitDB is a scratch database with the whole schema, so a worker's claim sees only what the
// test queued.
func eightBitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openResetWorkerDB(t)
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// eightBitWorker is a real worker, as the gateway starts it, whose recipient domain has the given
// MX hosts, each dialled at the address next to it.
func eightBitWorker(t *testing.T, db *gorm.DB, hostAddr ...string) *Worker {
	t.Helper()
	var hosts mxList
	addrs := map[string]string{}
	for i := 0; i < len(hostAddr); i += 2 {
		hosts = append(hosts, hostAddr[i])
		addrs[hostAddr[i]] = hostAddr[i+1]
	}
	w := NewWorker(db, "mail.sender.test", 1, time.Second)
	w.allowPrivateDest = true
	w.resolver = hosts
	w.smtpAddr = func(host string) string { return addrs[host] }
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }
	w.SetMasterKey(relayRouteMasterKey)
	w.SetSuppressionTTL(time.Hour)
	return w
}

// signedSender is a mailbox on a hosted domain with a DKIM key, and removes what the test queued.
func signedSender(t *testing.T, db *gorm.DB) (domain, address string, mailboxID uint) {
	t.Helper()
	domain = seedSignedDomain(t, db, relayRouteMasterKey)
	var d models.Domain
	if err := db.Where("name = ?", domain).First(&d).Error; err != nil {
		t.Fatal(err)
	}
	mb := models.Mailbox{DomainID: d.ID, LocalPart: "alice", Address: "alice@" + domain, Active: true}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	return domain, mb.Address, mb.ID
}

// enqueue queues a message as SMTP submission does.
func enqueue(t *testing.T, db *gorm.DB, sender, recipient, message string) models.OutboundQueue {
	t.Helper()
	store := smtpgateway.NewStore(db, relayRouteMasterKey)
	if err := store.EnqueueOutbound(smtpgateway.OutboundMessage{
		Sender: sender, Recipient: recipient, Domain: recipient[strings.LastIndexByte(recipient, '@')+1:],
		RawMessage: message, BodyType: "8BITMIME",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	var row models.OutboundQueue
	if err := db.Where("recipient = ? AND sender = ?", recipient, sender).Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.RawMessage != message {
		t.Fatalf("the queue changed the message: %q", row.RawMessage)
	}
	return row
}

// storedExactly fails unless the row still holds these octets in the column that can hold them.
func storedExactly(t *testing.T, db *gorm.DB, id uint, message string) {
	t.Helper()
	var row struct {
		RawMessage string
		RawOctets  []byte
	}
	if err := db.Raw("SELECT raw_message, raw_octets FROM outbound_queue WHERE id = ?", id).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if models.SourceFitsText(message) {
		if row.RawMessage != message || row.RawOctets != nil {
			t.Fatalf("row %d: raw_message %q, raw_octets %d octets", id, row.RawMessage, len(row.RawOctets))
		}
		return
	}
	if row.RawMessage != "" || string(row.RawOctets) != message {
		t.Fatalf("row %d changed: raw_message %d octets, raw_octets %q, want %q", id, len(row.RawMessage), row.RawOctets, message)
	}
}

// dkimVerifies checks data carries one signature for domain, which verifies over what arrived.
func dkimVerifies(t *testing.T, db *gorm.DB, domain, data string) {
	t.Helper()
	resolver := dkimResolverFor(t, db, domain)
	signatures := dkim.Verify(context.Background(), []byte(data), resolver)
	if len(signatures) != 1 || signatures[0].Result != "pass" || signatures[0].Domain != domain {
		t.Fatalf("DKIM over what arrived = %+v", signatures)
	}
}

// dsnInInbox is the newest DSN the sender's mailbox got.
func dsnInInbox(t *testing.T, db *gorm.DB, mailboxID uint) models.Message {
	t.Helper()
	var dsn models.Message
	if err := db.Where("mailbox_id = ? AND is_dsn", mailboxID).Order("id DESC").First(&dsn).Error; err != nil {
		t.Fatalf("no DSN reached the sender: %v", err)
	}
	return dsn
}

// TestEightBitRelayArrivesExactly: the queue worker claims each shape as queued by submission and
// relays it through a real SMTP server, which gets the exact octets declared BODY=8BITMIME, with
// a signature that verifies over what arrived.
func TestEightBitRelayArrivesExactly(t *testing.T) {
	db := eightBitDB(t)
	mx := newRealMX(t, true, nil)
	domain, sender, _ := signedSender(t, db)
	w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
	for _, mail := range eightBitMails(sender, "bob@elsewhere.test") {
		t.Run(mail.name, func(t *testing.T) {
			before := len(mx.received())
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			w.processOne(0)
			after := readRow(t, db, row.ID)
			if after.Status != "delivered" {
				t.Fatalf("status %s: %s", after.Status, after.LastError)
			}
			got := mx.received()
			if len(got) != before+1 {
				t.Fatalf("%d transactions, want %d", len(got), before+1)
			}
			session := got[len(got)-1]
			if !strings.HasSuffix(session.data, mail.message) || !strings.HasPrefix(session.data, "DKIM-Signature: ") {
				t.Fatalf("arrived changed:\n got %q\nwant suffix %q", session.data, mail.message)
			}
			if signature, _, _ := strings.Cut(session.data, mail.message); strings.Count(signature, "DKIM-Signature:") != 1 {
				t.Fatalf("signed more than once: %q", signature)
			}
			if session.body != gosmtp.Body8BitMIME {
				t.Fatalf("BODY=%q, want 8BITMIME", session.body)
			}
			if session.utf8 {
				t.Fatal("tagged SMTPUTF8 with ASCII headers: RFC 6531 section 3.4 says it should not be")
			}
			dkimVerifies(t, db, domain, session.data)
			storedExactly(t, db, row.ID, mail.message)
		})
	}
}

// TestEightBitRelayThroughConfiguredRelay: the same through a domain's smarthost, over verified TLS.
func TestEightBitRelayThroughConfiguredRelay(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, _ := signedSender(t, db)
	cert, roots := relayTestCertificate(t, false)
	for _, mail := range eightBitMails(sender, "bob@elsewhere.test") {
		t.Run(mail.name, func(t *testing.T) {
			peer, finish := relayTestPeer(t, cert, relayPeerOptions{requireAUTH: true})
			db.Exec("DELETE FROM domain_relays")
			enc, err := models.EncryptRelayPassword(relayTestPassword, relayRouteMasterKey)
			if err != nil {
				t.Fatal(err)
			}
			seedRelay(t, db, domain, models.DomainRelay{Transport: models.OutboundTransportRelay, Host: peer.host, Port: peer.port,
				TLSMode: models.RelayTLSStartTLS, Username: relayTestUser, PasswordEnc: enc})
			w := eightBitWorker(t, db)
			w.relayRoots = roots
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			w.processOne(0)
			if after := readRow(t, db, row.ID); after.Status != "delivered" {
				t.Fatalf("status %s: %s", after.Status, after.LastError)
			}
			result := finish()
			if !strings.HasSuffix(result.data, mail.message) {
				t.Fatalf("arrived changed:\n got %q\nwant suffix %q", result.data, mail.message)
			}
			if command := mailCommand(result.commands); !strings.Contains(command, "BODY=8BITMIME") || strings.Contains(command, "SMTPUTF8") {
				t.Fatalf("MAIL = %q", command)
			}
			dkimVerifies(t, db, domain, result.data)
			storedExactly(t, db, row.ID, mail.message)
		})
	}
}

// TestEightBitRetryAfterTemporaryFailure: the peer takes the whole message and answers 451, the
// row waits, and the next claim sends the same octets, read back from the row and not from the
// first attempt. A row whose retries have run out bounces and tells the sender.
func TestEightBitRetryAfterTemporaryFailure(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, mailboxID := signedSender(t, db)
	temporary := &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "try later"}
	for _, mail := range eightBitMails(sender, "bob@elsewhere.test") {
		t.Run(mail.name, func(t *testing.T) {
			mx := newRealMX(t, true, func(attempt int) error {
				if attempt == 1 {
					return temporary
				}
				return nil
			})
			w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			w.processOne(0)
			waiting := readRow(t, db, row.ID)
			if waiting.Status != "deferred" || waiting.Attempts != 1 || waiting.LastErrorCode != 451 {
				t.Fatalf("after 451: status %s, attempts %d, code %d: %s", waiting.Status, waiting.Attempts, waiting.LastErrorCode, waiting.LastError)
			}
			if !waiting.NextAttempt.After(time.Now()) {
				t.Fatalf("next attempt %s is not later", waiting.NextAttempt)
			}
			storedExactly(t, db, row.ID, mail.message)
			w.processOne(0) // not due yet
			if len(mx.received()) != 0 {
				t.Fatal("retried before its time")
			}
			if err := db.Model(&models.OutboundQueue{}).Where("id = ?", row.ID).Update("next_attempt", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			w.processOne(0)
			after := readRow(t, db, row.ID)
			if after.Status != "delivered" || after.Attempts != 2 {
				t.Fatalf("after retry: status %s, attempts %d: %s", after.Status, after.Attempts, after.LastError)
			}
			got := mx.received()
			if len(got) != 1 || !strings.HasSuffix(got[0].data, mail.message) {
				t.Fatalf("retry changed the message: %d transactions: %+v", len(got), got)
			}
			dkimVerifies(t, db, domain, got[0].data)
			storedExactly(t, db, row.ID, mail.message)
		})
		t.Run(mail.name+" retries run out", func(t *testing.T) {
			mx := newRealMX(t, true, func(int) error { return temporary })
			w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			if err := db.Model(&models.OutboundQueue{}).Where("id = ?", row.ID).Update("max_retries", 1).Error; err != nil {
				t.Fatal(err)
			}
			w.processOne(0)
			if after := readRow(t, db, row.ID); after.Status != "bounced" {
				t.Fatalf("status %s: %s", after.Status, after.LastError)
			}
			dsn := dsnInInbox(t, db, mailboxID)
			if !strings.Contains(string(dsn.Source()), "Status: 4.3.0") || !strings.Contains(string(dsn.Source()), "Subject: shape") {
				t.Fatalf("DSN does not say what failed: %q", dsn.Source())
			}
			storedExactly(t, db, row.ID, mail.message)
		})
	}
}

// deadAddr is an address nothing listens on.
func deadAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().String()
}

// TestEightBitWithoutEightBitMIMEBounces: RFC 6152 section 3 gives a client two options when the
// next hop lacks 8BITMIME and it has 8-bit data: convert it, or treat it as a permanent error.
// REST Mail doesn't convert, so it bounces, sends nothing and suppresses nothing.
func TestEightBitWithoutEightBitMIMEBounces(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, mailboxID := signedSender(t, db)
	var sendingDomainID uint
	if err := db.Model(&models.Domain{}).Where("name = ?", domain).Select("id").Scan(&sendingDomainID).Error; err != nil {
		t.Fatal(err)
	}
	cert, roots := relayTestCertificate(t, false)
	bouncedUntold := func(t *testing.T, row models.OutboundQueue, message string) {
		t.Helper()
		after := readRow(t, db, row.ID)
		if after.Status != "bounced" || !strings.Contains(after.LastError, "5.6.3") || after.FailureCategory != models.FailureOtherPermanent {
			t.Fatalf("status %s, category %s: %s", after.Status, after.FailureCategory, after.LastError)
		}
		// The address is fine: the next hop lacked a feature, and later mail must still go to it.
		var suppressed int64
		if err := db.Model(&models.Suppression{}).Where("domain_id = ?", sendingDomainID).Count(&suppressed).Error; err != nil {
			t.Fatal(err)
		}
		if suppressed != 0 {
			t.Fatalf("a missing 8BITMIME suppressed %d recipients", suppressed)
		}
		dsn := dsnInInbox(t, db, mailboxID)
		if !strings.Contains(string(dsn.Source()), "Status: 5.6.3") {
			t.Fatalf("DSN = %q", dsn.Source())
		}
		storedExactly(t, db, row.ID, message)
	}
	for _, mail := range eightBitMails(sender, "bob@elsewhere.test") {
		t.Run("MX "+mail.name, func(t *testing.T) {
			legacy := newNoFallbackMX(t, false)
			w := eightBitWorker(t, db, "mx.elsewhere.test", legacy.addr)
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			w.processOne(0)
			if legacy.mails.Load() != 0 {
				t.Fatal("8-bit data sent to a peer that does not offer 8BITMIME")
			}
			bouncedUntold(t, row, mail.message)
		})
		t.Run("relay "+mail.name, func(t *testing.T) {
			peer, finish := relayTestPeer(t, cert, relayPeerOptions{no8BIT: true, noUTF8: true, requireAUTH: true})
			db.Exec("DELETE FROM domain_relays")
			enc, err := models.EncryptRelayPassword(relayTestPassword, relayRouteMasterKey)
			if err != nil {
				t.Fatal(err)
			}
			seedRelay(t, db, domain, models.DomainRelay{Transport: models.OutboundTransportRelay, Host: peer.host, Port: peer.port,
				TLSMode: models.RelayTLSStartTLS, Username: relayTestUser, PasswordEnc: enc})
			w := eightBitWorker(t, db)
			w.relayRoots = roots
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			w.processOne(0)
			db.Exec("DELETE FROM domain_relays")
			if command := mailCommand(finish().commands); command != "" {
				t.Fatalf("8-bit data offered to a relay without 8BITMIME: %s", command)
			}
			bouncedUntold(t, row, mail.message)
		})
	}
	t.Run("a sender outside gets the DSN by mail", func(t *testing.T) {
		legacy := newNoFallbackMX(t, false)
		w := eightBitWorker(t, db, "mx.elsewhere.test", legacy.addr)
		mail := eightBitMails("author@original.test", "bob@elsewhere.test")[0]
		row := enqueue(t, db, "author@original.test", "bob@elsewhere.test", mail.message)
		if err := db.Model(&models.OutboundQueue{}).Where("id = ?", row.ID).Update("sending_domain", domain).Error; err != nil {
			t.Fatal(err)
		}
		w.processOne(0)
		if after := readRow(t, db, row.ID); after.Status != "bounced" {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		var notice models.OutboundQueue
		if err := db.Where("recipient = ? AND is_dsn", "author@original.test").First(&notice).Error; err != nil {
			t.Fatalf("no DSN queued: %v", err)
		}
		if notice.Sender != "" || !strings.Contains(notice.RawMessage, "Status: 5.6.3") {
			t.Fatalf("DSN: sender %q, %q", notice.Sender, notice.RawMessage)
		}
		db.Delete(&notice)
	})
	t.Run("ASCII still goes", func(t *testing.T) {
		legacy := newNoFallbackMX(t, false)
		w := eightBitWorker(t, db, "mx.elsewhere.test", legacy.addr)
		store := smtpgateway.NewStore(db, relayRouteMasterKey)
		if err := store.EnqueueOutbound(smtpgateway.OutboundMessage{Sender: sender, Recipient: "ascii@elsewhere.test", Domain: "elsewhere.test",
			RawMessage: "From: " + sender + "\r\nTo: ascii@elsewhere.test\r\nSubject: plain\r\n\r\nplain\r\n"}); err != nil {
			t.Fatal(err)
		}
		w.processOne(0)
		if legacy.mails.Load() != 1 {
			t.Fatalf("%d MAIL commands, want 1", legacy.mails.Load())
		}
	})
}

// TestEightBitPicksAnMXThatCanTakeIt: lacking 8BITMIME is the fault of one host. Another MX that
// offers it gets the message, and one that can't be reached yet keeps the message waiting for it.
func TestEightBitPicksAnMXThatCanTakeIt(t *testing.T) {
	db := eightBitDB(t)
	_, sender, _ := signedSender(t, db)
	mail := eightBitMails(sender, "bob@elsewhere.test")[0]

	t.Run("the next MX offers it", func(t *testing.T) {
		legacy, modern := newNoFallbackMX(t, false), newRealMX(t, true, nil)
		w := eightBitWorker(t, db, "mx1.elsewhere.test", legacy.addr, "mx2.elsewhere.test", modern.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
		w.processOne(0)
		if after := readRow(t, db, row.ID); after.Status != "delivered" {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		if got := modern.received(); len(got) != 1 || !strings.HasSuffix(got[0].data, mail.message) || legacy.mails.Load() != 0 {
			t.Fatalf("modern MX got %+v, legacy MX %d MAIL", got, legacy.mails.Load())
		}
	})
	for _, order := range []string{"the preferred MX is down", "the last MX is down"} {
		t.Run(order, func(t *testing.T) {
			legacy := newNoFallbackMX(t, false)
			hosts := []string{"mx1.elsewhere.test", deadAddr(t), "mx2.elsewhere.test", legacy.addr}
			if order == "the last MX is down" {
				hosts[1], hosts[3] = hosts[3], hosts[1]
			}
			w := eightBitWorker(t, db, hosts...)
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			w.processOne(0)
			after := readRow(t, db, row.ID)
			if after.Status != "deferred" {
				t.Fatalf("status %s: an MX that may offer 8BITMIME is only down: %s", after.Status, after.LastError)
			}
			if legacy.mails.Load() != 0 {
				t.Fatal("8-bit data sent to a peer that does not offer 8BITMIME")
			}
			db.Delete(&models.OutboundQueue{}, row.ID)
		})
	}
}

// TestSMTPUTF8FollowsThePeer: without SMTPUTF8 at the next hop, a message that needs it is rejected
// (RFC 6531 section 3.2), and one that doesn't need it isn't tagged (section 3.4). 8-bit in the
// body alone needs only 8BITMIME.
func TestSMTPUTF8FollowsThePeer(t *testing.T) {
	db := eightBitDB(t)
	_, sender, mailboxID := signedSender(t, db)
	utf8Mail := "From: " + sender + "\r\nTo: bob@elsewhere.test\r\nSubject: caf\xc3\xa9\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <utf8@remote.test>\r\n\r\nGr\xfc\xdfe\r\n"

	t.Run("offered: it goes tagged", func(t *testing.T) {
		mx := newRealMX(t, true, nil)
		w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", utf8Mail)
		w.processOne(0)
		if after := readRow(t, db, row.ID); after.Status != "delivered" {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		got := mx.received()
		if len(got) != 1 || !strings.HasSuffix(got[0].data, utf8Mail) || !got[0].utf8 || got[0].body != gosmtp.Body8BitMIME {
			t.Fatalf("received %+v", got)
		}
		db.Delete(&models.OutboundQueue{}, row.ID)
	})
	t.Run("not offered: it bounces untouched", func(t *testing.T) {
		mx := newRealMX(t, false, nil)
		w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", utf8Mail)
		w.processOne(0)
		after := readRow(t, db, row.ID)
		if after.Status != "bounced" || !strings.Contains(after.LastError, "5.6.9") {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		if got := mx.received(); len(got) != 0 {
			t.Fatalf("sent anyway: %+v", got)
		}
		if dsn := dsnInInbox(t, db, mailboxID); !strings.Contains(string(dsn.Source()), "Status: 5.6.9") {
			t.Fatalf("DSN = %q", dsn.Source())
		}
		storedExactly(t, db, row.ID, utf8Mail)
		db.Delete(&models.OutboundQueue{}, row.ID)
	})
	t.Run("not offered: an 8-bit body with ASCII headers needs only 8BITMIME", func(t *testing.T) {
		mx := newRealMX(t, false, nil)
		w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
		for _, mail := range eightBitMails(sender, "bob@elsewhere.test") {
			row := enqueue(t, db, sender, "bob@elsewhere.test", mail.message)
			w.processOne(0)
			if after := readRow(t, db, row.ID); after.Status != "delivered" {
				t.Fatalf("%s: status %s: %s", mail.name, after.Status, after.LastError)
			}
			got := mx.received()
			if last := got[len(got)-1]; !strings.HasSuffix(last.data, mail.message) || last.utf8 {
				t.Fatalf("%s: received %+v", mail.name, last)
			}
			db.Delete(&models.OutboundQueue{}, row.ID)
		}
	})
}

// eightBitAPI is a real inbound API on db: the delivery route and RESTMAIL behind a real SMTP
// inbound listener, with script as the domain's Sieve when it isn't empty.
type eightBitAPI struct {
	url, smtpAddr string
}

func newEightBitAPI(t *testing.T, db *gorm.DB, domain, script string) *eightBitAPI {
	t.Helper()
	var forwarder models.Domain
	if err := db.Where("name = ?", domain).First(&forwarder).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&forwarder).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	configured := []pipeline.FilterConfig{{Name: "recipient_check", Type: pipeline.FilterTypeAction, Enabled: true}}
	if script != "" {
		config, _ := json.Marshal(map[string]string{"script": script})
		configured = append(configured, pipeline.FilterConfig{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: config})
	}
	stored, _ := json.Marshal(configured)
	if err := db.Create(&models.Pipeline{DomainID: forwarder.ID, Direction: "inbound", Active: true, Filters: stored}).Error; err != nil {
		t.Fatal(err)
	}
	registry := pipeline.NewRegistry()
	registry.Register("recipient_check", filters.NewRecipientCheck(db))
	registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
	registry.Register("vacation", func([]byte) (pipeline.Filter, error) { return aliasForwardPass{}, nil })
	registry.Register("sieve", filters.NewSieveWithPolicy(filters.SieveRedirectPolicy{AllowExternal: true}))
	engine := pipeline.NewEngine(registry, nil)
	router := chi.NewRouter()
	routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	middleware := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	messages := handlers.NewMessageHandler(db, nil, engine, relayRouteMasterKey, nil)
	messages.RegisterDelivery(routes, middleware)
	handlers.NewMailboxHandler(db, nil, nil).RegisterAddressCheck(routes)
	rest := handlers.NewRestmailHandler(db, engine, nil, handlers.RestmailTarpitConfig{}, handlers.RestmailDeliverAuthConfig{TrustedCIDRs: []string{"127.0.0.0/8"}})
	rest.SetLocalDelivery(messages)
	rest.Register(routes, middleware)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	gateway := smtpgateway.NewServer("mx."+domain, apiclient.New(server.URL), nil, smtpgateway.NewStore(db, relayRouteMasterKey), connlimiter.New(connlimiter.Config{}))
	if err := gateway.ListenAndServe(smtpgateway.SMTPPorts{Inbound: port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Shutdown)
	return &eightBitAPI{url: server.URL, smtpAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
}

// sendSMTP sends a message in a real SMTP session.
func (a *eightBitAPI) sendSMTP(t *testing.T, from, to, message string) {
	t.Helper()
	client, err := smtp.Dial(a.smtpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Mail(from); err != nil {
		t.Fatal(err)
	}
	if err := client.Rcpt(to); err != nil {
		t.Fatal(err)
	}
	writer, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, message); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("end of data: %v", err)
	}
	if err := client.Quit(); err != nil {
		t.Fatal(err)
	}
}

// sendRESTMAIL sends a message as a REST Mail peer does.
func (a *eightBitAPI) sendRESTMAIL(t *testing.T, from, to, message string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"from": from, "to": []string{to}, "raw_message": []byte(message)})
	response, err := http.Post(a.url+"/restmail/messages", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("RESTMAIL %d: %s", response.StatusCode, body)
	}
}

// proveForward relays the one queued row through a real SMTP server and proves it arrived whole.
func proveForward(t *testing.T, db *gorm.DB, domain, target, message string, wantDeliveredTo []string) {
	t.Helper()
	var rows []models.OutboundQueue
	if err := db.Where("recipient = ?", target).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("queue rows for %s = %d, want 1", target, len(rows))
	}
	row := rows[0]
	if !strings.HasSuffix(row.RawMessage, message) || row.SendingDomain != domain || row.ARCResults == "" {
		t.Fatalf("queued forward: sending domain %s, ARC %q, %q", row.SendingDomain, row.ARCResults, row.RawMessage)
	}
	mx := newRealMX(t, true, func(attempt int) error {
		if attempt == 1 {
			return &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "try later"}
		}
		return nil
	})
	w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
	w.processOne(0)
	if after := readRow(t, db, row.ID); after.Status != "deferred" {
		t.Fatalf("status after 451: %s: %s", after.Status, after.LastError)
	}
	if err := db.Model(&models.OutboundQueue{}).Where("id = ?", row.ID).Update("next_attempt", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	w.processOne(0)
	if after := readRow(t, db, row.ID); after.Status != "delivered" {
		t.Fatalf("status: %s: %s", after.Status, after.LastError)
	}
	got := mx.received()
	if len(got) != 1 || !strings.HasSuffix(got[0].data, message) {
		t.Fatalf("arrived changed: %d transactions:\n got %+v\nwant suffix %q", len(got), got, message)
	}
	for _, address := range wantDeliveredTo {
		if !strings.Contains(got[0].data, "Delivered-To: "+address+"\r\n") {
			t.Fatalf("no Delivered-To for %s: %q", address, got[0].data)
		}
	}
	if got[0].body != gosmtp.Body8BitMIME {
		t.Fatalf("BODY=%q", got[0].body)
	}
	// What this server adds goes before the message: its octets are the last thing signed.
	signatures := dkimResolverFor(t, db, domain)
	if results := dkim.Verify(context.Background(), []byte(got[0].data), signatures); len(results) == 0 || results[0].Result != "pass" || results[0].Domain != domain {
		t.Fatalf("DKIM = %+v", results)
	}
	if verdict, reason := arc.Verify(context.Background(), []byte(got[0].data), signatures); verdict != "pass" {
		t.Fatalf("ARC = %s (%s)", verdict, reason)
	}
	db.Delete(&models.OutboundQueue{}, row.ID)
}

// dkimResolverFor answers the key lookups for mail domain signs.
func dkimResolverFor(t *testing.T, db *gorm.DB, domain string) func(context.Context, string) ([]string, error) {
	t.Helper()
	var d models.Domain
	if err := db.Where("name = ?", domain).First(&d).Error; err != nil {
		t.Fatal(err)
	}
	privateKey, err := models.LoadDKIMPrivateKey(d.DKIMPrivateKey, relayRouteMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := dkim.ParsePrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return func(_ context.Context, name string) ([]string, error) {
		if strings.TrimSuffix(name, ".") == d.DKIMSelector+"._domainkey."+domain {
			return []string{"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(publicKey)}, nil
		}
		return nil, nil
	}
}

// TestEightBitAliasForwardArrivesExactly: mail for an alias of a remote address, by SMTP and by
// RESTMAIL, reaches a real SMTP server with its exact octets, through a temporary failure.
func TestEightBitAliasForwardArrivesExactly(t *testing.T) {
	db := eightBitDB(t)
	domain := seedSignedDomain(t, db, relayRouteMasterKey)
	var forwarder models.Domain
	if err := db.Where("name = ?", domain).First(&forwarder).Error; err != nil {
		t.Fatal(err)
	}
	source, target := "alias@"+domain, "recipient@elsewhere.test"
	if err := db.Create(&models.Alias{DomainID: forwarder.ID, SourceAddress: source, DestinationAddress: target, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	api := newEightBitAPI(t, db, domain, "")
	for _, mail := range eightBitMails("author@original.test", source) {
		t.Run("SMTP "+mail.name, func(t *testing.T) {
			api.sendSMTP(t, "author@original.test", source, mail.message)
			proveForward(t, db, domain, target, mail.message, []string{source, target})
		})
		t.Run("RESTMAIL "+mail.name, func(t *testing.T) {
			api.sendRESTMAIL(t, "author@original.test", source, mail.message)
			proveForward(t, db, domain, target, mail.message, []string{source, target})
		})
	}
}

// TestEightBitSieveRedirectArrivesExactly: a Sieve redirect, with and without :copy, by SMTP and
// by RESTMAIL, reaches a real SMTP server with exact octets, and a kept copy stays exact too.
func TestEightBitSieveRedirectArrivesExactly(t *testing.T) {
	target := "fwd@elsewhere.test"
	for script, keep := range map[string]bool{
		`redirect "` + target + `";`:                         false,
		`require ["copy"]; redirect :copy "` + target + `";`: true,
	} {
		t.Run(fmt.Sprintf("keep=%v", keep), func(t *testing.T) {
			db := eightBitDB(t)
			domain := seedSignedDomain(t, db, relayRouteMasterKey)
			var d models.Domain
			if err := db.Where("name = ?", domain).First(&d).Error; err != nil {
				t.Fatal(err)
			}
			mailbox := models.Mailbox{DomainID: d.ID, LocalPart: "user", Address: "user@" + domain, Active: true, QuotaBytes: 1 << 30}
			if err := db.Create(&mailbox).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.QuotaUsage{MailboxID: mailbox.ID}).Error; err != nil {
				t.Fatal(err)
			}
			if err := grants.EnsureDefaults(db, &mailbox); err != nil {
				t.Fatal(err)
			}
			api := newEightBitAPI(t, db, domain, script)
			prove := func(t *testing.T, message string) {
				t.Helper()
				proveForward(t, db, domain, target, message, []string{target})
				var kept []models.Message
				if err := db.Where("mailbox_id = ?", mailbox.ID).Find(&kept).Error; err != nil {
					t.Fatal(err)
				}
				if keep != (len(kept) == 1) || (!keep && len(kept) != 0) {
					t.Fatalf("%d kept copies, keep=%v", len(kept), keep)
				}
				if keep && !bytes.HasSuffix(kept[0].Source(), []byte(message)) {
					t.Fatalf("kept copy changed: %q", kept[0].Source())
				}
				if err := db.Where("mailbox_id = ?", mailbox.ID).Delete(&models.Message{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, mail := range eightBitMails("author@original.test", mailbox.Address) {
				t.Run("SMTP "+mail.name, func(t *testing.T) {
					api.sendSMTP(t, "author@original.test", mailbox.Address, mail.message)
					prove(t, mail.message)
				})
				t.Run("RESTMAIL "+mail.name, func(t *testing.T) {
					api.sendRESTMAIL(t, "author@original.test", mailbox.Address, mail.message)
					prove(t, mail.message)
				})
			}
		})
	}
}

// TestEightBitRelayByRESTMAIL: a REST Mail peer takes the exact octets, including those SMTP DATA
// can't carry, since RESTMAIL carries them in base64.
func TestEightBitRelayByRESTMAIL(t *testing.T) {
	mails := append(eightBitMails("alice@sender.test", "bob@elsewhere.test"),
		eightBitMail{"bare LF among CRLF", "From: alice@sender.test\r\nSubject: raw\r\n\r\none\ntwo\xe9\r\n"},
		eightBitMail{"bare CR", "From: alice@sender.test\r\nSubject: raw\r\n\r\none\rtwo\xe9\r\n"})
	for _, mail := range mails {
		t.Run(mail.name, func(t *testing.T) {
			var received []byte
			server, w := discoveryServer(t, func(_ http.ResponseWriter, r *http.Request) {
				var body struct {
					RawMessage []byte `json:"raw_message"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				received = body.RawMessage
			})
			item := models.OutboundQueue{Sender: "alice@sender.test", Recipient: "bob@elsewhere.test", Domain: "elsewhere.test", RawMessage: mail.message}
			if err := w.deliverRESTMAILHTTPS(context.Background(), server.URL+"/restmail", item); err != nil {
				t.Fatal(err)
			}
			if string(received) != mail.message {
				t.Fatalf("arrived changed:\n got %q\nwant %q", received, mail.message)
			}
		})
	}
}
